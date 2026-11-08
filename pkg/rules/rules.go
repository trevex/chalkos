package rules

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/checker"
	celast "cel.dev/cel-go/common/ast"

	"github.com/trevex/chalkos/pkg/schema"
)

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
	schema.Rule
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

// Compile compiles the rules every schema of s declares: a kind's own, then those of its fields
// in the order schema.WalkSchema reaches them, which is by path. It reports every rule that does
// not compile, each naming the kind or field that declares it. A rule binds self to the value at
// its path, which a list item or a map value has none of, so rules below a list's items or a map's
// values are refused: they are declared on the list or the map instead. Without s it compiles
// none.
func Compile(s *schema.Schemas) (*Set, error) {
	set := &Set{}
	if s == nil {
		return set, nil
	}
	var errs []error
	// Kind names are unique within an image, so document() names a kind by its name alone.
	apiVersions := map[string]string{}
	for _, k := range s.Kinds() {
		apiVersions[k.Kind] = k.APIVersion
	}
	add := func(k schema.Kind, path []string, rules []schema.Rule) {
		for _, r := range rules {
			p, err := compile(apiVersions, k, path, r)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			set.Programs = append(set.Programs, p)
		}
	}
	for _, k := range s.Kinds() {
		s.WalkSchema(k.APIVersion, k.Kind, func(n schema.Node) bool {
			a := n.Annotations()
			add(k, nil, a.KindRules)
			switch {
			case len(a.Rules) == 0:
			case len(n.Field) == 0:
				errs = append(errs, fmt.Errorf("%s: x-chalkos-rules at the schema's root; a kind declares its own rules in x-chalkos-kind", k.Kind))
			case slices.Contains(n.Field, schema.ItemStep) || slices.Contains(n.Field, schema.ValueStep):
				errs = append(errs, fmt.Errorf("%s %s: a rule below a list item or a map value has no path to bind self to; declare it on the list or the map", k.Kind, n.Path))
			default:
				add(k, slices.Clone(n.Field), a.Rules)
			}
			return true
		})
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return set, nil
}

// compile compiles a rule of the kind k, of its field at path or, with a nil path, of the kind.
// Every rule sees config, oldConfig and node, and document(kind, name) over config; a kind's rules
// documents and oldDocuments, a field's self and oldSelf; and each fact it names as facts.<name>.
func compile(apiVersions map[string]string, k schema.Kind, path []string, r schema.Rule) (*Program, error) {
	p := &Program{Rule: r, Kind: k, Path: path}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: rule %q: %s", p.Where(), r.Rule, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(r.Rule) == "" {
		return nil, fail("the rule is empty")
	}
	vars := []string{"config", "oldConfig", "node"}
	if path == nil {
		vars = append(vars, "documents", "oldDocuments")
	} else {
		vars = append(vars, "self", "oldSelf")
	}
	base, err := newEnv(vars...)
	if err == nil {
		base, err = base.Extend(documents(apiVersions))
	}
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
		names, err := factNames(parsed, "facts")
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

// factNames returns the facts a parsed expression reads, each as <ident>.<name> for one of
// idents, such as facts. Facts are read by name alone, so a node loads exactly the facts its
// rules name; any other use of facts, such as facts["share"], has() or facts itself, is refused.
func factNames(a *cel.Ast, idents ...string) ([]string, error) {
	var names []string
	selected := map[int64]bool{}
	var bad error
	celast.PreOrderVisit(a.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.SelectKind {
			return
		}
		sel := e.AsSelect()
		op := sel.Operand()
		if op.Kind() != celast.IdentKind || !slices.Contains(idents, op.AsIdent()) {
			return
		}
		if sel.IsTestOnly() {
			bad = fmt.Errorf("has() does not apply to %s: a fact the node cannot find reads as null", op.AsIdent())
		}
		selected[op.ID()] = true
		if !slices.Contains(names, sel.FieldName()) {
			names = append(names, sel.FieldName())
		}
	}))
	celast.PreOrderVisit(a.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() == celast.IdentKind && slices.Contains(idents, e.AsIdent()) && !selected[e.ID()] && bad == nil {
			bad = fmt.Errorf("%s are read by name, as %s.<name>", e.AsIdent(), e.AsIdent())
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

// Of is the set's rules of the kinds of apiVersion, as for checking a config's storage alone.
func (s *Set) Of(apiVersion string) *Set {
	out := &Set{}
	for _, p := range s.Programs {
		if p.Kind.APIVersion == apiVersion {
			out.Programs = append(out.Programs, p)
		}
	}
	return out
}
