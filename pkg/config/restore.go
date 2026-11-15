package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/trevex/chalkos/pkg/jsonfile"
)

// FromJSON is the document a JSON object holds, its numbers kept as json.Number so no digit is
// rounded away.
func FromJSON(data []byte) (Document, error) {
	var obj map[string]any
	if err := jsonfile.DecodeExact(data, &obj); err != nil {
		return Document{}, err
	}
	if obj == nil {
		return Document{}, fmt.Errorf("the document is no JSON object")
	}
	return FromObject(obj), nil
}

// Restore puts the secret values of delivered back into hashed, a check's redacted output of
// delivered with defaults filled; no schema fills defaults into those values. An embedded Secret
// object keeps its apiVersion and kind as delivered. It refuses output that is not delivered plus
// defaults, such as a check that filled defaults into hashed values or added a Secret object with
// values.
func Restore(hashed, delivered []Document) ([]Document, error) {
	values := make(map[ID]Document, len(delivered))
	for _, d := range delivered {
		values[d.ID()] = d
	}
	out := make([]Document, len(hashed))
	seen := make(map[ID]bool, len(hashed))
	for i, h := range hashed {
		k := h.ID()
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
			// A document without secret values is compared too: its hashed form is itself only
			// while the check added no Secret object with values to it.
			obj, ok := restoreEmbedded(h.Object, d.Object)
			if !ok {
				return nil, mismatch
			}
			out[i].Object = obj.(map[string]any)
		}
		got, err := json.Marshal(out[i].hidden())
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

// restoreEmbedded returns a copy of h, a check's hashed form of d, with the data and stringData
// of each Secret object d embeds taken from d. It returns false where h's shape on the way to
// such an object, or the object's apiVersion or kind, differs from d.
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
			if hm["apiVersion"] != d["apiVersion"] || hm["kind"] != d["kind"] {
				return nil, false
			}
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
