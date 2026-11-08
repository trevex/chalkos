package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/schema"
)

// Redacted are config documents with every secret value hidden, as Redact hides them: all that
// rules, renderers, validators and command facts read of a config, and all of it that leaves a
// node or reaches an image's checker. Only Redact and the readers of documents that Redact gave
// where they were written produce it, so a program that takes Redacted never sees a secret
// value. No document is redacted twice: Redact takes documents with their values, which Redacted
// does not convert to. The zero value holds no documents.
type Redacted struct {
	docs Documents
}

// Redact returns docs with every secret value hidden, so a node can show its config for comparison,
// and an image check it, without revealing a secret:
//   - each value of a Secret's spec.data as hideValue hides it: a string by its hex SHA-256 and
//     any other value by the zero value of its JSON type; a spec.data, or a spec, that is no
//     object, which no schema accepts, the same way as a whole;
//   - each data and stringData value of a Secret object another document embeds, at any depth, by
//     the hex SHA-256 of the value, of its JSON where it is no string. An embedded Secret object
//     is an object of apiVersion v1, or /v1, and kind Secret in any casing, such as a document
//     carries for another API to apply.
//
// Redact needs no schemas: it hides the same values whichever image's documents it is given, also
// those of an image the node does not run. Two sets redact to the same documents where they hold
// the same strings, and an embedded Secret object's to the same where they hold the same values of
// any type. docs are left as they are.
func Redact(docs []Document) Redacted {
	out := make(Documents, len(docs))
	for i, d := range docs {
		d.Object = d.hidden()
		out[i] = d
	}
	return Redacted{docs: out}
}

// RedactedObjects are the documents of objects that Redact gave where they were written, such as
// a baseline chalkd records or the input chalkd gives an image's checker. They stay as they are:
// RedactedObjects trusts its caller to pass objects that were redacted when they were written.
func RedactedObjects(objects []map[string]any) Redacted {
	docs := make(Documents, len(objects))
	for i, obj := range objects {
		docs[i] = FromObject(obj)
	}
	return Redacted{docs: docs}
}

// ParseRedacted reads a stream of documents that Redact gave where they were written, as a node
// serves its config, as Parse reads one. They stay as they are: ParseRedacted trusts its caller
// to pass documents that were redacted when they were written.
func ParseRedacted(r io.Reader) (Redacted, error) {
	docs, err := Parse(r)
	if err != nil {
		return Redacted{}, err
	}
	return Redacted{docs: docs}, nil
}

// Documents are the redacted documents, in their order. The slice is the caller's; the objects
// are shared.
func (r Redacted) Documents() Documents {
	return slices.Clone(r.docs)
}

// Len is the number of documents.
func (r Redacted) Len() int { return len(r.docs) }

// Objects are the documents' objects, in their order, as RedactedObjects reads them back.
func (r Redacted) Objects() []map[string]any {
	out := make([]map[string]any, len(r.docs))
	for i, d := range r.docs {
		out[i] = d.Object
	}
	return out
}

// Defaulted is r with the defaults schemas give filled in, as the rules of the image whose
// schemas they are compare a config with it. The documents another image delivered or translated
// mean to this image what its own defaults make of them: a field the delivery left out takes
// this image's default, whatever default the other image gave it. A document of a kind schemas does not accept is
// left out, as no rule of this image reads its kind. Documents with defaults already keep them.
// The defaults are the schemas' own, so the documents stay redacted.
func (r Redacted) Defaulted(schemas *schema.Schemas) Redacted {
	var out Documents
	for _, d := range r.docs {
		filled, err := schemas.Default(d.Object)
		if err != nil {
			continue
		}
		d.Object = filled
		out = append(out, d)
	}
	return Redacted{docs: out}
}

// TranslateRedacted is Translate of redacted documents, which it accepts as it accepts the
// documents with their values: the checks of secret references need the Secrets' keys alone, and
// a hidden value keeps the JSON type of its value for the schemas. It returns the set's
// documents, with their defaults, which stay redacted, since the defaults are the schemas' own.
func TranslateRedacted(name string, docs Redacted, schemas *schema.Schemas, t Target) (Redacted, identity.Identity, error) {
	set, id, err := Translate(name, docs.docs, schemas, t)
	if err != nil {
		return Redacted{}, identity.Identity{}, err
	}
	return Redacted{docs: set.Documents()}, id, nil
}

