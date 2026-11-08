package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/schema"
)

// Facts gives the node's facts by name; nil when it has none, as on a node that does not declare
// the fact. A node loads each fact once, on the first rule that reads it.
type Facts interface {
	Fact(name string) (any, error)
}

// Input is what rules are evaluated against: the config delivered and the config it replaces,
// each with its schema defaults, the node's name and the node's facts.
type Input struct {
	// Documents are the config delivered.
	Documents config.Redacted
	// Old is the running image's config; empty without one, as at an install, where field rules
	// that read oldSelf do not run and the rules of kinds see no oldDocuments.
	Old config.Redacted
	// Node is the node's name, which rules read as node.name: the inventory's name of the node,
	// which its node certificate carries.
	Node string
	// Facts is nil where no facts can be read, as in chalkctl: rules that read facts do not run.
	Facts Facts
}

// Failure is a rule the delivered config does not meet: the refusal it makes and what the change
// takes. A field's rule refuses its field, by its document's Name and its Path; a kind's rule
// names neither, and its message says what it is about.
type Failure struct {
	refusals.Refusal
	Takes schema.Takes
}

// FactError is a fact a rule reads that could not be loaded; it refuses the delivery.
type FactError struct {
	Name string
	Err  error
}

func (e *FactError) Error() string {
	return fmt.Sprintf("cannot check: fact %s: %s", e.Name, bounded(e.Err.Error()))
}
func (e *FactError) Unwrap() error { return e.Err }

// Evaluate runs every rule of the set against in and returns those the config does not meet, in
// the set's order. Every rule sees config, oldConfig and node; a kind's rules documents and
// oldDocuments, a field's self and oldSelf. An error is a rule that could not be evaluated, or a
// fact that could not be loaded, which refuses the delivery as well.
func (s *Set) Evaluate(in Input) ([]Failure, error) {
	next, old := in.Documents.Documents(), in.Old.Documents()
	cfg, oldCfg := configMap(next), configMap(old)
	node := nodeOf(in.Node)
	var failures []Failure
	for _, p := range s.Programs {
		if len(p.Facts) > 0 && in.Facts == nil {
			continue
		}
		if p.Path == nil {
			vars := map[string]any{
				"documents":    documentsOf(next, p),
				"oldDocuments": documentsOf(old, p),
			}
			f, err := p.evaluate(vars, cfg, oldCfg, node, in.Facts, "")
			if err != nil {
				return nil, err
			}
			failures = append(failures, f...)
			continue
		}
		for _, d := range kindDocuments(next, p) {
			self, ok := valueAt(d.Object, p.Path)
			if !ok {
				continue
			}
			oldSelf, hasOld := any(nil), false
			if o, ok := oldDocument(old, p, d.Name); ok {
				oldSelf, hasOld = valueAt(o.Object, p.Path)
			}
			if p.Transition && !hasOld {
				continue
			}
			vars := map[string]any{"self": self, "oldSelf": oldSelf}
			f, err := p.evaluate(vars, cfg, oldCfg, node, in.Facts, d.Name)
			if err != nil {
				return nil, err
			}
			failures = append(failures, f...)
		}
	}
	return failures, nil
}

// bindFacts binds <ident>.<name> in vars for each of the names to a function that loads the fact
// from facts when evaluation first reads it; nil facts reads every fact as null. The first fact
// that cannot be loaded is kept in factErr.
func bindFacts(vars map[string]any, ident string, names []string, facts Facts, factErr *error) {
	for _, name := range names {
		vars[ident+"."+name] = func() ref.Val {
			if facts == nil {
				return types.NullValue
			}
			v, err := facts.Fact(name)
			if err != nil {
				if *factErr == nil {
					*factErr = &FactError{Name: name, Err: err}
				}
				return types.NewErr("%v", err)
			}
			return adapter{}.NativeToValue(celValue(v))
		}
	}
}

// nodeOf is the node variable of a node named name.
func nodeOf(name string) map[string]any {
	return map[string]any{"name": name}
}

// evaluate runs the program with the variables given; doc is the document of a field's rule.
func (p *Program) evaluate(vars map[string]any, cfg, oldCfg, node map[string]any, facts Facts, doc string) ([]Failure, error) {
	vars["config"], vars["oldConfig"], vars["node"] = cfg, oldCfg, node
	vars[documentConfig] = cfg
	var factErr error
	bindFacts(vars, "facts", p.Facts, facts, &factErr)
	where := p.Where()
	if doc != "" {
		where = refusals.Refusal{Kind: p.Kind.Kind, Name: doc, Path: strings.Join(p.Path, ".")}.Where()
	}
	out, _, err := p.check.Eval(vars)
	if factErr != nil {
		return nil, factErr
	}
	if err != nil {
		return nil, fmt.Errorf("cannot check: %s: %s", where, cannotEvaluate(err))
	}
	ok, isBool := out.(types.Bool)
	if !isBool {
		return nil, fmt.Errorf("cannot check: %s: a rule yields %s, not a bool", where, out.Type().TypeName())
	}
	if ok {
		return nil, nil
	}
	f := Failure{Refusal: refusals.Refusal{Kind: p.Kind.Kind, Message: p.Rule.Message}, Takes: p.Takes}
	if doc != "" {
		f.Name, f.Path = doc, strings.Join(p.Path, ".")
	}
	if p.message != nil {
		msg, _, err := p.message.Eval(vars)
		if factErr != nil {
			return nil, factErr
		}
		if s, isString := msg.(types.String); err == nil && isString {
			f.Message = refusals.Printable(string(s))
		} else if f.Message == "" {
			f.Message = "the config does not meet a rule of " + p.Where() + ", whose messageExpression fails"
			if why := messageError(msg, err); why != "" {
				f.Message += ": " + why
			}
		}
	}
	if f.Message == "" {
		f.Message = "the config does not meet a rule of " + p.Where()
	}
	return []Failure{f}, nil
}

