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
	// refs are each document's references, by its ID, every one of which resolves within docs.
	refs map[ID][]Ref
}

// kindName is a kind by its apiVersion and name.
type kindName struct{ apiVersion, kind string }

// singletons are the kinds a node's set holds exactly once.
var singletons = []kindName{
	{StorageAPI, "SystemDisk"},
}

// NewSet checks docs as a node's config against the image's schemas: each document is valid, its
// kind is one the image accepts, kinds that are not multiple appear once, names are unique per
// kind, the SystemDisk is present, and every reference names a document of the set, a key
// reference a key its Secret holds and a credential file of its own, and no value repeats that
// the schemas declare unique. A layer's Translator checks the documents its layer requires.
func NewSet(docs []Document, s *schema.Schemas) (*Set, error) {
	return newSet(docs, s, singletons)
}

// newSet is NewSet with the kinds that must be present given, instead of the SystemDisk.
func newSet(docs []Document, s *schema.Schemas, required []kindName) (*Set, error) {
	multiple := map[string]bool{}
	for _, k := range s.Kinds() {
		multiple[k.APIVersion+" "+k.Kind] = k.Multiple
	}
	set := &Set{schemas: s}
	count := map[string]int{}
	names := map[string]bool{}
	for _, d := range docs {
		filled, err := s.Fill(d.Object)
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
	for _, k := range required {
		if count[k.apiVersion+" "+k.kind] == 0 {
			return nil, fmt.Errorf("no %s: a node's config holds one %s", k.kind, k.kind)
		}
	}
	SortDocuments(set.docs)
	refs, err := checkRefs(set.docs, s)
	if err != nil {
		return nil, err
	}
	set.refs = refs
	if err := checkUnique(set.docs, s); err != nil {
		return nil, err
	}
	return set, nil
}

// Refs returns the references of the set's documents, by the ID of the document that holds them;
// a document without references has no entry. Every reference resolves within the set. The map
// and its slices are the set's own, which callers read and never change.
func (s *Set) Refs() map[ID][]Ref { return s.refs }

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
