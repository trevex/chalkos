package schema

import (
	"fmt"
	"maps"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Raw returns the schema document of a kind as its file holds it, for readers of annotations
// such as x-chalkos-networkd-key.
func (s *Schemas) Raw(apiVersion, kind string) (any, bool) {
	raw, ok := s.raw[key(apiVersion, kind)]
	return raw, ok
}

// Default returns doc with every field its kind's schema gives a default for and doc leaves out
// set to that default, at every level: in the fields of objects doc holds, in list items and in
// the values of maps. doc must be valid. Defaults are copied, so no two documents share them.
func (s *Schemas) Default(doc any) (any, error) {
	m, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("a config document must be an object")
	}
	apiVersion, _ := m["apiVersion"].(string)
	kind, _ := m["kind"].(string)
	sch, ok := s.schemas[key(apiVersion, kind)]
	if !ok {
		return nil, fmt.Errorf("does not accept kind %q of apiVersion %q", kind, apiVersion)
	}
	return fill(sch, doc), nil
}

func fill(sch *jsonschema.Schema, v any) any {
	for sch.Ref != nil {
		sch = sch.Ref
	}
	switch v := v.(type) {
	case map[string]any:
		if len(sch.AnyOf) > 0 {
			return fill(branchFor(sch.AnyOf, v), v)
		}
		out := maps.Clone(v)
		for name, prop := range sch.Properties {
			if val, ok := out[name]; ok {
				out[name] = fill(prop, val)
			} else if d := defaultOf(prop); d != nil {
				out[name] = fill(prop, deepCopy(*d))
			} else if isGroup(prop) {
				// A group of options, such as encryption, has no default of its own but exists
				// whenever its options have defaults, as in the module system.
				if group := fill(prop, map[string]any{}).(map[string]any); len(group) > 0 {
					out[name] = group
				}
			}
		}
		if extra, ok := sch.AdditionalProperties.(*jsonschema.Schema); ok {
			for name, val := range out {
				if _, declared := sch.Properties[name]; !declared {
					out[name] = fill(extra, val)
				}
			}
		}
		return out
	case []any:
		if len(sch.AnyOf) > 0 {
			return fill(branchFor(sch.AnyOf, v), v)
		}
		if sch.Items2020 == nil {
			return v
		}
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = fill(sch.Items2020, item)
		}
		return out
	default:
		return v
	}
}

// isGroup tells whether a schema is a plain object of declared properties, as a group of options
// converts to, and not a map or a nullable value.
func isGroup(sch *jsonschema.Schema) bool {
	for sch.Ref != nil {
		sch = sch.Ref
	}
	return len(sch.AnyOf) == 0 && len(sch.Properties) > 0
}

// defaultOf returns the default a property's schema gives, following $ref.
func defaultOf(sch *jsonschema.Schema) *any {
	for sch.Default == nil && sch.Ref != nil {
		sch = sch.Ref
	}
	return sch.Default
}

// branchFor picks the anyOf branch whose type matches v's, such as the object branch of a
// nullable object or of networkd's object-or-list sections, so defaults fill in through it.
func branchFor(branches []*jsonschema.Schema, v any) *jsonschema.Schema {
	want := "object"
	if _, ok := v.([]any); ok {
		want = "array"
	}
	for _, b := range branches {
		r := b
		for r.Ref != nil && r.Types == nil {
			r = r.Ref
		}
		if hasType(r.Types, want) {
			return b
		}
	}
	return branches[0]
}

// hasType tells whether t declares jsonType among its types. t.String() renders as e.g.
// "[object]", but ToStrings is the library's accessor for the type names themselves.
func hasType(t *jsonschema.Types, jsonType string) bool {
	if t == nil {
		return false
	}
	for _, s := range t.ToStrings() {
		if s == jsonType {
			return true
		}
	}
	return false
}

func deepCopy(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = deepCopy(val)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = deepCopy(val)
		}
		return out
	default:
		return v
	}
}
