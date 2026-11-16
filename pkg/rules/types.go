package rules

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/trevex/chalkos/pkg/schema"
)

// kindTypes are the CEL types of a kind's schema, as rules read its documents. An object that
// allows no fields beyond its properties is an object type, so a rule that reads a field the
// schema does not declare fails its check; an object whose other fields share a schema is a map;
// a list is a list of its items' type. Strings, bools and integers are themselves. Everything
// else is dyn: a number, which celValue reads as an int where it is whole; an object that allows
// any field, as an embedded Kubernetes object does; and an anyOf, unless it is an object type or
// null, as nullOr writes a submodule. The values stay JSON maps and lists, which CEL selects
// from by key; the types serve the check alone.
type kindTypes struct {
	types.Provider
	kind string
	// named are the object types by the schema, $ref followed, that declares them.
	named map[*jsonschema.Schema]*types.Type
	// fields are the fields of each object type by its name.
	fields map[string]map[string]*types.Type
}

func newKindTypes(kind string) (*kindTypes, error) {
	reg, err := types.NewRegistry()
	if err != nil {
		return nil, err
	}
	return &kindTypes{Provider: reg, kind: kind, named: map[*jsonschema.Schema]*types.Type{}, fields: map[string]map[string]*types.Type{}}, nil
}

// of is the type of a value of s, the schema at field. An object type is named by the kind and
// the field it is first met at, which messages of the check show.
func (k *kindTypes) of(s *jsonschema.Schema, field []string) *types.Type {
	s = schema.Resolve(s)
	if t, ok := k.named[s]; ok {
		return t
	}
	if len(s.AnyOf) > 0 {
		return k.anyOf(s.AnyOf, field)
	}
	var names []string
	if s.Types != nil {
		names = s.Types.ToStrings()
	} else if s.Enum != nil && !slices.ContainsFunc(s.Enum.Values, func(v any) bool { _, ok := v.(string); return !ok }) {
		names = []string{"string"}
	}
	if len(names) != 1 {
		return types.DynType
	}
	switch names[0] {
	case "string":
		return types.StringType
	case "boolean":
		return types.BoolType
	case "integer":
		return types.IntType
	case "array":
		if s.Items2020 == nil {
			return types.NewListType(types.DynType)
		}
		return types.NewListType(k.of(s.Items2020, append(slices.Clip(field), schema.ItemStep)))
	case "object":
		switch extra := s.AdditionalProperties.(type) {
		case *jsonschema.Schema:
			if len(s.Properties) == 0 {
				return types.NewMapType(types.StringType, k.of(extra, append(slices.Clip(field), schema.ValueStep)))
			}
		case bool:
			if !extra {
				return k.object(s, field)
			}
		}
	}
	return types.DynType
}

// anyOf is the type of a value of one of the branches: an object type that may be null, which
// CEL's object types are, or else dyn.
func (k *kindTypes) anyOf(branches []*jsonschema.Schema, field []string) *types.Type {
	var t *types.Type
	for _, b := range branches {
		r := schema.Resolve(b)
		if r.Types != nil && slices.Equal(r.Types.ToStrings(), []string{"null"}) {
			continue
		}
		if t != nil {
			return types.DynType
		}
		t = k.of(r, field)
	}
	if t == nil || t.Kind() != types.StructKind {
		return types.DynType
	}
	return t
}

// object is the object type of s, its fields typed by their schemas.
func (k *kindTypes) object(s *jsonschema.Schema, field []string) *types.Type {
	name := k.kind
	if len(field) > 0 {
		name += "." + schema.FieldPath(field)
	}
	t := types.NewObjectType(name)
	k.named[s] = t
	fields := map[string]*types.Type{}
	k.fields[name] = fields
	for _, prop := range slices.Sorted(maps.Keys(s.Properties)) {
		fields[prop] = k.of(s.Properties[prop], append(slices.Clip(field), prop))
	}
	return t
}

