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
//   - each data and stringData value of a Kubernetes Secret an Addon applies by the hex SHA-256 of
//     the value, of its JSON where it is no string, which Hash keeps as they are.
//
// Two sets redact to the same documents where they hold the same strings, and an Addon's to the
// same where they hold the same values of any type. docs are left as they are.
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
	switch {
	case d.IsSecret():
		return hideSecret(d.Object)
	case d.IsAddon():
		return hideAddonSecrets(d.Object)
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

// hideAddonSecrets returns an Addon's object with the values of its Kubernetes Secrets hidden.
func hideAddonSecrets(obj map[string]any) map[string]any {
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		return obj
	}
	objects, ok := spec["objects"].([]any)
	if !ok {
		return obj
	}
	out, spec := maps.Clone(obj), maps.Clone(spec)
	spec["objects"] = hideObjects(objects)
	out["spec"] = spec
	return out
}

// hideObjects returns Kubernetes objects with the values of their Secrets hashed, also of Secrets
// in the items of a list, such as a v1 List, which the API server applies item by item. The values
// are hashed whatever their type, so a change of any value changes the hash of the Addon's kind,
// and a data that is no object is hashed whole.
func hideObjects(objects []any) []any {
	objects = slices.Clone(objects)
	for i, o := range objects {
		k, ok := o.(map[string]any)
		if !ok {
			continue
		}
		switch {
		case kubernetesSecret(k):
			k = maps.Clone(k)
			for _, field := range []string{"data", "stringData"} {
				switch data := k[field].(type) {
				case nil:
				case map[string]any:
					k[field] = hashValues(data)
				default:
					k[field] = hashValue(data)
				}
			}
		case kubernetesList(k):
			k = maps.Clone(k)
			k["items"] = hideObjects(k["items"].([]any))
		default:
			continue
		}
		objects[i] = k
	}
	return objects
}

// kubernetesSecret tells whether a Kubernetes object is a Secret. The kind is compared in any
// casing: what the API server would refuse is still not to be shown.
func kubernetesSecret(obj map[string]any) bool {
	kind, _ := obj["kind"].(string)
	return obj["apiVersion"] == "v1" && strings.EqualFold(kind, "Secret")
}

// kubernetesList tells whether a Kubernetes object is a list of objects in its items, of a kind
// ending in List in any casing.
func kubernetesList(obj map[string]any) bool {
	kind, _ := obj["kind"].(string)
	_, ok := obj["items"].([]any)
	return ok && len(kind) >= 4 && strings.EqualFold(kind[len(kind)-4:], "List")
}

// Redacted tells whether the field of d at path, keys and list indexes in decimal, holds values
// Redact replaces, or is one.
func Redacted(d Document, path []string) bool {
	switch {
	case d.IsSecret():
		return len(path) >= 2 && path[0] == "spec" && path[1] == "data"
	case d.IsAddon():
		if len(path) < 2 || path[0] != "spec" || path[1] != "objects" {
			return false
		}
		spec, _ := d.Object["spec"].(map[string]any)
		objects, _ := spec["objects"].([]any)
		return redactedObject(objects, path[2:])
	}
	return false
}

// redactedObject tells whether the field at path, below a list of Kubernetes objects, holds values
// hideObjects replaces.
func redactedObject(objects []any, path []string) bool {
	if len(path) < 2 {
		return false
	}
	i, err := strconv.Atoi(path[0])
	if err != nil || i < 0 || i >= len(objects) {
		return false
	}
	obj, _ := objects[i].(map[string]any)
	switch {
	case kubernetesSecret(obj):
		return path[1] == "data" || path[1] == "stringData"
	case kubernetesList(obj) && path[1] == "items":
		return redactedObject(obj["items"].([]any), path[2:])
	}
	return false
}
