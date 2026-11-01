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

// hashValues returns data with each value replaced by hashValue of it, as Hash names a set.
func hashValues(data map[string]any) map[string]any {
	hidden := make(map[string]any, len(data))
	for k, v := range data {
		hidden[k] = hashValue(v)
	}
	return hidden
}

// hashValue is the hex SHA-256 of v: of a string as it is, of any other value as its JSON.
func hashValue(v any) string {
	s, isString := v.(string)
	if !isString {
		b, _ := json.Marshal(v)
		s = string(b)
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hideValues returns data with each value hidden by hideValue, as Redact hides a Secret's.
func hideValues(data map[string]any) map[string]any {
	hidden := make(map[string]any, len(data))
	for k, v := range data {
		hidden[k] = hideValue(v)
	}
	return hidden
}

// hideValue hides a value of a Secret as Redact does: a string by its hex SHA-256, as Hash
// replaces it, and any other value by the zero value of its JSON type, such as 0 for a number,
// also one Go built as an int or a float64 rather than parsed as a json.Number. A value that is no
// string keeps its type, so a schema that refuses it refuses the hidden document with the same
// message, as a schema's message of a value of the wrong type names the type alone. Secret values
// are strings, so a document that holds another passes no check, and nothing compares the zero
// values.
func hideValue(v any) any {
	switch v := v.(type) {
	case string:
		return hashValue(v)
	case bool:
		return false
	case json.Number, float64, float32, int, int64, int32, uint, uint64, uint32:
		return json.Number("0")
	case []any:
		return []any{}
	case map[string]any:
		return map[string]any{}
	}
	return nil
}

// KindHashes hashes the documents of each kind in docs, by kind name, together with the Secrets
// they reference, so a kind's hash changes when one of its documents does or a Secret one of them
// references. Secrets have no entry of their own. schemas tell which fields are secretRefs.
//
// It hashes docs as Redact gives them, an Addon's Kubernetes Secrets hidden too, which Hash keeps
// as they are. hashed tells that docs come from Redact already; they give the same hashes as the
// documents with their values.
func KindHashes(docs []Document, schemas *schema.Schemas, hashed bool) (map[string]string, error) {
	if !hashed {
		docs = Redact(docs)
	}
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
		h, err := hashRedacted(input)
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
	was, err := KindHashes(before, schemas, false)
	if err != nil {
		return nil, err
	}
	gets, err := KindHashes(after, schemas, false)
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

// hashRedacted is Hash of documents Redact gave: their Secrets' values are hashed already, so it
// hashes the objects as they are, which for a set without Addons, whose Secret values are strings,
// is Hash of the set before Redact.
func hashRedacted(docs []Document) (string, error) {
	return hashJSON(sortedJSON(docs, func(d Document) map[string]any { return d.Object }))
}