// indexes lets rules index an object type by a string, as o["in"] reads a field whose name CEL
// reserves and o[family] one a value names. The index yields dyn; fieldsRead checks a literal
// name. An overload on a type no value has makes dyn indexed by a string read as dyn too: with
// the map's overload alone, its value type stays open, and beside null, as in
// `n in m ? m[n] : null`, the check takes it for null.
func (k *kindTypes) indexes() cel.EnvOption {
	overloads := []cel.FunctionOpt{cel.Overload("chalkos_index_none", []*cel.Type{cel.OpaqueType("chalkos_none"), cel.StringType}, cel.DynType)}
	for _, name := range slices.Sorted(maps.Keys(k.fields)) {
		overloads = append(overloads, cel.Overload("chalkos_index_"+name, []*cel.Type{types.NewObjectType(name), cel.StringType}, cel.DynType))
	}
	return cel.Function(operators.Index, overloads...)
}

// fieldsRead returns the fields of object types a checked expression reads, by a select or a
// string literal index, sorted: names from the schema, which an error may show. It returns none
// for an expression that looks a key up by a value, whose missing key a select's cannot be told
// from, other than a name of documents or oldDocuments over which it loops. It refuses an index
// by a string literal the object type declares no field of, as the check refuses such a select.
func (k *kindTypes) fieldsRead(checked *cel.Ast) ([]string, error) {
	a := checked.NativeRep()
	var read, errs []string
	byValue := false
	// names are the loop variables over documents and oldDocuments, by the variable looped over.
	names := map[string][]string{}
	celast.PreOrderVisit(a.Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.ComprehensionKind {
			return
		}
		c := e.AsComprehension()
		if r := c.IterRange(); r.Kind() == celast.IdentKind {
			names[r.AsIdent()] = append(names[r.AsIdent()], c.IterVar())
		}
	}))
	isName := func(m, key celast.Expr) bool {
		return m.Kind() == celast.IdentKind && (m.AsIdent() == "documents" || m.AsIdent() == "oldDocuments") &&
			key.Kind() == celast.IdentKind && slices.Contains(names[m.AsIdent()], key.AsIdent())
	}
	celast.PreOrderVisit(a.Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		var operand celast.Expr
		var field string
		switch {
		case e.Kind() == celast.SelectKind && !e.AsSelect().IsTestOnly():
			operand, field = e.AsSelect().Operand(), e.AsSelect().FieldName()
		case e.Kind() == celast.CallKind && e.AsCall().FunctionName() == operators.Index:
			args := e.AsCall().Args()
			var isLiteral bool
			if field, isLiteral = stringLiteral(args[1]); !isLiteral {
				if a.GetType(args[0].ID()).Kind() != types.ListKind && !isName(args[0], args[1]) {
					byValue = true
				}
				return
			}
			operand = args[0]
		default:
			return
		}
		fields, ok := k.fields[a.GetType(operand.ID()).TypeName()]
		if !ok {
			return
		}
		if _, declared := fields[field]; !declared {
			loc := a.SourceInfo().GetStartLocation(e.ID())
			errs = append(errs, fmt.Sprintf("ERROR: <input>:%d:%d: undefined field '%s'", loc.Line(), loc.Column()+1, field))
		} else if !slices.Contains(read, field) {
			read = append(read, field)
		}
	}))
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "\n"))
	}
	if byValue {
		return nil, nil
	}
	slices.Sort(read)
	return read, nil
}

func (k *kindTypes) FindStructType(name string) (*types.Type, bool) {
	if _, ok := k.fields[name]; ok {
		return types.NewTypeTypeWithParam(types.NewObjectType(name)), true
	}
	return k.Provider.FindStructType(name)
}

func (k *kindTypes) FindStructFieldNames(name string) ([]string, bool) {
	if fields, ok := k.fields[name]; ok {
		names := make([]string, 0, len(fields))
		for f := range fields {
			names = append(names, f)
		}
		slices.Sort(names)
		return names, true
	}
	return k.Provider.FindStructFieldNames(name)
}

// FindStructFieldType gives the type of a declared field alone, without the means to read it, so
// evaluation selects it from the JSON map by key.
func (k *kindTypes) FindStructFieldType(name, field string) (*types.FieldType, bool) {
	if fields, ok := k.fields[name]; ok {
		t, ok := fields[field]
		if !ok {
			return nil, false
		}
		return &types.FieldType{Type: t}, true
	}
	return k.Provider.FindStructFieldType(name, field)
}
