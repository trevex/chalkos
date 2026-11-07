package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// FromJSON is the document a JSON object holds, its numbers kept as json.Number, as Parse keeps
// them, so no digit is rounded away.
func FromJSON(data []byte) (Document, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return Document{}, err
	}
	if obj == nil {
		return Document{}, fmt.Errorf("the document is no JSON object")
	}
	return FromObject(obj), nil
}

// Restore returns hashed, documents a check of delivered gave back with their defaults and their
// secret values hashed, with the secret values of delivered in place of the hashes: each Secret's
// spec.data, and each field of a spec in which delivered embeds a Secret object, which no schema
// fills defaults into. It refuses documents that are not those of delivered with defaults: one
// that delivered lacks, two of one name, a delivered document that is missing, or one whose
// hashed form differs from Redact of the result, as when a check filled defaults into the values
// it got hashed.
func Restore(hashed, delivered []Document) ([]Document, error) {
	type key struct{ apiVersion, kind, name string }
	of := func(d Document) key { return key{d.APIVersion, d.Kind, d.Name} }
	values := make(map[key]Document, len(delivered))
	for _, d := range delivered {
		values[of(d)] = d
	}
	out := make([]Document, len(hashed))
	seen := make(map[key]bool, len(hashed))
	for i, h := range hashed {
		k := of(h)
		d, ok := values[k]
		switch {
		case !ok:
			return nil, fmt.Errorf("%s is no delivered document", h)
		case seen[k]:
			return nil, fmt.Errorf("%s is among the checked documents twice", h)
		}
		seen[k] = true
		out[i] = h
		fields := restoredFields(d)
		if len(fields) == 0 {
			continue
		}
		spec, _ := h.Object["spec"].(map[string]any)
		from, _ := d.Object["spec"].(map[string]any)
		obj, spec := maps.Clone(h.Object), maps.Clone(spec)
		if spec == nil {
			spec = map[string]any{}
		}
		for _, field := range fields {
			if v, ok := from[field]; ok {
				spec[field] = v
			} else {
				delete(spec, field)
			}
		}
		obj["spec"] = spec
		out[i].Object = obj
		got, err := json.Marshal(out[i].Hashed())
		if err != nil {
			return nil, err
		}
		want, err := json.Marshal(h.Object)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(got, want) {
			return nil, fmt.Errorf("%s holds other secret values than were delivered", h)
		}
	}
	if len(seen) != len(values) {
		return nil, fmt.Errorf("documents are missing: %d delivered, %d checked", len(values), len(seen))
	}
	return out, nil
}

// restoredFields are the fields of d's spec whose values Restore takes from d: a Secret's data,
// and the fields of any other document that embed a Secret object.
func restoredFields(d Document) []string {
	if d.IsSecret() {
		return []string{"data"}
	}
	spec, _ := d.Object["spec"].(map[string]any)
	var fields []string
	for _, field := range slices.Sorted(maps.Keys(spec)) {
		if _, hides := hideEmbedded(spec[field]); hides {
			fields = append(fields, field)
		}
	}
	return fields
}
