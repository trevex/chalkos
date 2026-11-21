package rules

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/cost"
	"cel.dev/cel-go/common/types"

	"github.com/trevex/chalkos/pkg/schema"
)

// Cost limits. A rule's estimated cost assumes every list, map and string holds sizeHint
// elements, so two nested loops over a kind's documents fit and three exceed the limit. The
// runtime limit stops a rule on the documents it meets.
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
	// checkFields and messageFields are the fields of the kind's schema the rule and its message
	// read, as kindTypes.fieldsRead gives them, which an error of their evaluation may name.
	checkFields, messageFields []string
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

// Compile compiles the rules every schema of s declares: a kind's own, then its fields' by path.
// A rule binds self to the value at its path, which a list item or map value lacks, so a rule
// below one is refused and belongs on the list or the map. A field's rule applies outside spec
// too. It reports every rule that does not compile, each naming the kind or field that declares
// it. Without s it compiles none.
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
	for _, k := range s.Kinds() {
		kt, err := newKindTypes(k.Kind)
		if err != nil {
			return nil, err
		}
		add := func(path []string, self *types.Type, rules []schema.Rule) {
			for _, r := range rules {
				p, err := compile(apiVersions, kt, k, path, self, r)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				set.Programs = append(set.Programs, p)
			}
		}
		s.WalkSchema(k.APIVersion, k.Kind, func(n schema.Node) bool {
			a := n.Annotations()
			if len(n.Field) == 0 && len(a.KindRules) > 0 {
				add(nil, kt.of(n.Schema, nil), a.KindRules)
			}
			switch {
			case len(a.Rules) == 0:
			case len(n.Field) == 0:
				errs = append(errs, fmt.Errorf("%s: x-chalkos-rules at the schema's root; a kind declares its own rules in x-chalkos-kind", k.Kind))
			case slices.Contains(n.Field, schema.ItemStep) || slices.Contains(n.Field, schema.ValueStep):
				errs = append(errs, fmt.Errorf("%s %s: a rule below a list item or a map value has no path to bind self to; declare it on the list or the map", k.Kind, n.Path))
			default:
				add(slices.Clone(n.Field), kt.of(n.Schema, n.Field), a.Rules)
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
// documents and oldDocuments, maps of names to documents of type self; a field's self and oldSelf,
// of type self; and each fact it names as facts.<name>. kt holds the types of k's schema.
func compile(apiVersions map[string]string, kt *kindTypes, k schema.Kind, path []string, self *types.Type, r schema.Rule) (*Program, error) {
	p := &Program{Rule: r, Kind: k, Path: path}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: rule %q: %s", p.Where(), r.Rule, fmt.Sprintf(format, args...))
	}
	opts := []cel.EnvOption{documents(apiVersions), cel.CustomTypeProvider(kt), kt.indexes()}
	if path == nil {
		docs := cel.MapType(cel.StringType, self)
		opts = append(opts, cel.Variable("documents", docs), cel.Variable("oldDocuments", docs))
	} else {
		opts = append(opts, cel.Variable("self", self), cel.Variable("oldSelf", self))
	}
	base, err := newEnv("config", "oldConfig")
	if err == nil {
		base, err = base.Extend(opts...)
	}
	if err != nil {
		return nil, err
	}
	e, parsed, facts, err := parseRule(base, []string{"facts"}, r.Rule, r.MessageExpression)
	if err != nil {
		return nil, fail("%v", err)
	}
	check, message := parsed[0], parsed[1]
	p.Facts = facts
	if p.check, p.checkFields, err = program(e, check, cel.BoolType); err != nil {
		return nil, fail("%v", err)
	}
	if message != nil {
		if p.message, p.messageFields, err = program(e, message, cel.StringType); err != nil {
			return nil, fail("messageExpression: %v", err)
		}
	}
	p.Transition = path != nil && reads(check, "oldSelf")
	return p, nil
}

// parseRule parses rule and the further expressions srcs in base, and extends base with each fact
// they read as <ident>.<name> for every one of idents. The rule must not be empty; an empty further
// expression has a nil AST; the facts' names come back sorted.
func parseRule(base *cel.Env, idents []string, rule string, srcs ...string) (*cel.Env, []*cel.Ast, []string, error) {
	if strings.TrimSpace(rule) == "" {
		return nil, nil, nil, errors.New("the rule is empty")
	}
	parsed := make([]*cel.Ast, 1+len(srcs))
	var facts []string
	for i, src := range append([]string{rule}, srcs...) {
		if src == "" {
			continue
		}
		a, iss := base.Parse(src)
		if iss.Err() != nil {
			return nil, nil, nil, iss.Err()
		}
		names, err := factNames(a, idents...)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, n := range names {
			if !slices.Contains(facts, n) {
				facts = append(facts, n)
			}
		}
		parsed[i] = a
	}
	slices.Sort(facts)
	opts := make([]cel.EnvOption, 0, len(idents)*len(facts))
	for _, n := range facts {
		for _, ident := range idents {
			opts = append(opts, cel.Variable(ident+"."+n, cel.DynType))
		}
	}
	e, err := base.Extend(opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	return e, parsed, facts, nil
}

// program checks a parsed expression, which must yield want or dyn, and limits its cost. In the
// environment of a kind's rule it returns the fields of the kind's schema the expression reads,
// as kindTypes.fieldsRead does.
func program(e *cel.Env, parsed *cel.Ast, want *cel.Type) (cel.Program, []string, error) {
	checked, iss := e.Check(parsed)
	if iss.Err() != nil {
		return nil, nil, iss.Err()
	}
	var fields []string
	if kt, ok := e.CELTypeProvider().(*kindTypes); ok {
		var err error
		if fields, err = kt.fieldsRead(checked); err != nil {
			return nil, nil, err
		}
	}
	if out := checked.OutputType(); !out.IsExactType(want) && !out.IsExactType(cel.DynType) {
		return nil, nil, fmt.Errorf("yields %s, not %s", out, want)
	}
	est, err := e.EstimateCost(checked, estimator{})
	if err != nil {
		return nil, nil, err
	}
	if est.Max > maxEstimatedCost {
		return nil, nil, fmt.Errorf("costs up to %d, more than the limit of %d, with %d elements in each list, map and string", est.Max, maxEstimatedCost, sizeHint)
	}
	prg, err := e.Program(checked, cel.CostLimit(runtimeCostLimit))
	return prg, fields, err
}

// estimator assumes sizeHint elements wherever CEL cannot tell a size, and the same for the keys
// and elements of a list or map down to sizeDepth levels, the deepest nesting the kinds' documents
// reach; CEL counts a container whose element size it cannot tell as unbounded.
type estimator struct{}

const sizeDepth = 8

func nestedSize(depth int) *cost.SizeEstimate {
	s := &cost.SizeEstimate{Min: 0, Max: sizeHint}
	if depth > 0 {
		s.Key = nestedSize(depth - 1)
		s.Elem = nestedSize(depth - 1)
	}
	return s
}

func (estimator) EstimateSize(cost.AstNode) *cost.SizeEstimate {
	// A new estimate each time: the checker keeps and merges the estimates it is given.
	return nestedSize(sizeDepth)
}

func (estimator) EstimateCallCost(string, string, *cost.AstNode, []cost.AstNode) *cost.CallEstimate {
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
