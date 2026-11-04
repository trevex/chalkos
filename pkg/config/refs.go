package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/schema"
)

// ID names a document: no two documents of a set share one.
type ID struct {
	APIVersion, Kind, Name string
}

// String is the ID as references are reported: Kind/name.
func (id ID) String() string { return id.Kind + "/" + id.Name }

// ID is the document's ID.
func (d Document) ID() ID { return ID{APIVersion: d.APIVersion, Kind: d.Kind, Name: d.Name} }

// Ref is a reference of a document to another: a field its kind's schema marks with
// x-chalkos-ref, as the converter marks a lib.chalkos.types.ref or secretRef field.
type Ref struct {
	// Path is the field's path below the document, such as spec.rules[2].to.vrf.
	Path string
	// Target is the document the field names.
	Target ID
	// Key is the key of the Secret a key reference names; empty for a reference to a document.
	Key string
}

// Secret is the key reference r is, and whether it is one.
func (r Ref) Secret() (SecretRef, bool) {
	return SecretRef{Name: r.Target.Name, Key: r.Key}, r.Key != ""
}

// Refs returns the references of d that its kind's schema in schemas marks, in the order of the
// schema's fields by name and of list items by index; none for a kind schemas does not declare,
// or without schemas. A field that holds one of the names its reference reserves, such as the VRF
// default, references nothing.
func Refs(d Document, schemas *schema.Schemas) []Ref {
	var refs []Ref
	walkDocument(d, schemas, func(n node) bool {
		m, ok := markerOf(n.sch)
		if !ok {
			return true
		}
		target := ID{APIVersion: m.apiVersion, Kind: m.kind}
		if m.key {
			// A key reference is {secretRef: {name, key}}.
			obj, _ := n.v.(map[string]any)
			ref, _ := obj["secretRef"].(map[string]any)
			name, okName := ref["name"].(string)
			key, okKey := ref["key"].(string)
			if okName && okKey {
				target.Name = name
				refs = append(refs, Ref{Path: n.path, Target: target, Key: key})
			}
			return false
		}
		if name, ok := n.v.(string); ok && !slices.Contains(m.reserved, name) {
			target.Name = name
			refs = append(refs, Ref{Path: n.path, Target: target})
		}
		return false
	})
	return refs
}

// refMarker is a field's x-chalkos-ref: the apiVersion and kind of the documents it names, the
// names it reserves, and whether it names a key of a Secret.
type refMarker struct {
	apiVersion, kind string
	reserved         []string
	key              bool
}

func markerOf(sch map[string]any) (refMarker, bool) {
	m, ok := sch["x-chalkos-ref"].(map[string]any)
	if !ok {
		return refMarker{}, false
	}
	var r refMarker
	r.apiVersion, _ = m["apiVersion"].(string)
	r.kind, _ = m["kind"].(string)
	r.key, _ = m["key"].(bool)
	reserved, _ := m["reserved"].([]any)
	for _, n := range reserved {
		if s, ok := n.(string); ok {
			r.reserved = append(r.reserved, s)
		}
	}
	return r, true
}

// checkRefs returns the references of each of docs, by the document's ID, and refuses those that
// do not resolve: a reference to a document docs lack or a key its Secret lacks, and a key
// reference that cannot become a credential file of its own, as the node writes one for each unit
// that reads it. It reports every such reference at once, one line each, in the order of docs and
// of each document's references.
func checkRefs(docs Documents, s *schema.Schemas) (map[ID][]Ref, error) {
	have := map[ID]bool{}
	for _, d := range docs {
		have[d.ID()] = true
	}
	secrets := docs.Secrets()
	out := map[ID][]Ref{}
	files := map[string]SecretRef{}
	var lines []string
	for _, d := range docs {
		refs := Refs(d, s)
		if len(refs) > 0 {
			out[d.ID()] = refs
		}
		for _, r := range refs {
			at := d.ID().String() + ": " + r.Path + ": "
			if !have[r.Target] {
				lines = append(lines, fmt.Sprintf("%s%s %q does not exist", at, r.Target.Kind, r.Target.Name))
				continue
			}
			ref, ok := r.Secret()
			if !ok {
				continue
			}
			if _, ok := secrets.Lookup(ref); !ok {
				lines = append(lines, fmt.Sprintf("%sSecret %q has no key %q", at, ref.Name, ref.Key))
				continue
			}
			if err := CheckCredential(ref); err != nil {
				lines = append(lines, at+err.Error())
				continue
			}
			// Secret a.b with key c and Secret a with key b.c share the file a.b.c.
			name := CredentialFile(ref)
			if other, ok := files[name]; ok && other != ref {
				lines = append(lines, fmt.Sprintf("%skey %q of Secret %q and key %q of Secret %q would both be the credential file %s; rename one", at, ref.Key, ref.Name, other.Key, other.Name, name))
				continue
			}
			files[name] = ref
		}
	}
	if len(lines) > 0 {
		return nil, errors.New(strings.Join(lines, "\n"))
	}
	return out, nil
}