// messageError is what a failure says of a messageExpression that yields msg and err: the kind
// of error, as evalError says it, or the type it yields instead of a string.
func messageError(msg ref.Val, err error) string {
	if err != nil {
		return evalError(err)
	}
	if msg == nil {
		return ""
	}
	return fmt.Sprintf("it yields %s, not a string", msg.Type().TypeName())
}

// ConfigOf is a config set as rules see it and command facts read it: objects by apiVersion, kind
// and name, with their secret values hidden.
func ConfigOf(docs config.Redacted) map[string]any {
	return configMap(docs.Documents())
}

func configMap(docs []config.Document) map[string]any {
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
		names[d.Name] = celValue(d.Object)
	}
	return out
}

// documentsOf is the documents of the program's kind by name.
func documentsOf(docs []config.Document, p *Program) map[string]any {
	out := map[string]any{}
	for _, d := range kindDocuments(docs, p) {
		out[d.Name] = celValue(d.Object)
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

// Refusal says what the failures of a config's rules take, the most a change takes first: a
// refusal, whose message says what to do instead, a reinstall of the node, or chalkctl storage
// reset. Each rule's message names the change and its remedy.
func Refusal(failures []Failure, node string) string {
	takes := func(t schema.Takes) bool {
		return slices.ContainsFunc(failures, func(f Failure) bool { return f.Takes == t })
	}
	head := "the config takes chalkctl storage reset"
	switch {
	case takes(schema.Refuse):
		head = "the config is refused"
	case takes(schema.Reinstall):
		head = "the config takes a reinstall of " + node
	}
	msgs := make([]string, len(failures))
	for i, f := range failures {
		msgs[i] = f.String()
	}
	return head + ": " + strings.Join(msgs, "; ")
}

// maxErrorText bounds the error a refusal shows of a fact that cannot be loaded.
const maxErrorText = 512

// evalErrors are the errors of a rule that cannot be evaluated a refusal names, by how CEL's
// error starts, and what the refusal says of each. CEL's errors show the value they are about,
// such as the key a map lacks or the index past a list's end, which can be one a rule read, so a
// refusal says only what kind of error it is, and of an error not listed here nothing.
var evalErrors = []struct{ prefix, says string }{
	{"no such key: ", "no such key"},
	{"index out of bounds: ", "index out of bounds"},
	{"index out of range: ", "index out of bounds"},
	{"no such overload", "no such overload"},
	{"unknown time zone ", "unknown time zone"},
	{"operation cancelled: actual cost limit exceeded", "cost limit exceeded"},
	{"integer overflow", "overflow"},
	{"unsigned integer overflow", "overflow"},
	{"duration overflow", "overflow"},
	{"timestamp overflow", "overflow"},
	{"division by zero", "division by zero"},
	{"modulus by zero", "division by zero"},
	{"type conversion error", "conversion error"},
	{"invalid RFC 3339 timestamp", "conversion error"},
	{"a cidr does not convert", "conversion error"},
	{"an ip does not convert", "conversion error"},
}

// invalidInput is the error of an extension function given an argument it does not take, as
// join given a list that holds something other than strings: "join: invalid input: <value>".
var invalidInput = regexp.MustCompile(`^[A-Za-z.]+: invalid input: `)

// libraryErrors are the errors of chalkos's functions, which hold no value.
var libraryErrors = []string{errNotCIDR, errNotIP, errNotSize, errNegativeIndex, errPastRange, errNotPortRange}

// evalError is what a refusal says of err, the error of a rule or a messageExpression that cannot
// be evaluated: the kind of error, or nothing for one evalErrors does not know.
func evalError(err error) string {
	text := err.Error()
	if slices.Contains(libraryErrors, text) {
		return text
	}
	for _, e := range evalErrors {
		if strings.HasPrefix(text, e.prefix) {
			return e.says
		}
	}
	if invalidInput.MatchString(text) {
		return "invalid input to a function"
	}
	return ""
}

// cannotEvaluate is the error of a rule that cannot be evaluated, with what evalError says of
// err.
func cannotEvaluate(err error) string {
	if says := evalError(err); says != "" {
		return "a rule cannot be evaluated: " + says
	}
	return "a rule cannot be evaluated"
}

// bounded is s cut to maxErrorText bytes, at a character's start.
func bounded(s string) string {
	if len(s) <= maxErrorText {
		return s
	}
	cut := maxErrorText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
