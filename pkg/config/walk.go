package config

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/schema"
)

// A node is a schema node the walker reached and the value it describes there.
type node struct {
	// sch is the raw schema node, with a local $ref followed.
	sch map[string]any
	// v is the value, nil where the document leaves the field out.
	v any
	// path is the value's path below the document, such as spec.rules[2].to.
	path string
	// field is the schema's path, the same for every value the node describes: [] stands for each
	// item of a list and [*] for each value of a map, as in spec.rules[].to.
	field string
}

// walkDocument walks d alongside its kind's schema in schemas, as walk does, from the document's
// root; it walks nothing for a kind schemas does not declare, or without schemas.
func walkDocument(d Document, schemas *schema.Schemas, visit func(node) bool) {
	if schemas == nil {
		return
	}
	raw, ok := schemas.Raw(d.APIVersion, d.Kind)
	if !ok {
		return
	}
	sch, _ := raw.(map[string]any)
	defs, _ := sch["$defs"].(map[string]any)
	walk(defs, node{sch: sch, v: d.Object}, visit)
}

// walk walks n's schema node alongside its value and calls visit for every node it reaches,
// where visit returns whether to walk on below the node. It follows $ref into defs, anyOf into
// the branch whose declared type matches the value's own, properties into object fields, items
// into array elements and additionalProperties into a map's values. visit sees a node with anyOf,
// such as a nullable field's, before its branch, and both with the same path. A key or index the
// schema says nothing about is left alone, however it is shaped.
func walk(defs map[string]any, n node, visit func(node) bool) {
	// visit sees the definition in place of a node with $ref, so a marker such as x-chalkos-unique
	// counts only on the definition. The kinds converter emits no $ref; networkd's sections do.
	n.sch = followRef(defs, n.sch)
	if !visit(n) {
		return
	}
	if branches, ok := n.sch["anyOf"].([]any); ok {
		branch, _ := pickBranch(defs, branches, n.v).(map[string]any)
		walk(defs, node{sch: branch, v: n.v, path: n.path, field: n.field}, visit)
		return
	}
	switch val := n.v.(type) {
	case map[string]any:
		props, _ := n.sch["properties"].(map[string]any)
		extra, _ := n.sch["additionalProperties"].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(val)) {
			prop, _ := props[name].(map[string]any)
			field := fieldPath(n.field, name)
			if prop == nil {
				prop, field = extra, n.field+"[*]"
			}
			if prop == nil {
				continue
			}
			walk(defs, node{sch: prop, v: val[name], path: fieldPath(n.path, name), field: field}, visit)
		}
	case []any:
		items, _ := n.sch["items"].(map[string]any)
		if items == nil {
			return
		}
		for i, item := range val {
			walk(defs, node{sch: items, v: item, path: n.path + "[" + strconv.Itoa(i) + "]", field: n.field + "[]"}, visit)
		}
	}
}

// plainKey is a field or map key a path writes after a dot; any other goes in quoted brackets.
var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// fieldPath is path with the field or map key name appended.
func fieldPath(path, name string) string {
	switch {
	case !plainKey.MatchString(name):
		return path + "[" + strconv.Quote(name) + "]"
	case path == "":
		return name
	}
	return path + "." + name
}

// followRef resolves a local "$ref": "#/$defs/Name", as the networkd schemas emit for each
// section, to the definition it names.
func followRef(defs map[string]any, sch map[string]any) map[string]any {
	for {
		ref, ok := sch["$ref"].(string)
		if !ok {
			return sch
		}
		name, ok := strings.CutPrefix(ref, "#/$defs/")
		next, _ := defs[name].(map[string]any)
		if !ok || next == nil {
			return sch
		}
		sch = next
	}
}

// pickBranch returns the anyOf branch whose declared type matches v's JSON type, or the first
// branch when none does. It is the schema package's own branchFor, read on a raw schema document
// instead of a compiled one.
func pickBranch(defs map[string]any, branches []any, v any) any {
	for _, b := range branches {
		bm, _ := b.(map[string]any)
		bm = followRef(defs, bm)
		for _, t := range jsonTypes(v) {
			if rawHasType(bm, t) {
				return b
			}
		}
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return nil
}

// jsonTypes are the JSON Schema types that describe v, a value as Parse reads it.
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

// rawHasType tells whether a raw schema's "type" names jsonType, whether it is given as a single
// string or, as networkd's passthrough values declare it, a list of strings.
func rawHasType(sch map[string]any, jsonType string) bool {
	switch t := sch["type"].(type) {
	case string:
		return t == jsonType
	case []any:
		for _, v := range t {
			if s, _ := v.(string); s == jsonType {
				return true
			}
		}
	}
	return false
}
