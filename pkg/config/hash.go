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
// Numbers are hashed as FromObject keeps them, so 1 and 1.0 hash alike while 1.5 and 1.50 differ. It shows whether two sets are the same
// without revealing a secret. Clients hash the documents they deliver, before chalkd fills in
// defaults, so the hash depends on the operator's files alone and not on the schemas of the image
// they meet.
func Hash(docs []Document) (string, error) {
	return hashJSON(canonical(docs))
}

// canonical is the form of docs Hash hashes.
func canonical(docs []Document) ([]byte, error) {
	return sortedJSON(docs, func(d Document) map[string]any {
		if d.IsSecret() {
			return hashSecretData(d.Object)
		}
		return d.Object
	})
}

// sortedJSON is docs ordered by apiVersion, kind and name, each as object gives it, as one JSON
// array. encoding/json writes map keys sorted.
func sortedJSON(docs []Document, object func(Document) map[string]any) ([]byte, error) {
	sorted := slices.Clone(docs)
	SortDocuments(sorted)
	objects := make([]any, len(sorted))
	for i, d := range sorted {
		objects[i] = object(d)
	}
	return json.Marshal(objects)
}

// hashJSON is the SHA-256, in lower-case hex, of c, the JSON a marshal returned with err.
func hashJSON(c []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// hashSecretData returns a Secret's object with the values of spec.data hashed by hashValues, as
// Hash hashes them; a spec.data that is no object stays as it is, which Hash hashes all the same.
func hashSecretData(obj map[string]any) map[string]any {
	out := maps.Clone(obj)
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		return out
	}
	spec = maps.Clone(spec)
	if data, ok := spec["data"].(map[string]any); ok {
		spec["data"] = hashValues(data)
	}
	out["spec"] = spec
	return out
}

// KindHashes hashes the documents of each kind in docs, by kind name, together with the Secrets
// they reference, so a kind's hash changes when one of its documents does or a Secret one of them
// references. Secrets have no entry of their own. schemas tell which fields are key references.
// It hashes the documents as Redact gave them, the Secret objects documents embed hidden too,
// which Hash keeps as they are.
func KindHashes(docs Redacted, schemas *schema.Schemas) (map[string]string, error) {
	secrets := map[string]Document{}
	byKind := map[string]Documents{}
	for _, d := range docs.docs {
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
			for _, r := range Refs(d, schemas) {
				ref, ok := r.Secret()
				if !ok {
					continue
				}
				if sd, ok := secrets[ref.Name]; ok && !referenced[ref.Name] {
					referenced[ref.Name] = true
					input = append(input, sd)
				}
			}
		}
		h, err := Redacted{docs: input}.hash()
		if err != nil {
			return nil, err
		}
		out[kind] = h
	}
	return out, nil
}

// hash is the SHA-256, in lower-case hex, of the documents ordered by apiVersion, kind and name,
// as one JSON array, which for a set that embeds no Secret object, and whose Secret values are
// strings, is Hash of the set before Redact.
func (r Redacted) hash() (string, error) {
	return hashJSON(sortedJSON(r.docs, func(d Document) map[string]any { return d.Object }))
}
