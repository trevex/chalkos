package config

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Redact returns docs with every secret value hidden, so a node can show its config for comparison,
// and an image check it, without revealing a secret:
//   - each value of a Secret's spec.data as hideValue hides it: a string by its hex SHA-256, as
//     Hash replaces it, and any other value by the zero value of its JSON type; a spec.data, or a
//     spec, that is no object, which no schema accepts, the same way as a whole;
//   - each data and stringData value of a Secret object another document embeds, at any depth, by
//     the hex SHA-256 of the value, of its JSON where it is no string, which Hash keeps as they
//     are. An embedded Secret object is an object of apiVersion v1, or /v1, and kind Secret in
//     any casing, such as a document carries for another API to apply.
//
// Redact needs no schemas: it hides the same values whichever image's documents it is given, also
// those of an image the node does not run. Two sets redact to the same documents where they hold
// the same strings, and an embedded Secret object's to the same where they hold the same values of
// any type. docs are left as they are.
func Redact(docs []Document) []Document {
	out := make([]Document, len(docs))
	for i, d := range docs {
		d.Object = d.Hashed()
		out[i] = d
	}
	return out
}

// Hashed is the document's object with its secret values hidden, as Redact hides them: what rules
// and command facts see of a document. The document is left as it is.
func (d Document) Hashed() map[string]any {
	if d.IsSecret() {
		return hideSecret(d.Object)
	}
	if obj, changed := hideEmbedded(d.Object); changed {
		return obj.(map[string]any)
	}
	return d.Object
}

// hideSecret returns a Secret's object with the values of spec.data hidden by hideValue, and
// spec.data or spec hidden whole where it is no object.
func hideSecret(obj map[string]any) map[string]any {
	out := maps.Clone(obj)
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		if v, present := obj["spec"]; present {
			out["spec"] = hideValue(v)
		}
		return out
	}
	spec = maps.Clone(spec)
	switch data := spec["data"].(type) {
	case nil:
	case map[string]any:
		spec["data"] = hideValues(data)
	default:
		spec["data"] = hideValue(data)
	}
	out["spec"] = spec
	return out
}

// secretFields are the fields of an embedded Secret object that hold its values.
var secretFields = []string{"data", "stringData"}

// hideEmbedded returns v with the values of each Secret object in it hashed, and whether it hid
// any; v itself is left as it is, and a part of it without a Secret object is shared, not copied.
// The values are hashed whatever their type, so a change of any value changes the hash of the
// document's kind, and a data that is no object is hashed whole.
func hideEmbedded(v any) (any, bool) {
	switch v := v.(type) {
	case map[string]any:
		var out map[string]any
		secret := embeddedSecret(v)
		for k, x := range v {
			var hidden any
			changed := false
			switch {
			case secret && slices.Contains(secretFields, k):
				switch data := x.(type) {
				case nil:
				case map[string]any:
					hidden, changed = hashValues(data), true
				default:
					hidden, changed = hashValue(data), true
				}
			default:
				hidden, changed = hideEmbedded(x)
			}
			if changed {
				if out == nil {
					out = maps.Clone(v)
				}
				out[k] = hidden
			}
		}
		if out == nil {
			return v, false
		}
		return out, true
	case []any:
		var out []any
		for i, x := range v {
			if hidden, changed := hideEmbedded(x); changed {
				if out == nil {
					out = slices.Clone(v)
				}
				out[i] = hidden
			}
		}
		if out == nil {
			return v, false
		}
		return out, true
	}
	return v, false
}

// embeddedSecret tells whether obj is a Secret object a document embeds: of the core group's
// version v1 and kind Secret. The apiVersion is v1 or /v1, both of which Kubernetes parses as the
// core group's v1. The kind is compared in any casing: what an API server would refuse is still
// not to be shown.
func embeddedSecret(obj map[string]any) bool {
	apiVersion, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)
	return strings.TrimPrefix(apiVersion, "/") == "v1" && strings.EqualFold(kind, "Secret")
}

// Redacted tells whether the field of d at path, keys and list indexes in decimal, holds values
// Redact replaces, or is one.
func Redacted(d Document, path []string) bool {
	if d.IsSecret() {
		return len(path) >= 2 && path[0] == "spec" && path[1] == "data"
	}
	var v any = d.Object
	for _, p := range path {
		switch n := v.(type) {
		case map[string]any:
			if embeddedSecret(n) && slices.Contains(secretFields, p) {
				return true
			}
			v = n[p]
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(n) {
				return false
			}
			v = n[i]
		default:
			return false
		}
	}
	return false
}
