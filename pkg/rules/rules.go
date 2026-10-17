package rules

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/checker"
	celast "cel.dev/cel-go/common/ast"

	"github.com/trevex/chalkos/pkg/schema"
)

// Takes is what a change a rule refuses needs instead.
type Takes string

const (
	// Refuse: the change cannot be made; the message says what to do instead.
	Refuse Takes = "refuse"
	// Reset: chalkctl storage reset carries the change out.
	Reset Takes = "reset"
	// Reinstall: only a reinstall of the node carries the change out.
	Reinstall Takes = "reinstall"
)

// Rule is a rule as a schema declares it: in a field's x-chalkos-rules, or in the rules of its
// kind's x-chalkos-kind.
type Rule struct {
	// Rule is a CEL expression that is true for every change the image accepts.
	Rule string `json:"rule"`
	// Message says why a change is refused and what to do instead; MessageExpression is CEL that
	// yields it, and the message is used when the expression fails.
	Message           string `json:"message,omitempty"`
	MessageExpression string `json:"messageExpression,omitempty"`
	// Takes defaults to Refuse.
	Takes Takes `json:"takes,omitempty"`
}

// Cost limits. A rule's estimated cost assumes every list, map and string holds sizeHint
// elements or characters, so a loop over the documents of a kind costs about sizeHint times its
// body, and two nested loops about sizeHint²; three nested loops exceed the limit. The runtime
// limit stops a rule on the documents it actually meets.
const (
	sizeHint         = 256
	maxEstimatedCost = 10_000_000
	runtimeCostLimit = 1_000_000
)

// Program is a rule compiled for the kind or field that declares it.
type Program struct {
	Rule
	// Kind is the kind that declares the rule.
	Kind schema.Kind
	// Path is the field's path below the document, such as spec.ipFamilies; nil for a rule of
	// the kind.
	Path []string
	// Facts are the names of the facts the rule and its message read, sorted.
	Facts []string
	// Transition tells whether a field's rule reads oldSelf, so it runs only where the running
	// config holds the field.
	Transition bool

	check, message cel.Program
}

// Where names the kind or field that declares the rule.
func (p *Program) Where() string {
	if p.Path == nil {
		return p.Kind.Kind
	}
	return p.Kind.Kind + " " + strings.Join(p.Path, ".")
}

// Set is the compiled rules of an image's schemas, in the order of the kinds, each kind's own
// rules first and then its fields' by path.
type Set struct {
	Programs []*Program
}

