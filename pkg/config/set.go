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

// NewSet checks docs as a node's whole config against the image's schemas. A layer's Translator
// checks the documents its layer requires.
func NewSet(docs []Document, s *schema.Schemas) (*Set, error) {
	return newSet(docs, s, true)
}

// newSet checks docs as NewSet does, and requires a SystemDisk only where requireSystemDisk is set.
func newSet(docs []Document, s *schema.Schemas, requireSystemDisk bool) (*Set, error) {
	multiple := map[ID]bool{}
	for _, k := range s.Kinds() {
		multiple[ID{APIVersion: k.APIVersion, Kind: k.Kind}] = k.Multiple
	}
	set := &Set{schemas: s}
	count := map[ID]int{}
	names := map[ID]bool{}
	for _, d := range docs {
		filled, err := s.Fill(d.Object)
		if err != nil {
			return nil, err
		}
		d.Object = filled
		kind := ID{APIVersion: d.APIVersion, Kind: d.Kind}
		count[kind]++
		if count[kind] > 1 && !multiple[kind] {
			return nil, fmt.Errorf("more than one %s: the image accepts one %s per node", d.Kind, d.Kind)
		}
		if names[d.ID()] {
			return nil, fmt.Errorf("two %s documents named %q", d.Kind, d.Name)
		}
		names[d.ID()] = true
		set.docs = append(set.docs, d)
	}
	if requireSystemDisk && count[ID{APIVersion: StorageAPI, Kind: "SystemDisk"}] == 0 {
		return nil, fmt.Errorf("no SystemDisk: a node's config holds one SystemDisk")
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

// Refs returns each document's references by its ID, every one resolving within the set. The map
// is the set's own, so callers must not change it.
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

// CheckRegistries refuses a set whose Registry and Mirror documents RegistriesOf refuses.
func (s *Set) CheckRegistries() error {
	_, err := RegistriesOf(s.docs, nil)
	return err
}
