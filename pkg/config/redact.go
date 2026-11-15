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

// Redacted are config documents with every secret value hidden. Only Redact and the readers of
// documents Redact wrote produce it, so a program that takes Redacted never sees a secret value.
// No document is redacted twice: Redact takes documents with their values, which Redacted does not
// convert to. The zero value holds no documents.
type Redacted struct {
	docs Documents
}

// Redact returns docs with secret values hidden: a Secret's spec.data values by hideValue, and
// the data and stringData of embedded v1 Secret objects by their SHA-256. It needs no schemas, so
// it hides the same values for any image.
func Redact(docs []Document) Redacted {
	out := make(Documents, len(docs))
	for i, d := range docs {
		d.Object = d.hidden()
		out[i] = d
	}
	return Redacted{docs: out}
}

// RedactedObjects wraps objects that Redact wrote earlier. It trusts its caller that they were
// redacted when written.
func RedactedObjects(objects []map[string]any) Redacted {
	docs := make(Documents, len(objects))
	for i, obj := range objects {
		docs[i] = FromObject(obj)
	}
	return Redacted{docs: docs}
}

// ParseRedacted reads a stream of documents that Redact wrote earlier, such as a node's served
// config. It trusts its caller that they were redacted when written.
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

// Objects are the documents' objects, in their order.
func (r Redacted) Objects() []map[string]any {
	out := make([]map[string]any, len(r.docs))
	for i, d := range r.docs {
		out[i] = d.Object
	}
	return out
}

// Defaulted is r with the schemas' defaults filled in, so documents another image delivered mean
// what this image's defaults make of them. A document of a kind schemas does not accept is left
// out, as no rule of this image reads it. The defaults are the schemas' own, so the documents stay
// redacted.
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

// TranslateRedacted is Translate of redacted documents. Secret reference checks need the keys
// alone and a hidden value keeps its JSON type, so redacted documents pass as the originals would.
func TranslateRedacted(name string, docs Redacted, schemas *schema.Schemas, t Target) (Redacted, identity.Identity, error) {
	set, id, err := Translate(name, docs.docs, schemas, t)
	if err != nil {
		return Redacted{}, identity.Identity{}, err
	}
	return Redacted{docs: set.Documents()}, id, nil
}

// CheckRedacted checks docs as Translate does, short of what a whole node needs (a SystemDisk and
// the translation), and returns them with their defaults. Image tests use it.
func CheckRedacted(docs Redacted, schemas *schema.Schemas) (Redacted, error) {
	set, err := checkSet(docs.docs, schemas, false)
	if err != nil {
		return Redacted{}, err
	}
	return Redacted{docs: set.Documents()}, nil
}

// hidden is the document's object with its secret values hidden.
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
	if data := spec["data"]; data != nil {
		spec["data"] = hideData(data, hideValue)
	}
	out["spec"] = spec
	return out
}

// secretFields are the fields of an embedded Secret object that hold its values.
var secretFields = []string{"data", "stringData"}

// hideEmbedded returns v with the values of each embedded Secret object hashed, and whether it hid
// any. Parts without a Secret object are shared. Values of any type are hashed, so any change
// changes the document's hash.
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
				if x != nil {
					hidden, changed = hideData(x, hashValue), true
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

// embeddedSecret tells whether obj is a core v1 Secret. Kubernetes parses both v1 and /v1 as the
// core group; the kind matches in any casing, as a refused Secret is still not to be shown.
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

// hideData hides the data of a Secret or an embedded Secret object: each value of an object by
// leaf, and data that is no object whole.
func hideData(data any, leaf func(any) any) any {
	m, ok := data.(map[string]any)
	if !ok {
		return leaf(data)
	}
	hidden := make(map[string]any, len(m))
	for k, v := range m {
		hidden[k] = leaf(v)
	}
	return hidden
}

// hashValue is the hex SHA-256 of v: of a string as it is, of any other value as its JSON.
func hashValue(v any) any {
	s, isString := v.(string)
	if !isString {
		b, _ := json.Marshal(v)
		s = string(b)
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hideValue hides a Secret value: a string by its SHA-256, anything else by the zero value of
// its JSON type, so a schema refuses the hidden document as it would the original.
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
