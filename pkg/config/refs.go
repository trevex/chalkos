package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
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
	if schemas == nil {
		return nil
	}
	raw, ok := schemas.Raw(d.APIVersion, d.Kind)
	if !ok {
		return nil
	}
	sch, _ := raw.(map[string]any)
	defs, _ := sch["$defs"].(map[string]any)
	var refs []Ref
	walkRefs(defs, sch, d.Object, "", func(r Ref) { refs = append(refs, r) })
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

// walkRefs walks sch, a raw schema node, alongside the value v it describes at path, and calls f
// for every node marked with x-chalkos-ref: a string names a document, an object with key true
// names a key of a Secret as {secretRef: {name, key}}. It follows $ref into defs, properties into
// object fields, items into array elements, additionalProperties into a map's values, and anyOf
// into the branch whose declared type matches the value's own. A key or index the schema says
// nothing about is left alone, however it is shaped.
func walkRefs(defs map[string]any, sch map[string]any, v any, path string, f func(Ref)) {
	sch = followRef(defs, sch)
	if m, ok := markerOf(sch); ok {
		target := ID{APIVersion: m.apiVersion, Kind: m.kind}
		if m.key {
			obj, _ := v.(map[string]any)
			ref, _ := obj["secretRef"].(map[string]any)
			name, okName := ref["name"].(string)
			key, okKey := ref["key"].(string)
			if okName && okKey {
				target.Name = name
				f(Ref{Path: path, Target: target, Key: key})
			}
			return
		}
		if name, ok := v.(string); ok && !slices.Contains(m.reserved, name) {
			target.Name = name
			f(Ref{Path: path, Target: target})
		}
		return
	}
	if branches, ok := sch["anyOf"].([]any); ok {
		branch, _ := pickBranch(defs, branches, v).(map[string]any)
		walkRefs(defs, branch, v, path, f)
		return
	}
	switch val := v.(type) {
	case map[string]any:
		props, _ := sch["properties"].(map[string]any)
		extra, _ := sch["additionalProperties"].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(val)) {
			prop, _ := props[name].(map[string]any)
			if prop == nil {
				prop = extra
			}
			if prop == nil {
				continue
			}
			walkRefs(defs, prop, val[name], fieldPath(path, name), f)
		}
	case []any:
		items, _ := sch["items"].(map[string]any)
		if items == nil {
			return
		}
		for i, item := range val {
			walkRefs(defs, items, item, path+"["+strconv.Itoa(i)+"]", f)
		}
	}
}

// plainKey is a field or map key a path writes after a dot; any other goes in quoted brackets.
var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// fieldPath is path with the field or map key name appended.
func fieldPath(path, name string) string {
	switch {
	case !plainKey.MatchString(name):
		return path + "[" + strconv.Quote(name) + "]"
	case path == "":
		return name
	}
	return path + "." + name
}

// followRef resolves a local "$ref": "#/$defs/Name", as the converter emits for a repeated
// section, to the definition it names.
func followRef(defs map[string]any, sch map[string]any) map[string]any {
	for {
		ref, ok := sch["$ref"].(string)
		if !ok {
			return sch
		}
		name, ok := strings.CutPrefix(ref, "#/$defs/")
		next, _ := defs[name].(map[string]any)
		if !ok || next == nil {
			return sch
		}
		sch = next
	}
}

// pickBranch returns the anyOf branch whose declared type matches v's JSON type, or the first
// branch when none does. It is the schema package's own branchFor, read on a raw schema document
// instead of a compiled one.
func pickBranch(defs map[string]any, branches []any, v any) any {
	for _, b := range branches {
		bm, _ := b.(map[string]any)
		bm = followRef(defs, bm)
		for _, t := range jsonTypes(v) {
			if rawHasType(bm, t) {
				return b
			}
		}
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return nil
}

// jsonTypes are the JSON Schema types that describe v, a value as Parse reads it.
func jsonTypes(v any) []string {
	switch v.(type) {
	case nil:
		return []string{"null"}
	case []any:
		return []string{"array"}
	case map[string]any:
		return []string{"object"}
	case string:
		return []string{"string"}
	case bool:
		return []string{"boolean"}
	}
	return []string{"number", "integer"}
}

// rawHasType tells whether a raw schema's "type" names jsonType, whether it is given as a single
// string or, as networkd's passthrough values declare it, a list of strings.
func rawHasType(sch map[string]any, jsonType string) bool {
	switch t := sch["type"].(type) {
	case string:
		return t == jsonType
	case []any:
		for _, v := range t {
			if s, _ := v.(string); s == jsonType {
				return true
			}
		}
	}
	return false
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
