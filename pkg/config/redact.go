package config

import (
	"maps"
	"slices"
	"strconv"
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
	objects = slices.Clone(objects)
	for i, o := range objects {
		k, ok := o.(map[string]any)
		if !ok || !kubernetesSecret(k) {
			continue
		}
		k = maps.Clone(k)
		for _, field := range []string{"data", "stringData"} {
			if data, ok := k[field].(map[string]any); ok {
				k[field] = hideValues(data)
			}
		}
		objects[i] = k
	}
	out, spec := maps.Clone(obj), maps.Clone(spec)
	spec["objects"] = objects
	out["spec"] = spec
	return out
}

// kubernetesSecret tells whether a Kubernetes object is a Secret.
func kubernetesSecret(obj map[string]any) bool {
	return obj["apiVersion"] == "v1" && obj["kind"] == "Secret"
}

// Redacted tells whether the field of d at path, keys and list indexes in decimal, holds values
// Redact replaces, or is one.
func Redacted(d Document, path []string) bool {
	switch {
	case d.IsSecret():
		return len(path) >= 2 && path[0] == "spec" && path[1] == "data"
	case d.StaysStored():
		if len(path) < 4 || path[0] != "spec" || path[1] != "objects" || path[3] != "data" && path[3] != "stringData" {
			return false
		}
		spec, _ := d.Object["spec"].(map[string]any)
		objects, _ := spec["objects"].([]any)
		i, err := strconv.Atoi(path[2])
		if err != nil || i < 0 || i >= len(objects) {
			return false
		}
		obj, _ := objects[i].(map[string]any)
		return kubernetesSecret(obj)
	}
	return false
}
