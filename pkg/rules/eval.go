package rules

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/internal/strs"
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
	// which its node certificate carries. Rules read the static addresses of Documents as
	// node.addresses.
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
	// Reason is the reason the rule names, which the refusal carries to the client; empty for none.
	Reason string
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

// Evaluate returns the rules of the set that the config in does not meet, in the set's order. A
// rule that cannot be evaluated or a fact that cannot be loaded is an error, refusing the delivery.
func (s *Set) Evaluate(in Input) ([]Failure, error) {
	next, old := in.Documents.Documents(), in.Old.Documents()
	cfg, oldCfg := ConfigOf(in.Documents), ConfigOf(in.Old)
	node := nodeOf(in.Node, next)
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

// nodeOf is the node as rules read it: node.name, its name, and node.addresses, the static
// addresses of docs that are IP addresses, as its node certificate names them, so ip() takes
// every one.
func nodeOf(name string, docs config.Documents) map[string]any {
	addresses := []any{}
	for _, a := range config.StaticAddresses(docs) {
		if addr, err := netip.ParseAddr(a); err == nil && addr.Zone() == "" {
			addresses = append(addresses, a)
		}
	}
	return map[string]any{"name": name, "addresses": addresses}
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

// evalBool evaluates prg, the check of a rule, with vars. A fact the rule reads that could not be
// loaded, which bindFacts keeps in factErr, is the error then, whatever the rule yields without
// it; else the error says why the rule yields no bool, naming the one of fields it lacks.
func evalBool(prg cel.Program, vars map[string]any, fields []string, factErr *error) (bool, error) {
	out, _, err := prg.Eval(vars)
	if *factErr != nil {
		return false, *factErr
	}
	if err != nil {
		return false, errors.New(cannotEvaluate(err, fields))
	}
	ok, isBool := out.(types.Bool)
	if !isBool {
		return false, fmt.Errorf("a rule yields %s, not a bool", out.Type().TypeName())
	}
	return bool(ok), nil
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
	ok, err := evalBool(p.check, vars, p.checkFields, &factErr)
	// A fact that could not be loaded is returned as it is, outside the cannot-check wrap.
	if factErr != nil {
		return nil, factErr
	}
	if err != nil {
		return nil, fmt.Errorf("cannot check: %s: %w", where, err)
	}
	if ok {
		return nil, nil
	}
	f := Failure{Refusal: refusals.Refusal{Kind: p.Kind.Kind, Message: p.Rule.Message}, Takes: p.Takes, Reason: p.Reason}
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
			if why := messageError(msg, err, p.messageFields); why != "" {
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
// of error, or the type it yields instead of a string.
func messageError(msg ref.Val, err error, fields []string) string {
	if err != nil {
		return evalError(err, fields)
	}
	if msg == nil {
		return ""
	}
	return fmt.Sprintf("it yields %s, not a string", msg.Type().TypeName())
}

// FactInput is what command facts read of a check of next against old: both as rules see them,
// as config and oldConfig.
func FactInput(next, old config.Redacted) map[string]any {
	return map[string]any{"config": ConfigOf(next), "oldConfig": ConfigOf(old)}
}

// ConfigOf is a config set as rules see it and command facts read it: objects by apiVersion, kind
// and name, with their secret values hidden.
func ConfigOf(docs config.Redacted) map[string]any {
	out := map[string]any{}
	for _, d := range docs.Documents() {
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
func celValue(v any) any { return schema.MapJSON(v, celLeaf) }

// celLeaf converts a value celValue reaches that is neither an object nor an array.
func celLeaf(v any) any {
	switch v := v.(type) {
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
	return head + ": " + strs.Join(failures, "; ")
}

// maxErrorText bounds the error a refusal shows of a fact that cannot be loaded.
const maxErrorText = 512

// evalErrors maps how a CEL evaluation error starts to what a refusal says of it. CEL's errors
// show the value they are about, which can be one a rule read, so a refusal names only the kind
// of error, and nothing for one not listed.
var evalErrors = []struct{ prefix, says string }{
	{"no such key: ", "no such key"},
	{"index out of bounds: ", "index out of bounds"},
	{"index out of range: ", "index out of bounds"},
	{"no such overload", "no such overload"},
	{"unknown time zone ", "unknown time zone"},
	{"operation cancelled: actual cost limit exceeded", "the image's rule is too costly for this config"},
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
// be evaluated: the kind of error, or nothing for one evalErrors does not know. A missing key that
// is one of fields, the schema's fields the expression reads, is named: a field's name comes from
// the schema, not from a value.
func evalError(err error, fields []string) string {
	text := err.Error()
	if slices.Contains(libraryErrors, text) {
		return text
	}
	if key, ok := strings.CutPrefix(text, "no such key: "); ok && slices.Contains(fields, key) {
		return "no such key: " + key + ", a field the config leaves out; test it with has() first"
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
func cannotEvaluate(err error, fields []string) string {
	if says := evalError(err, fields); says != "" {
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