// hidden is the document's object with its secret values hidden, as Redact hides them. The
// document is left as it is.
func (d Document) hidden() map[string]any {
	if d.IsSecret() {
		return hideSecret(d.Object)
	}
	if obj, changed := hideEmbedded(d.Object); changed {
		return obj.(map[string]any)
	}
	return d.Object
}

// hideSecret returns a Secret's object with the values of spec.data hidden by hideValue, and
// spec.data or spec hidden whole where it is no object.
func hideSecret(obj map[string]any) map[string]any {
	out := maps.Clone(obj)
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		if v, present := obj["spec"]; present {
			out["spec"] = hideValue(v)
		}
		return out
	}
	spec = maps.Clone(spec)
	switch data := spec["data"].(type) {
	case nil:
	case map[string]any:
		spec["data"] = hideValues(data)
	default:
		spec["data"] = hideValue(data)
	}
	out["spec"] = spec
	return out
}

// secretFields are the fields of an embedded Secret object that hold its values.
var secretFields = []string{"data", "stringData"}

// hideEmbedded returns v with the values of each Secret object in it hashed, and whether it hid
// any; v itself is left as it is, and a part of it without a Secret object is shared, not copied.
// The values are hashed whatever their type, so a change of any value changes the hash of the
// document's kind, and a data that is no object is hashed whole.
func hideEmbedded(v any) (any, bool) {
	switch v := v.(type) {
	case map[string]any:
		var out map[string]any
		secret := embeddedSecret(v)
		for k, x := range v {
			var hidden any
			changed := false
			switch {
			case secret && slices.Contains(secretFields, k):
				switch data := x.(type) {
				case nil:
				case map[string]any:
					hidden, changed = hashValues(data), true
				default:
					hidden, changed = hashValue(data), true
				}
			default:
				hidden, changed = hideEmbedded(x)
			}
			if changed {
				if out == nil {
					out = maps.Clone(v)
				}
				out[k] = hidden
			}
		}
		if out == nil {
			return v, false
		}
		return out, true
	case []any:
		var out []any
		for i, x := range v {
			if hidden, changed := hideEmbedded(x); changed {
				if out == nil {
					out = slices.Clone(v)
				}
				out[i] = hidden
			}
		}
		if out == nil {
			return v, false
		}
		return out, true
	}
	return v, false
}

// embeddedSecret tells whether obj is a Secret object a document embeds: of the core group's
// version v1 and kind Secret. The apiVersion is v1 or /v1, both of which Kubernetes parses as the
// core group's v1. The kind is compared in any casing: what an API server would refuse is still
// not to be shown.
func embeddedSecret(obj map[string]any) bool {
	apiVersion, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)
	return strings.TrimPrefix(apiVersion, "/") == "v1" && strings.EqualFold(kind, "Secret")
}

// SecretField tells whether the field of d at path, keys and list indexes in decimal, holds values
// Redact replaces, or is one.
func SecretField(d Document, path []string) bool {
	if d.IsSecret() {
		return len(path) >= 2 && path[0] == "spec" && path[1] == "data"
	}
	var v any = d.Object
	for _, p := range path {
		switch n := v.(type) {
		case map[string]any:
			if embeddedSecret(n) && slices.Contains(secretFields, p) {
				return true
			}
			v = n[p]
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(n) {
				return false
			}
			v = n[i]
		default:
			return false
		}
	}
	return false
}

// hashValues returns data with each value replaced by hashValue of it, as Redact hides the values
// of an embedded Secret object.
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

// hideValue hides a value of a Secret as Redact does: a string by its hex SHA-256, and any other
// value by the zero value of its JSON type, such as 0 for a number, also one Go built as an int
// or a float64 rather than parsed as a json.Number. A value that is no
// string keeps its type, so a schema that refuses it refuses the hidden document with the same
// message, as a schema's message of a value of the wrong type names the type alone. Secret values
// are strings, so a document that holds another passes no check, and no delivery is named by
// the zero values its hash then holds.
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
