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
// spec.data, and the data and stringData of each Secret object delivered embeds, which no schema
// fills defaults into. It refuses documents that are not those of delivered with defaults: one
// that delivered lacks, two of one name, a delivered document that is missing, one that lacks the
// place of a Secret object delivered embeds, or one whose hashed form differs from Redact of the
// result, as when a check filled defaults into the values it got hashed.
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
		mismatch := fmt.Errorf("%s holds other secret values than were delivered", h)
		switch {
		case d.IsSecret():
			out[i].Object = restoreSecret(h.Object, d.Object)
		default:
			if _, hides := hideEmbedded(d.Object); !hides {
				continue
			}
			obj, ok := restoreEmbedded(h.Object, d.Object)
			if !ok {
				return nil, mismatch
			}
			out[i].Object = obj.(map[string]any)
		}
		got, err := json.Marshal(out[i].Hashed())
		if err != nil {
			return nil, err
		}
		want, err := json.Marshal(h.Object)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(got, want) {
			return nil, mismatch
		}
	}
	if len(seen) != len(values) {
		return nil, fmt.Errorf("documents are missing: %d delivered, %d checked", len(values), len(seen))
	}
	return out, nil
}

// restoreSecret returns h, a check's hashed form of the Secret d, with the spec.data of d.
func restoreSecret(h, d map[string]any) map[string]any {
	obj := maps.Clone(h)
	spec, _ := h["spec"].(map[string]any)
	spec = maps.Clone(spec)
	if spec == nil {
		spec = map[string]any{}
	}
	from, _ := d["spec"].(map[string]any)
	if v, ok := from["data"]; ok {
		spec["data"] = v
	} else {
		delete(spec, "data")
	}
	obj["spec"] = spec
	return obj
}

// restoreEmbedded returns h, a check's hashed form of d, with the data and stringData of each
// Secret object d embeds taken from d, at the same place in h, and the rest of h, its defaults
// among it, as it is. It returns false where h lacks the place of such an object, or holds a list
// of another length on the way to it. h itself is left as it is.
func restoreEmbedded(h, d any) (any, bool) {
	switch d := d.(type) {
	case map[string]any:
		hm, ok := h.(map[string]any)
		if !ok {
			return nil, false
		}
		out := maps.Clone(hm)
		secret := embeddedSecret(d)
		if secret {
			for _, f := range secretFields {
				if v, ok := d[f]; ok {
					out[f] = v
				} else {
					delete(out, f)
				}
			}
		}
		for k, x := range d {
			if secret && slices.Contains(secretFields, k) {
				continue
			}
			if _, hides := hideEmbedded(x); !hides {
				continue
			}
			hx, ok := hm[k]
			if !ok {
				return nil, false
			}
			if out[k], ok = restoreEmbedded(hx, x); !ok {
				return nil, false
			}
		}
		return out, true
	case []any:
		hl, ok := h.([]any)
		if !ok || len(hl) != len(d) {
			return nil, false
		}
		out := slices.Clone(hl)
		for i, x := range d {
			if _, hides := hideEmbedded(x); !hides {
				continue
			}
			if out[i], ok = restoreEmbedded(hl[i], x); !ok {
				return nil, false
			}
		}
		return out, true
	}
	return h, true
}
