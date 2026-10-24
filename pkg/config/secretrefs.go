package config

import (
	"cmp"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/schema"
)

// SecretRefs returns the secretRefs of d that its kind's schema in schemas marks, ordered by
// Secret and key; none for a kind schemas does not declare, or without schemas.
func SecretRefs(d Document, schemas *schema.Schemas) []SecretRef {
	if schemas == nil {
		return nil
	}
	raw, ok := schemas.Raw(d.APIVersion, d.Kind)
	if !ok {
		return nil
	}
	sch, _ := raw.(map[string]any)
	defs, _ := sch["$defs"].(map[string]any)
	var refs []SecretRef
	walkSchemaRefs(defs, sch, d.Object, func(ref SecretRef) { refs = append(refs, ref) })
	slices.SortFunc(refs, func(a, b SecretRef) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Key, b.Key))
	})
	return refs
}

// walkSchemaRefs walks sch, a raw schema node, alongside the value it describes, and calls f for
// every node whose raw form carries "x-chalkos-secret-ref": true, the way the converter marks a
// lib.chalkos.types.secretRef field. It follows $ref into defs, properties into object fields,
// items into array elements, additionalProperties into a map's values, and anyOf into the branch
// whose declared type matches the value's own: null, object or array. A key or index the schema
// says nothing about is left alone, however it is shaped.
func walkSchemaRefs(defs map[string]any, sch map[string]any, v any, f func(SecretRef)) {
	sch = followRef(defs, sch)
	if marked, _ := sch["x-chalkos-secret-ref"].(bool); marked {
		if v == nil {
			return
		}
		m, _ := v.(map[string]any)
		ref, _ := m["secretRef"].(map[string]any)
		name, _ := ref["name"].(string)
		key, _ := ref["key"].(string)
		f(SecretRef{Name: name, Key: key})
		return
	}
	if branches, ok := sch["anyOf"].([]any); ok {
		branch, _ := pickBranch(defs, branches, v).(map[string]any)
		walkSchemaRefs(defs, branch, v, f)
		return
	}
	switch val := v.(type) {
	case map[string]any:
		props, _ := sch["properties"].(map[string]any)
		extra, _ := sch["additionalProperties"].(map[string]any)
		for name, item := range val {
			prop, _ := props[name].(map[string]any)
			if prop == nil {
				prop = extra
			}
			if prop == nil {
				continue
			}
			walkSchemaRefs(defs, prop, item, f)
		}
	case []any:
		items, _ := sch["items"].(map[string]any)
		if items == nil {
			return
		}
		for _, item := range val {
			walkSchemaRefs(defs, items, item, f)
		}
	}
}

// followRef resolves a local "$ref": "#/$defs/Name", as the converter emits for a repeated
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

// pickBranch returns the anyOf branch whose declared type matches v's JSON type: null for a nil
// value, array for a slice, object otherwise. It is the schema package's own branchFor, read on a
// raw schema document instead of a compiled one.
func pickBranch(defs map[string]any, branches []any, v any) any {
	want := "object"
	switch v.(type) {
	case nil:
		want = "null"
	case []any:
		want = "array"
	}
	for _, b := range branches {
		bm, _ := b.(map[string]any)
		if rawHasType(followRef(defs, bm), want) {
			return b
		}
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return nil
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
