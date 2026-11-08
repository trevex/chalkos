package schema

import (
	"maps"
	"regexp"
	"slices"
	"strconv"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The steps of a Field that are not a property's name.
const (
	// ItemStep stands for each item of a list.
	ItemStep = "[]"
	// ValueStep stands for each value of a map.
	ValueStep = "<name>"
)

// A Node is a node of a kind's schema that a walk reaches: a property, a list's items, a map's
// values or a branch of an anyOf, with $ref followed.
//
// Both walks follow the same keywords: properties, additionalProperties that is a schema, items
// and the branches of anyOf. A node with $ref stands for the schema it refers to, following a
// chain of $ref to its end: its Schema, and so what lies below it and its annotations, are the
// target's, and its Keywords are the target's with those beside each $ref laid over them, such as
// a field's own description. compile refuses an x-chalkos-* keyword beside $ref. Other
// applicators, such as allOf, oneOf and prefixItems, are not walked: the kinds' schemas use none.
type Node struct {
	// Schema is the node, with $ref followed.
	Schema *jsonschema.Schema
	// Field is the node's place in the schema, a step per level below the document: a property's
	// name, ItemStep for each item of a list, ValueStep for each value of a map. It is the same for
	// every value the node describes, and empty at the document's root. A branch of an anyOf has
	// the Field of the anyOf.
	Field []string
	// Path is, in Walk, the value's path below the document, such as spec.rules[2].to; in
	// WalkSchema the field's, as FieldPath writes it.
	Path string
	// Value is, in Walk, the value the node describes, nil where the document leaves it out.
	Value any

	// written is the node as the schema names it, $ref not followed.
	written *jsonschema.Schema
}

// Annotations are the node's annotations, as AnnotationsOf reads them.
func (n Node) Annotations() Annotations { return AnnotationsOf(n.Schema) }

// Keywords are the node's keywords, as KeywordsOf reads them.
func (n Node) Keywords() map[string]any { return KeywordsOf(n.written) }

// FieldPath writes a Field as a path: properties after dots, or in quoted brackets for a name
// that is not a plain identifier, [] for each item of a list and .<name> for each value of a map,
// as in spec.rules[].to and spec.nodes.<name>.address.
func FieldPath(field []string) string {
	path := ""
	for _, step := range field {
		switch step {
		case ItemStep:
			path += ItemStep
		case ValueStep:
			path += "." + ValueStep
		default:
			path = PropertyPath(path, step)
		}
	}
	return path
}

// plainKey is a field or map key a path writes after a dot; any other goes in quoted brackets.
var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// PropertyPath is path with the field or map key name appended.
func PropertyPath(path, name string) string {
	switch {
	case !plainKey.MatchString(name):
		return path + "[" + strconv.Quote(name) + "]"
	case path == "":
		return name
	}
	return path + "." + name
}

// Resolve follows a chain of $ref from s to the schema it ends at.
func Resolve(s *jsonschema.Schema) *jsonschema.Schema {
	for s.Ref != nil {
		s = s.Ref
	}
	return s
}

// Walk walks v, a document of the kind of apiVersion and kind or a value as one holds it,
// alongside the kind's schema from its root, and calls visit for every node it reaches, where
// visit returns whether to walk on below the node. It walks the fields of an object that v holds,
// in name order, by their properties or else by additionalProperties, and each item of a list by
// items; a key or item the schema says nothing about is left alone. At an anyOf it visits the
// node and then the one branch that describes v, with the same Path: the first whose type,
// $ref followed, is v's JSON type, or the first branch when none is. It walks nothing for a kind
// the schemas do not declare.
func (s *Schemas) Walk(apiVersion, kind string, v any, visit func(Node) bool) {
	if sch, ok := s.schemas[key(apiVersion, kind)]; ok {
		walkValue(Node{Schema: sch, Value: v}, visit)
	}
}

func walkValue(n Node, visit func(Node) bool) {
	n.written, n.Schema = n.Schema, Resolve(n.Schema)
	if !visit(n) {
		return
	}
	if len(n.Schema.AnyOf) > 0 {
		walkValue(Node{Schema: branchFor(n.Schema.AnyOf, n.Value), Field: n.Field, Path: n.Path, Value: n.Value}, visit)
		return
	}
	switch val := n.Value.(type) {
	case map[string]any:
		extra, _ := n.Schema.AdditionalProperties.(*jsonschema.Schema)
		for _, name := range slices.Sorted(maps.Keys(val)) {
			prop, step := n.Schema.Properties[name], name
			if prop == nil {
				prop, step = extra, ValueStep
			}
			if prop == nil {
				continue
			}
			walkValue(Node{Schema: prop, Field: append(slices.Clip(n.Field), step), Path: PropertyPath(n.Path, name), Value: val[name]}, visit)
		}
	case []any:
		if n.Schema.Items2020 == nil {
			return
		}
		for i, item := range val {
			walkValue(Node{Schema: n.Schema.Items2020, Field: append(slices.Clip(n.Field), ItemStep), Path: n.Path + "[" + strconv.Itoa(i) + "]", Value: item}, visit)
		}
	}
}

// WalkSchema walks the schema of the kind of apiVersion and kind from its root, without a value,
// and calls visit for every node it reaches, where visit returns whether to walk on below the
// node. Below a node it walks the properties in name order, then additionalProperties, then
// items, then every branch of an anyOf, in order and with the node's Field: a walk without a
// value cannot tell which branch a value would take. It does not visit a node whose schema is
// already above it on its path, as below a schema that refers to itself. It walks nothing for a
// kind the schemas do not declare.
func (s *Schemas) WalkSchema(apiVersion, kind string, visit func(Node) bool) {
	if sch, ok := s.schemas[key(apiVersion, kind)]; ok {
		walkSchema(Node{Schema: sch}, nil, visit)
	}
}

func walkSchema(n Node, on []*jsonschema.Schema, visit func(Node) bool) {
	n.written, n.Schema = n.Schema, Resolve(n.Schema)
	if slices.Contains(on, n.Schema) {
		return
	}
	n.Path = FieldPath(n.Field)
	if !visit(n) {
		return
	}
	on = append(on, n.Schema)
	below := func(sch *jsonschema.Schema, step string) {
		walkSchema(Node{Schema: sch, Field: append(slices.Clip(n.Field), step)}, slices.Clip(on), visit)
	}
	for _, name := range slices.Sorted(maps.Keys(n.Schema.Properties)) {
		below(n.Schema.Properties[name], name)
	}
	if extra, ok := n.Schema.AdditionalProperties.(*jsonschema.Schema); ok {
		below(extra, ValueStep)
	}
	if n.Schema.Items2020 != nil {
		below(n.Schema.Items2020, ItemStep)
	}
	for _, b := range n.Schema.AnyOf {
		walkSchema(Node{Schema: b, Field: n.Field}, slices.Clip(on), visit)
	}
}

// branchFor picks the anyOf branch that describes v: the first whose type, $ref followed, is v's
// JSON type, such as the object branch of a nullable object or of networkd's object-or-list
// sections, or the first branch when none is.
func branchFor(branches []*jsonschema.Schema, v any) *jsonschema.Schema {
	for _, b := range branches {
		r := b
		for r.Ref != nil && r.Types == nil {
			r = r.Ref
		}
		if r.Types == nil {
			continue
		}
		types := r.Types.ToStrings()
		for _, t := range jsonTypes(v) {
			if slices.Contains(types, t) {
				return b
			}
		}
	}
	return branches[0]
}

// jsonTypes are the JSON Schema types that describe v, a decoded JSON value.
func jsonTypes(v any) []string {
	switch v.(type) {
	case nil:
		return []string{"null"}
	case []any:
		return []string{"array"}
	case map[string]any:
		return []string{"object"}
	case string:
		return []string{"string"}
	case bool:
		return []string{"boolean"}
	}
	return []string{"number", "integer"}
}
