package config

import (
	"fmt"
	"strings"

	"github.com/trevex/chalkos/pkg/schema"
)

// Set is a node's whole config: every document valid against the image's schemas, with schema
// defaults filled in, and the rules of a set met.
type Set struct {
	docs    []Document
	secrets map[string]map[string]string
	// schemas are the image's, which renderers read annotations from.
	schemas *schema.Schemas
}

// singletons are the kinds a set holds exactly once.
var singletons = []struct{ apiVersion, kind string }{
	{StorageAPI, "SystemDisk"},
}

// NewSet checks docs as a node's config against the image's schemas: each document is valid, its
// kind is one the image accepts, kinds that are not multiple appear once, names are unique per
// kind, the SystemDisk is present, and every secretRef names a key of a Secret in the set. The
// Cluster and Node documents an image with Kubernetes requires are its cluster's, which
// ClusterOf checks.
func NewSet(docs []Document, s *schema.Schemas) (*Set, error) {
	multiple := map[string]bool{}
	for _, k := range s.Kinds() {
		multiple[k.APIVersion+" "+k.Kind] = k.Multiple
	}
	set := &Set{secrets: map[string]map[string]string{}, schemas: s}
	count := map[string]int{}
	names := map[string]bool{}
	for _, d := range docs {
		if err := s.Validate(d.Object); err != nil {
			return nil, err
		}
		filled, err := s.Default(d.Object)
		if err != nil {
			return nil, err
		}
		d.Object = filled.(map[string]any)
		id := d.APIVersion + " " + d.Kind
		count[id]++
		if count[id] > 1 && !multiple[id] {
			return nil, fmt.Errorf("more than one %s: the image accepts one %s per node", d.Kind, d.Kind)
		}
		if names[id+" "+d.Name] {
			return nil, fmt.Errorf("two %s documents named %q", d.Kind, d.Name)
		}
		names[id+" "+d.Name] = true
		set.docs = append(set.docs, d)
	}
	for _, k := range singletons {
		if count[k.apiVersion+" "+k.kind] == 0 {
			return nil, fmt.Errorf("no %s: a node's config holds one %s", k.kind, k.kind)
		}
	}
	SortDocuments(set.docs)
	for _, d := range set.Of(CoreAPI, "Secret") {
		var spec secretSpec
		if err := d.decodeSpec(&spec); err != nil {
			return nil, err
		}
		set.secrets[d.Name] = spec.Data
	}
	if err := checkSecretRefs(set.docs, s, set.secrets); err != nil {
		return nil, err
	}
	return set, nil
}

// Documents returns the set's documents, ordered by apiVersion, kind and name.
func (s *Set) Documents() []Document { return s.docs }

// Of returns the documents of a kind, ordered by name.
func (s *Set) Of(apiVersion, kind string) []Document {
	var out []Document
	for _, d := range s.docs {
		if d.APIVersion == apiVersion && d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

// One returns the document of a kind the set holds exactly once: the image declares it as not
// multiple, and exactly one document of it is present. Kind names are unique within an image, so
// the kind name alone finds its apiVersion, whether the kind is built in or an extension's.
func (s *Set) One(kind string) (Document, bool) {
	for _, k := range s.schemas.Kinds() {
		if k.Kind != kind {
			continue
		}
		if k.Multiple {
			return Document{}, false
		}
		if docs := s.Of(k.APIVersion, kind); len(docs) == 1 {
			return docs[0], true
		}
		return Document{}, false
	}
	return Document{}, false
}

// CheckRegistries refuses a set whose Registry and Mirror documents RegistriesOf refuses: two of
// one registry, or an http:// endpoint whose host has no Registry that allows plain HTTP.
func (s *Set) CheckRegistries() error {
	_, err := RegistriesOf(s.docs, nil)
	return err
}

// ResolveSecret returns the value a secretRef names.
func (s *Set) ResolveSecret(ref SecretRef) (string, error) {
	data, ok := s.secrets[ref.Name]
	if !ok {
		return "", fmt.Errorf("no Secret %q", ref.Name)
	}
	v, ok := data[ref.Key]
	if !ok {
		return "", fmt.Errorf("Secret %q has no key %q", ref.Name, ref.Key)
	}
	return v, nil
}

// checkSecretRefs finds every secretRef a document's own schema marks with
// "x-chalkos-secret-ref", at whatever depth and shape that schema gives it, and checks that the
// Secret and its key exist. It never mistakes a user's own data for one: a map entry named
// secretRef that the schema does not itself mark is just data, not a reference.
func checkSecretRefs(docs []Document, s *schema.Schemas, secrets map[string]map[string]string) error {
	for _, d := range docs {
		raw, ok := s.Raw(d.APIVersion, d.Kind)
		if !ok {
			// An unknown kind has already failed Validate before checkSecretRefs runs.
			continue
		}
		sch, _ := raw.(map[string]any)
		defs, _ := sch["$defs"].(map[string]any)
		err := walkSchemaRefs(defs, sch, d.Object, func(ref SecretRef) error {
			data, ok := secrets[ref.Name]
			if !ok {
				return fmt.Errorf("%s names Secret %q, which the config does not hold", d, ref.Name)
			}
			if _, ok := data[ref.Key]; !ok {
				return fmt.Errorf("%s names key %q of Secret %q, which it does not hold", d, ref.Key, ref.Name)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// walkSchemaRefs walks sch, a raw schema node, alongside the value it describes, and calls f for
// every node whose raw form carries "x-chalkos-secret-ref": true, the way the converter marks a
// lib.chalkos.types.secretRef field. It follows $ref into defs, properties into object fields,
// items into array elements, additionalProperties into a map's values, and anyOf into the branch
// whose declared type matches the value's own: null, object or array. A key or index the schema
// says nothing about is left alone, however it is shaped.
func walkSchemaRefs(defs map[string]any, sch map[string]any, v any, f func(SecretRef) error) error {
	sch = followRef(defs, sch)
	if marked, _ := sch["x-chalkos-secret-ref"].(bool); marked {
		if v == nil {
			return nil
		}
		m, _ := v.(map[string]any)
		ref, _ := m["secretRef"].(map[string]any)
		name, _ := ref["name"].(string)
		key, _ := ref["key"].(string)
		return f(SecretRef{Name: name, Key: key})
	}
	if branches, ok := sch["anyOf"].([]any); ok {
		branch, _ := pickBranch(defs, branches, v).(map[string]any)
		return walkSchemaRefs(defs, branch, v, f)
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
			if err := walkSchemaRefs(defs, prop, item, f); err != nil {
				return err
			}
		}
	case []any:
		items, _ := sch["items"].(map[string]any)
		if items == nil {
			return nil
		}
		for _, item := range val {
			if err := walkSchemaRefs(defs, items, item, f); err != nil {
				return err
			}
		}
	}
	return nil
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
