package rules

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"

	"github.com/trevex/chalkos/pkg/config"
)

// Facts gives the node's facts by name; nil when it has none, as on a node that does not declare
// the fact. A node loads each fact once, on the first rule that reads it.
type Facts interface {
	Fact(name string) (any, error)
}

// Input is what rules are evaluated against: the config delivered and the config it replaces,
// each with its schema defaults, and the node's facts.
type Input struct {
	Documents []config.Document
	// Old is the running image's config; nil without one, as at an install, where field rules
	// that read oldSelf do not run and the rules of kinds see no oldDocuments.
	Old []config.Document
	// Facts is nil where no facts can be read, as in chalkctl: rules that read facts do not run.
	Facts Facts
}

// Failure is a rule the delivered config does not meet.
type Failure struct {
	Kind  string
	Takes Takes
	// Document and Path name the field of a field's rule; both are empty for a kind's rule.
	Document string
	Path     []string
	Message  string
}

// String names the field a field's rule is about; a kind's rule names what it is about in its
// message.
func (f Failure) String() string {
	if f.Path == nil {
		return f.Message
	}
	return fmt.Sprintf("%s %q %s: %s", f.Kind, f.Document, strings.Join(f.Path, "."), f.Message)
}

// FactError is a fact a rule reads that could not be loaded; it refuses the delivery.
type FactError struct {
	Name string
	Err  error
}

func (e *FactError) Error() string { return fmt.Sprintf("cannot check: fact %s: %v", e.Name, e.Err) }
func (e *FactError) Unwrap() error { return e.Err }

// Evaluate runs every rule of the set against in and returns those the config does not meet, in
// the set's order. An error is a rule that could not be evaluated, or a fact that could not be
// loaded, which refuses the delivery as well.
func (s *Set) Evaluate(in Input) ([]Failure, error) {
	cfg, oldCfg := configOf(in.Documents), configOf(in.Old)
	var failures []Failure
	for _, p := range s.Programs {
		if len(p.Facts) > 0 && in.Facts == nil {
			continue
		}
		if p.Path == nil {
			vars := map[string]any{
				"documents":    documentsOf(in.Documents, p),
				"oldDocuments": documentsOf(in.Old, p),
			}
			f, err := p.evaluate(vars, cfg, oldCfg, in.Facts, "")
			if err != nil {
				return nil, err
			}
			failures = append(failures, f...)
			continue
		}
		for _, d := range kindDocuments(in.Documents, p) {
			self, ok := valueAt(d.Hashed(), p.Path)
			if !ok {
				continue
			}
			oldSelf, hasOld := any(nil), false
			if old, ok := oldDocument(in.Old, p, d.Name); ok {
				oldSelf, hasOld = valueAt(old.Hashed(), p.Path)
			}
			if p.Transition && !hasOld {
				continue
			}
			vars := map[string]any{"self": self, "oldSelf": oldSelf}
			f, err := p.evaluate(vars, cfg, oldCfg, in.Facts, d.Name)
			if err != nil {
				return nil, err
			}
			failures = append(failures, f...)
		}
	}
	return failures, nil
}

// evaluate runs the program with the variables given; doc is the document of a field's rule.
func (p *Program) evaluate(vars map[string]any, cfg, oldCfg map[string]any, facts Facts, doc string) ([]Failure, error) {
	vars["config"], vars["oldConfig"] = cfg, oldCfg
	var factErr error
	for _, name := range p.Facts {
		vars["facts."+name] = func() ref.Val {
			v, err := facts.Fact(name)
			if err != nil {
				if factErr == nil {
					factErr = &FactError{Name: name, Err: err}
				}
				return types.NewErr("%v", err)
			}
			return adapter{}.NativeToValue(celValue(v))
		}
	}
	where := p.Where()
	if doc != "" {
		where = fmt.Sprintf("%s %q %s", p.Kind.Kind, doc, strings.Join(p.Path, "."))
	}
	out, _, err := p.check.Eval(vars)
	if factErr != nil {
		return nil, factErr
	}
	if err != nil {
		return nil, fmt.Errorf("cannot check: %s: rule %q: %v", where, p.Rule.Rule, err)
	}
	ok, isBool := out.(types.Bool)
	if !isBool {
		return nil, fmt.Errorf("cannot check: %s: rule %q yields %s, not a bool", where, p.Rule.Rule, out.Type().TypeName())
	}
	if ok {
		return nil, nil
	}
	f := Failure{Kind: p.Kind.Kind, Takes: p.Takes, Message: p.Rule.Message}
	if doc != "" {
		f.Document, f.Path = doc, p.Path
	}
	if p.message != nil {
		msg, _, err := p.message.Eval(vars)
		if factErr != nil {
			return nil, factErr
		}
		if s, isString := msg.(types.String); err == nil && isString {
			f.Message = string(s)
		} else if f.Message == "" {
			f.Message = fmt.Sprintf("the config does not meet the rule %s, whose messageExpression fails: %v", p.Rule.Rule, errors.Join(err, notString(msg)))
		}
	}
	if f.Message == "" {
		f.Message = "the config does not meet the rule " + p.Rule.Rule
	}
	return []Failure{f}, nil
}

func notString(v ref.Val) error {
	if v == nil {
		return nil
	}
	if _, ok := v.(types.String); ok {
		return nil
	}
	return fmt.Errorf("it yields %s, not a string", v.Type().TypeName())
}

// configOf is a config set as rules see it: objects by apiVersion, kind and name, Secret values
// replaced by their hashes.
func configOf(docs []config.Document) map[string]any {
	out := map[string]any{}
	for _, d := range docs {
		kinds, ok := out[d.APIVersion].(map[string]any)
		if !ok {
			kinds = map[string]any{}
			out[d.APIVersion] = kinds
		}
		names, ok := kinds[d.Kind].(map[string]any)
		if !ok {
			names = map[string]any{}
			kinds[d.Kind] = names
		}
		names[d.Name] = celValue(d.Hashed())
	}
	return out
}

// documentsOf is the documents of the program's kind by name.
func documentsOf(docs []config.Document, p *Program) map[string]any {
	out := map[string]any{}
	for _, d := range kindDocuments(docs, p) {
		out[d.Name] = celValue(d.Hashed())
	}
	return out
}

func kindDocuments(docs []config.Document, p *Program) []config.Document {
	var out []config.Document
	for _, d := range docs {
		if d.APIVersion == p.Kind.APIVersion && d.Kind == p.Kind.Kind {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b config.Document) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// oldDocument is the running config's document a field's rule compares with: the one of the same
// name, or for a kind a node has once, that one whatever its name, so renaming the document does
// not escape the rule.
func oldDocument(old []config.Document, p *Program, name string) (config.Document, bool) {
	docs := kindDocuments(old, p)
	if !p.Kind.Multiple {
		if len(docs) == 1 {
			return docs[0], true
		}
		return config.Document{}, false
	}
	for _, d := range docs {
		if d.Name == name {
			return d, true
		}
	}
	return config.Document{}, false
}

// valueAt is the value at path in obj, which must exist.
func valueAt(obj map[string]any, path []string) (any, bool) {
	var v any = obj
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[key]; !ok {
			return nil, false
		}
	}
	return celValue(v), true
}

// celValue converts JSON as documents and facts hold it for CEL: a json.Number becomes an int
// when it is one, else a double, as CEL compares ints with ints.
func celValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = celValue(val)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = celValue(val)
		}
		return out
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		f, _ := v.Float64()
		return f
	case float64:
		if v == float64(int64(v)) {
			return int64(v)
		}
		return v
	}
	return v
}
