package config

import (
	"encoding/json"
	"fmt"

	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/schema"
)

// Translator translates the set into what a layer of the image adds to the identity id of the node
// called name, and refuses a set the layer's services could not run with. Translate runs it once
// the identity holds what every image gives the node.
type Translator func(s *Set, name string, id *identity.Identity) error

// Target is the image a set is translated for.
type Target struct {
	// Platform is the image's, as its os-release names it.
	Platform string
	// Translators are the translations of the image's layers, which run in this order; none on an
	// image whose layers translate nothing.
	Translators []Translator
}

// Translate checks docs as the set of the node called name and translates them into its identity
// on the image t describes. It checks the set rules, each document against its schema, the
// Secrets the secretRefs name and the Registry and Mirror documents, translates what every image
// gives the node, and then runs the image's translators, which check the documents of their
// layers. chalkctl and chalkd both translate with it, so they accept the same configs.
func Translate(name string, docs []Document, schemas *schema.Schemas, t Target) (*Set, identity.Identity, error) {
	set, err := NewSet(docs, schemas)
	if err != nil {
		return nil, identity.Identity{}, err
	}
	if err := set.CheckRegistries(); err != nil {
		return nil, identity.Identity{}, err
	}
	id, err := set.Identity(name, t.Platform)
	if err != nil {
		return nil, identity.Identity{}, err
	}
	for _, translate := range t.Translators {
		if err := translate(set, name, &id); err != nil {
			return nil, identity.Identity{}, err
		}
	}
	return set, id, nil
}

// Identity translates the set into the identity of the node called name on an image of the
// platform: what every image gives the node, its hostname, storage and network. Extension kinds
// stay in the set, and the image's translators add what its layers give.
//
// name is the node's name in the inventory, which its node certificate carries. Whatever the
// documents call the node, the hostname defaults to name unless a Host gives one, and the storage
// seeds derive from name.
func (s *Set) Identity(name, platform string) (identity.Identity, error) {
	if name == "" {
		return identity.Identity{}, fmt.Errorf("the node has no name to translate its config for")
	}
	sec, err := s.storage(name)
	if err != nil {
		return identity.Identity{}, err
	}
	var networkDocs []Document
	for _, kind := range []string{"Link", "NetDev", "Network"} {
		networkDocs = append(networkDocs, s.docs.Of(NetworkdAPI, kind)...)
	}
	id := identity.Identity{
		Hostname: name,
		Platform: platform,
		Network:  NetworkMap(networkDocs),
		Storage:  sec,
		Layers:   map[string]json.RawMessage{},
	}
	if hostDoc, ok := s.One("Host"); ok {
		var host hostSpec
		if err := hostDoc.DecodeSpec(&host); err != nil {
			return identity.Identity{}, err
		}
		if host.Hostname != nil {
			id.Hostname = *host.Hostname
		}
	}
	return id, nil
}
