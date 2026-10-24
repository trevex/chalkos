package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"

	"github.com/trevex/chalkos/pkg/schema"
)

// Hash names a set of documents by the SHA-256, in lower-case hex, of their canonical form: the
// documents ordered by apiVersion, kind and name, as one JSON array in which every object's keys
// are sorted and every value of a Secret's spec.data is replaced by the hex SHA-256 of the value.
// Numbers are hashed as written, so 1 and 1.0 differ. It shows whether two sets are the same
// without revealing a secret. Clients hash the documents they deliver, before chalkd fills in
// defaults, so the hash depends on the operator's files alone and not on the schemas of the image
// they meet.
func Hash(docs []Document) (string, error) {
	c, err := canonical(docs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// canonical is the form of docs Hash hashes. encoding/json writes map keys sorted.
func canonical(docs []Document) ([]byte, error) {
	sorted := slices.Clone(docs)
	SortDocuments(sorted)
	objects := make([]any, len(sorted))
	for i, d := range sorted {
		objects[i] = d.Object
		if d.IsSecret() {
			objects[i] = hideSecretData(d.Object)
		}
	}
	return json.Marshal(objects)
}

// hideSecretData returns a Secret's object with each value of spec.data replaced by its SHA-256.
func hideSecretData(obj map[string]any) map[string]any {
	out := maps.Clone(obj)
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		return out
	}
	spec = maps.Clone(spec)
	if data, ok := spec["data"].(map[string]any); ok {
		spec["data"] = hideValues(data)
	}
	out["spec"] = spec
	return out
}

// hideValues returns data with each value replaced by the hex SHA-256 of the value, a string as it
// is and any other value as its JSON.
func hideValues(data map[string]any) map[string]any {
	hidden := make(map[string]any, len(data))
	for k, v := range data {
		s, isString := v.(string)
		if !isString {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		sum := sha256.Sum256([]byte(s))
		hidden[k] = hex.EncodeToString(sum[:])
	}
	return hidden
}

// KindHashes hashes the documents of each kind in docs, by kind name, together with the Secrets
// they reference, so a kind's hash changes when one of its documents does or a Secret one of them
// references. Secrets have no entry of their own. schemas tell which fields are secretRefs.
func KindHashes(docs []Document, schemas *schema.Schemas) (map[string]string, error) {
	secrets := map[string]Document{}
	byKind := map[string][]Document{}
	for _, d := range docs {
		if d.IsSecret() {
			secrets[d.Name] = d
			continue
		}
		byKind[d.Kind] = append(byKind[d.Kind], d)
	}
	out := make(map[string]string, len(byKind))
	for kind, kindDocs := range byKind {
		input := slices.Clone(kindDocs)
		referenced := map[string]bool{}
		for _, d := range kindDocs {
			for _, ref := range SecretRefs(d, schemas) {
				if sd, ok := secrets[ref.Name]; ok && !referenced[ref.Name] {
					referenced[ref.Name] = true
					input = append(input, sd)
				}
			}
		}
		h, err := Hash(input)
		if err != nil {
			return nil, err
		}
		out[kind] = h
	}
	return out, nil
}

// ChangedKinds names, sorted, the kinds whose documents differ between before and after, or a
// Secret one of them references, by their KindHashes; a kind in neither set is unchanged. A unit
// that reads a kind's documents reads the values its secretRefs name too, which the documents
// alone do not show, so a change rule cannot tell this from config and oldConfig.
func ChangedKinds(before, after []Document, schemas *schema.Schemas) ([]string, error) {
	was, err := KindHashes(before, schemas)
	if err != nil {
		return nil, err
	}
	gets, err := KindHashes(after, schemas)
	if err != nil {
		return nil, err
	}
	var changed []string
	for kind, h := range gets {
		if was[kind] != h {
			changed = append(changed, kind)
		}
	}
	for kind := range was {
		if _, ok := gets[kind]; !ok {
			changed = append(changed, kind)
		}
	}
	slices.Sort(changed)
	return changed, nil
}
