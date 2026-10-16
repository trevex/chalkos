package schema

import (
	"fmt"
	"maps"
	"slices"

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
	work := maxFillWork
	return fill(sch, doc, 0, &work)
}

// maxDepth bounds how deep Default follows a schema, in nested values and in $ref chains. A
// schema that refers to itself, as one its reader chose may, would otherwise fill groups of
// options without end, or follow its $refs round and round.
const maxDepth = 128

var errTooDeep = fmt.Errorf("the schema nests deeper than %d levels, or refers to itself without end", maxDepth)

// maxFillWork bounds the schema nodes Default visits for one document. LoadSchemas refuses
// schemas whose expanded size would make the work large; this guards Default on its own, since
// a group of options it fills in visits schema nodes the document does not hold.
const maxFillWork = 1 << 18

var errTooMuchWork = fmt.Errorf("filling in the defaults takes more than %d steps", maxFillWork)

func fill(sch *jsonschema.Schema, v any, depth int, work *int) (any, error) {
	if depth > maxDepth {
		return nil, errTooDeep
	}
	if *work--; *work < 0 {
		return nil, errTooMuchWork
	}
	// A schema with $ref carries no other keyword of its own: the generators never emit both.
	sch, err := deref(sch)
	if err != nil {
		return nil, err
	}
	switch v := v.(type) {
	case map[string]any:
		if len(sch.AnyOf) > 0 {
			return fill(branchFor(sch.AnyOf, v), v, depth+1, work)
		}
		out := maps.Clone(v)
		for name, prop := range sch.Properties {
			var filled any
			var err error
			if val, ok := out[name]; ok {
				filled, err = fill(prop, val, depth+1, work)
			} else if d := defaultOf(prop); d != nil {
				filled, err = fill(prop, deepCopy(*d), depth+1, work)
			} else if isGroup(prop) {
				// A group of options, such as encryption, has no default of its own but exists
				// whenever its options have defaults, as in the module system.
				if filled, err = fill(prop, map[string]any{}, depth+1, work); err == nil && len(filled.(map[string]any)) == 0 {
					continue
				}
			} else {
				continue
			}
			if err != nil {
				return nil, err
			}
			out[name] = filled
		}
		if extra, ok := sch.AdditionalProperties.(*jsonschema.Schema); ok {
			for name, val := range out {
				if _, declared := sch.Properties[name]; !declared {
					if out[name], err = fill(extra, val, depth+1, work); err != nil {
						return nil, err
					}
				}
			}
		}
		return out, nil
	case []any:
		if len(sch.AnyOf) > 0 {
			return fill(branchFor(sch.AnyOf, v), v, depth+1, work)
		}
		if sch.Items2020 == nil {
			return v, nil
		}
		out := make([]any, len(v))
		for i, item := range v {
			if out[i], err = fill(sch.Items2020, item, depth+1, work); err != nil {
				return nil, err
			}
		}
		return out, nil
	default:
		return v, nil
	}
}

// deref follows sch's $refs to the schema they end at.
func deref(sch *jsonschema.Schema) (*jsonschema.Schema, error) {
	for hops := 0; sch.Ref != nil; hops++ {
		if hops > maxDepth {
			return nil, errTooDeep
		}
		sch = sch.Ref
	}
	return sch, nil
}

// isGroup tells whether a schema is a plain object of declared properties, as a group of options
// converts to, and not a map or a nullable value.
func isGroup(sch *jsonschema.Schema) bool {
	sch, err := deref(sch)
	return err == nil && len(sch.AnyOf) == 0 && len(sch.Properties) > 0
}

// defaultOf returns the default a property's schema gives, following $ref.
func defaultOf(sch *jsonschema.Schema) *any {
	for hops := 0; sch.Default == nil && sch.Ref != nil && hops <= maxDepth; hops++ {
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
		for hops := 0; r.Ref != nil && r.Types == nil && hops <= maxDepth; hops++ {
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
	return slices.Contains(t.ToStrings(), jsonType)
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
