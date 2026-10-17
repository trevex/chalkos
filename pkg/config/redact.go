package config

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Redact returns docs with every secret value replaced by the hex SHA-256 of the value, as Hash
// replaces a Secret's: the values of each Secret's spec.data, and the data and stringData values of
// each Kubernetes Secret an Addon applies, which Hash keeps as they are. Two sets redact to the same
// documents where they hold the same values, so a node can show its config for comparison without
// revealing a secret. docs are left as they are.
func Redact(docs []Document) []Document {
	out := make([]Document, len(docs))
	for i, d := range docs {
		switch {
		case d.IsSecret():
			d.Object = hideSecretData(d.Object)
		case d.StaysStored():
			d.Object = hideAddonSecrets(d.Object)
		}
		out[i] = d
	}
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

// hideObjects returns Kubernetes objects with the values of their Secrets hidden, also of Secrets
// in the items of a list, such as a v1 List, which the API server applies item by item.
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
				if data, ok := k[field].(map[string]any); ok {
					k[field] = hideValues(data)
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
	case d.StaysStored():
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
