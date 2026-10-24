package config

import (
	"fmt"

	"github.com/trevex/chalkos/pkg/schema"
)

// Set is a node's whole config: every document valid against the image's schemas, with schema
// defaults filled in, and the rules of a set met.
type Set struct {
	docs Documents
	// schemas are the image's, which renderers read annotations from.
	schemas *schema.Schemas
}

// singletons are the kinds a set holds exactly once.
var singletons = []struct{ apiVersion, kind string }{
	{StorageAPI, "SystemDisk"},
}

// NewSet checks docs as a node's config against the image's schemas: each document is valid, its
// kind is one the image accepts, kinds that are not multiple appear once, names are unique per
// kind, the SystemDisk is present, and every secretRef names a key of a Secret in the set and a
// credential file of its own. The Cluster and Node documents an image with Kubernetes requires
// are its cluster's, which Documents.Cluster checks.
func NewSet(docs []Document, s *schema.Schemas) (*Set, error) {
	multiple := map[string]bool{}
	for _, k := range s.Kinds() {
		multiple[k.APIVersion+" "+k.Kind] = k.Multiple
	}
	set := &Set{schemas: s}
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
		d.Object = filled
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
	SortDocuments(set.docs)
	if err := checkSecretRefs(set.docs, s, set.docs.Secrets()); err != nil {
		return nil, err
	}
	return set, nil
}

// Documents returns the set's documents, ordered by apiVersion, kind and name.
func (s *Set) Documents() Documents { return s.docs }

// One returns the document of a kind the set holds exactly once: the image declares it as not
// multiple, and exactly one document of it is present. Kind names are unique within an image, so
// the kind name alone finds its apiVersion, whether the kind is built in or an extension's.
func (s *Set) One(kind string) (Document, bool) {
	for _, k := range s.schemas.Kinds() {
		if k.Kind != kind {
			continue
		}
		if k.Multiple {
			return Document{}, false
		}
		if docs := s.docs.Of(k.APIVersion, kind); len(docs) == 1 {
			return docs[0], true
		}
		return Document{}, false
	}
	return Document{}, false
}

// CheckRegistries refuses a set whose Registry and Mirror documents RegistriesOf refuses: two of
// one registry, or an http:// endpoint whose host has no Registry that allows plain HTTP.
func (s *Set) CheckRegistries() error {
	_, err := RegistriesOf(s.docs, nil)
	return err
}

// checkSecretRefs checks every secretRef SecretRefs finds in docs: the Secret and its key exist,
// and the reference can become a credential file of its own, as the node writes one for each unit
// that reads it.
func checkSecretRefs(docs []Document, s *schema.Schemas, secrets SecretValues) error {
	files := map[string]SecretRef{}
	for _, d := range docs {
		for _, ref := range SecretRefs(d, s) {
			if _, ok := secrets[ref.Name]; !ok {
				return fmt.Errorf("%s names Secret %q, which the config does not hold", d, ref.Name)
			}
			if _, ok := secrets.Lookup(ref); !ok {
				return fmt.Errorf("%s names key %q of Secret %q, which it does not hold", d, ref.Key, ref.Name)
			}
			if err := CheckCredential(ref); err != nil {
				return fmt.Errorf("%s: %w", d, err)
			}
			// Secret a.b with key c and Secret a with key b.c share the file a.b.c.
			name := CredentialFile(ref)
			if other, ok := files[name]; ok && other != ref {
				return fmt.Errorf("%s: key %q of Secret %q and key %q of Secret %q would both be the credential file %s; rename one", d, ref.Key, ref.Name, other.Key, other.Name, name)
			}
			files[name] = ref
		}
	}
	return nil
}
