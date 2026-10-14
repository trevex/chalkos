package config

import (
	"fmt"
	"sort"

	"github.com/trevex/chalkos/pkg/schema"
)

// Set is a node's whole config: every document valid against the image's schemas, with schema
// defaults filled in, and the rules of a set met.
type Set struct {
	docs    []Document
	secrets map[string]map[string]string
	// schemas are the image's, which renderers read annotations from.
	schemas *schema.Schemas
}

// singletons are the kinds a set holds exactly once.
var singletons = []struct{ apiVersion, kind string }{
	{CoreAPI, "Cluster"},
	{CoreAPI, "Node"},
	{StorageAPI, "SystemDisk"},
}

// NewSet checks docs as a node's config against the image's schemas: each document is valid, its
// kind is one the image accepts, kinds that are not multiple appear once, names are unique per
// kind, Cluster, Node and SystemDisk are present, the Node's Image is in the set, and every
// secretRef names a key of a Secret in the set.
func NewSet(docs []Document, s *schema.Schemas) (*Set, error) {
	multiple := map[string]bool{}
	for _, k := range s.Kinds() {
		multiple[k.APIVersion+" "+k.Kind] = k.Multiple
	}
	set := &Set{secrets: map[string]map[string]string{}, schemas: s}
	count := map[string]int{}
	names := map[string]bool{}
	for _, d := range docs {
		if err := s.Validate(d.Object); err != nil {
			return nil, err
		}
		filled, err := s.Default(d.Object)
		if err != nil {
			return nil, err
		}
		d.Object = filled.(map[string]any)
		id := d.APIVersion + " " + d.Kind
		count[id]++
		if count[id] > 1 && !multiple[id] {
			return nil, fmt.Errorf("more than one %s: the image accepts one %s per node", d.Kind, d.Kind)
		}
		if names[id+" "+d.Name] {
			return nil, fmt.Errorf("two %s documents named %q", d.Kind, d.Name)
		}
		names[id+" "+d.Name] = true
		set.docs = append(set.docs, d)
	}
	for _, k := range singletons {
		if count[k.apiVersion+" "+k.kind] == 0 {
			return nil, fmt.Errorf("no %s: a node's config holds one %s", k.kind, k.kind)
		}
	}
	sort.SliceStable(set.docs, func(i, j int) bool {
		a, b := set.docs[i], set.docs[j]
		if a.APIVersion+a.Kind != b.APIVersion+b.Kind {
			return a.APIVersion+a.Kind < b.APIVersion+b.Kind
		}
		return a.Name < b.Name
	})
	var node nodeSpec
	n, _ := set.One("Node")
	if err := n.decodeSpec(&node); err != nil {
		return nil, err
	}
	if _, ok := set.get(CoreAPI, "Image", node.ImageRef); !ok {
		return nil, fmt.Errorf("%s runs Image %q, which the config does not hold", n, node.ImageRef)
	}
	for _, d := range set.Of(CoreAPI, "Secret") {
		var spec secretSpec
		if err := d.decodeSpec(&spec); err != nil {
			return nil, err
		}
		set.secrets[d.Name] = spec.Data
	}
	if err := checkSecretRefs(set.docs, set.secrets); err != nil {
		return nil, err
	}
	return set, nil
}

// Documents returns the set's documents, ordered by apiVersion, kind and name.
func (s *Set) Documents() []Document { return s.docs }

// Of returns the documents of a kind, ordered by name.
func (s *Set) Of(apiVersion, kind string) []Document {
	var out []Document
	for _, d := range s.docs {
		if d.APIVersion == apiVersion && d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

// One returns the document of a built-in kind the set holds at most once.
func (s *Set) One(kind string) (Document, bool) {
	for _, k := range singletons {
		if k.kind == kind {
			docs := s.Of(k.apiVersion, kind)
			if len(docs) == 1 {
				return docs[0], true
			}
		}
	}
	for _, api := range []string{TimeAPI, KubernetesAPI} {
		if docs := s.Of(api, kind); len(docs) == 1 {
			return docs[0], true
		}
	}
	return Document{}, false
}

func (s *Set) get(apiVersion, kind, name string) (Document, bool) {
	for _, d := range s.Of(apiVersion, kind) {
		if d.Name == name {
			return d, true
		}
	}
	return Document{}, false
}

// ResolveSecret returns the value a secretRef names.
func (s *Set) ResolveSecret(ref SecretRef) (string, error) {
	data, ok := s.secrets[ref.Name]
	if !ok {
		return "", fmt.Errorf("no Secret %q", ref.Name)
	}
	v, ok := data[ref.Key]
	if !ok {
		return "", fmt.Errorf("Secret %q has no key %q", ref.Name, ref.Key)
	}
	return v, nil
}

// checkSecretRefs finds every {secretRef: {name, key}} in the documents' specs, at any depth, and
// checks that the Secret and its key exist.
func checkSecretRefs(docs []Document, secrets map[string]map[string]string) error {
	for _, d := range docs {
		if err := walkSecretRefs(d.Object["spec"], func(ref SecretRef) error {
			data, ok := secrets[ref.Name]
			if !ok {
				return fmt.Errorf("%s names Secret %q, which the config does not hold", d, ref.Name)
			}
			if _, ok := data[ref.Key]; !ok {
				return fmt.Errorf("%s names key %q of Secret %q, which it does not hold", d, ref.Key, ref.Name)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func walkSecretRefs(v any, f func(SecretRef) error) error {
	switch v := v.(type) {
	case map[string]any:
		if ref, ok := v["secretRef"].(map[string]any); ok && len(v) == 1 {
			name, _ := ref["name"].(string)
			key, _ := ref["key"].(string)
			return f(SecretRef{Name: name, Key: key})
		}
		for _, val := range v {
			if err := walkSecretRefs(val, f); err != nil {
				return err
			}
		}
	case []any:
		for _, val := range v {
			if err := walkSecretRefs(val, f); err != nil {
				return err
			}
		}
	}
	return nil
}