// Compile compiles the rules every schema of s declares. It reports every rule that does not
// compile, each naming the kind or field that declares it.
func Compile(s *schema.Schemas) (*Set, error) {
	set := &Set{}
	var errs []error
	for _, k := range s.Kinds() {
		raw, _ := s.Raw(k.APIVersion, k.Kind)
		doc, _ := raw.(map[string]any)
		xk, _ := doc["x-chalkos-kind"].(map[string]any)
		if declared, ok := xk["rules"]; ok {
			rules, err := decodeRules(declared)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: rules: %w", k.Kind, err))
			}
			for _, r := range rules {
				p, err := compile(k, nil, r)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				set.Programs = append(set.Programs, p)
			}
		}
		props, _ := doc["properties"].(map[string]any)
		spec, _ := props["spec"].(map[string]any)
		fields, err := fieldRules(spec, []string{"spec"}, false)
		errs = append(errs, err...)
		for _, f := range fields {
			for _, r := range f.rules {
				p, err := compile(k, f.path, r)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				set.Programs = append(set.Programs, p)
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return set, nil
}

type field struct {
	path  []string
	rules []Rule
}

// fieldRules finds the x-chalkos-rules of a property schema and of the properties below it, by
// path: through the properties of objects and the branches of anyOf, as of a nullable group. A
// rule binds self to the value at its path, which a list item or a map value has none of, so
// rules below items or additionalProperties are refused: they are declared on the list or the
// map instead.
func fieldRules(sch map[string]any, path []string, inItems bool) ([]field, []error) {
	if sch == nil {
		return nil, nil
	}
	var out []field
	var errs []error
	where := strings.Join(path, ".")
	if declared, ok := sch["x-chalkos-rules"]; ok {
		if inItems {
			errs = append(errs, fmt.Errorf("%s: a rule below a list item or a map value has no path to bind self to; declare it on the list or the map", where))
		} else if rules, err := decodeRules(declared); err != nil {
			errs = append(errs, fmt.Errorf("%s: x-chalkos-rules: %w", where, err))
		} else {
			out = append(out, field{path: slices.Clone(path), rules: rules})
		}
	}
	props, _ := sch["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		sub, _ := props[name].(map[string]any)
		f, e := fieldRules(sub, append(slices.Clone(path), name), inItems)
		out, errs = append(out, f...), append(errs, e...)
	}
	if branches, ok := sch["anyOf"].([]any); ok {
		for _, b := range branches {
			sub, _ := b.(map[string]any)
			f, e := fieldRules(sub, path, inItems)
			out, errs = append(out, f...), append(errs, e...)
		}
	}
	for _, key := range []string{"items", "additionalProperties"} {
		if sub, ok := sch[key].(map[string]any); ok {
			_, e := fieldRules(sub, append(slices.Clone(path), "*"), true)
			errs = append(errs, e...)
		}
	}
	return out, errs
}

// decodeRules reads a schema's list of rules, refusing keys a rule does not have.
func decodeRules(v any) ([]Rule, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var rules []Rule
	if err := dec.Decode(&rules); err != nil {
		return nil, err
	}
	for i := range rules {
		switch rules[i].Takes {
		case "":
			rules[i].Takes = Refuse
		case Refuse, Reset, Reinstall:
		default:
			return nil, fmt.Errorf("takes %q is none of refuse, reset and reinstall", rules[i].Takes)
		}
	}
	return rules, nil
}

// compile compiles a rule of the kind k, of its field at path or, with a nil path, of the kind.
func compile(k schema.Kind, path []string, r Rule) (*Program, error) {
	p := &Program{Rule: r, Kind: k, Path: path}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: rule %q: %s", p.Where(), r.Rule, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(r.Rule) == "" {
		return nil, fail("the rule is empty")
	}
	vars := []string{"config", "oldConfig"}
	if path == nil {
		vars = append(vars, "documents", "oldDocuments")
	} else {
		vars = append(vars, "self", "oldSelf")
	}
	base, err := newEnv(vars...)
	if err != nil {
		return nil, err
	}
	facts := map[string]bool{}
	var check, message *cel.Ast
	for _, src := range []struct {
		text string
		ast  **cel.Ast
	}{{r.Rule, &check}, {r.MessageExpression, &message}} {
		if src.text == "" {
			continue
		}
		parsed, iss := base.Parse(src.text)
		if iss.Err() != nil {
			return nil, fail("%v", iss.Err())
		}
		names, err := factNames(parsed)
		if err != nil {
			return nil, fail("%v", err)
		}
		for _, n := range names {
			facts[n] = true
		}
		*src.ast = parsed
	}
	for n := range facts {
		p.Facts = append(p.Facts, n)
	}
	slices.Sort(p.Facts)
	opts := make([]cel.EnvOption, 0, len(p.Facts))
	for _, n := range p.Facts {
		opts = append(opts, cel.Variable("facts."+n, cel.DynType))
	}
	e, err := base.Extend(opts...)
	if err != nil {
		return nil, err
	}
	if p.check, err = program(e, check, cel.BoolType); err != nil {
		return nil, fail("%v", err)
	}
	if message != nil {
		if p.message, err = program(e, message, cel.StringType); err != nil {
			return nil, fail("messageExpression: %v", err)
		}
	}
	p.Transition = path != nil && reads(check, "oldSelf")
	return p, nil
}

// program checks a parsed expression, which must yield want or dyn, and limits its cost.
func program(e *cel.Env, parsed *cel.Ast, want *cel.Type) (cel.Program, error) {
	checked, iss := e.Check(parsed)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	if out := checked.OutputType(); !out.IsExactType(want) && !out.IsExactType(cel.DynType) {
		return nil, fmt.Errorf("yields %s, not %s", out, want)
	}
	est, err := e.EstimateCost(checked, estimator{})
	if err != nil {
		return nil, err
	}
	if est.Max > maxEstimatedCost {
		return nil, fmt.Errorf("costs up to %d, more than the limit of %d, with %d elements in each list, map and string", est.Max, maxEstimatedCost, sizeHint)
	}
	return e.Program(checked, cel.CostLimit(runtimeCostLimit))
}

// estimator assumes sizeHint elements wherever CEL cannot tell a size.
type estimator struct{}

func (estimator) EstimateSize(checker.AstNode) *checker.SizeEstimate {
	return &checker.SizeEstimate{Min: 0, Max: sizeHint}
}

func (estimator) EstimateCallCost(string, string, *checker.AstNode, []checker.AstNode) *checker.CallEstimate {
	return nil
}

// factNames returns the facts a parsed expression reads, each as facts.<name>. Facts are read by
// name alone, so a node loads exactly the facts its rules name; any other use of facts, such as
// facts["share"], has() or facts itself, is refused.
func factNames(a *cel.Ast) ([]string, error) {
	var names []string
	selected := map[int64]bool{}
	var bad error
	celast.PreOrderVisit(a.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.SelectKind {
			return
		}
		sel := e.AsSelect()
		op := sel.Operand()
		if op.Kind() != celast.IdentKind || op.AsIdent() != "facts" {
			return
		}
		if sel.IsTestOnly() {
			bad = errors.New("has() does not apply to facts: a fact the node cannot find reads as null")
		}
		selected[op.ID()] = true
		if !slices.Contains(names, sel.FieldName()) {
			names = append(names, sel.FieldName())
		}
	}))
	celast.PreOrderVisit(a.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() == celast.IdentKind && e.AsIdent() == "facts" && !selected[e.ID()] && bad == nil {
			bad = errors.New("facts are read by name, as facts.<name>")
		}
	}))
	return names, bad
}

// reads tells whether a checked expression reads the variable name.
func reads(a *cel.Ast, name string) bool {
	found := false
	celast.PreOrderVisit(a.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() == celast.IdentKind && e.AsIdent() == name {
			found = true
		}
	}))
	return found
}
