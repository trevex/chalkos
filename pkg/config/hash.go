package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/trevex/chalkos/pkg/schema"
)

// Hash names a set of documents by the SHA-256, in lower-case hex, of their canonical form: the
// documents as Redact gives them, ordered by apiVersion, kind and name, as one JSON array in which
// every object's keys are sorted. Numbers are hashed as FromObject keeps them, so 1 and 1.0 hash
// alike while 1.5 and 1.50 differ. It shows whether two sets are the same without revealing a
// secret. Clients hash the documents they deliver, before chalkd fills in defaults, so the hash
// depends on the operator's files alone and not on the schemas of the image they meet.
func Hash(docs []Document) (string, error) {
	return Redact(docs).hash()
}

// hash is the SHA-256, in lower-case hex, of the documents ordered by apiVersion, kind and name,
// as one JSON array. encoding/json writes map keys sorted.
func (r Redacted) hash() (string, error) {
	sorted := slices.Clone(r.docs)
	SortDocuments(sorted)
	objects := make([]any, len(sorted))
	for i, d := range sorted {
		objects[i] = d.Object
	}
	c, err := json.Marshal(objects)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// KindHashes hashes the documents of each kind in docs, by kind name, together with the Secrets
// they reference, as Hash hashes a set, so a kind's hash changes when one of its documents does or
// a Secret one of them references. Secrets have no entry of their own. schemas tell which fields
// are key references.
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
