package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevex/chalkos/pkg/jsonfile"
)

// Document is one config document.
type Document struct {
	APIVersion string
	Kind       string
	Name       string
	// Object is the whole document with no scalar reinterpreted: strings, timestamps and binary
	// values keep their text, and numbers are exact json.Numbers, integers in plain digits.
	Object map[string]any
}

// Documents are a node's config documents.
type Documents []Document

// Of returns the documents of a kind, in their order.
func (docs Documents) Of(apiVersion, kind string) Documents {
	var out Documents
	for _, d := range docs {
		if d.APIVersion == apiVersion && d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

// Single returns the document of a kind when docs hold exactly one.
func (docs Documents) Single(apiVersion, kind string) (Document, bool) {
	if found := docs.Of(apiVersion, kind); len(found) == 1 {
		return found[0], true
	}
	return Document{}, false
}

// jsonNumber matches a number literal as JSON writes it.
var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// mergeTag is the short form of the YAML tag a plain "<<" scalar resolves to.
const mergeTag = "!!merge"

// Parse reads the documents of a YAML stream, skipping empty ones. Every scalar keeps its exact
// value, so no date becomes a time.Time and no number loses precision. Mapping keys must be
// strings, and merge keys (<<) are refused.
func Parse(r io.Reader) ([]Document, error) {
	dec := yaml.NewDecoder(r)
	var docs []Document
	for i := 1; ; i++ {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		root := &doc
		if doc.Kind == yaml.DocumentNode {
			if len(doc.Content) == 0 {
				continue
			}
			root = doc.Content[0]
		}
		v, err := nodeValue(i, root)
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("document %d is not an object", i)
		}
		docs = append(docs, FromObject(m))
	}
}

// nodeValue converts a parsed YAML node into the same shapes config documents use elsewhere:
// map[string]any, []any, string, bool, json.Number, and nil, with aliases resolved.
func nodeValue(doc int, n *yaml.Node) (any, error) {
	for n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return scalarValue(doc, n)
	case yaml.MappingNode:
		return mappingValue(doc, n)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, item := range n.Content {
			v, err := nodeValue(doc, item)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("document %d, line %d: cannot read this node", doc, n.Line)
	}
}

// scalarValue resolves a scalar node's YAML tag and returns the value at that tag: nil, bool,
// json.Number for !!int and !!float, in JSON's spelling and exact, and the node's own text for
// !!str, !!timestamp and !!binary, which must not turn into time.Time or decoded bytes here.
func scalarValue(doc int, n *yaml.Node) (any, error) {
	switch n.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return nil, fmt.Errorf("document %d, line %d: %w", doc, n.Line, err)
		}
		return b, nil
	case "!!int":
		// YAML also writes integers in hex, octal and binary and with _ separators, none of which
		// JSON reads; the decimal text of the same integer, of any size, is.
		var i big.Int
		if _, ok := i.SetString(strings.ReplaceAll(n.Value, "_", ""), 0); !ok {
			return nil, fmt.Errorf("document %d, line %d: %s is not an integer", doc, n.Line, n.Value)
		}
		return json.Number(i.String()), nil
	case "!!float":
		var f float64
		if err := n.Decode(&f); err != nil {
			return nil, fmt.Errorf("document %d, line %d: %w", doc, n.Line, err)
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("document %d, line %d: %s is not a finite number", doc, n.Line, n.Value)
		}
		// A JSON number literal stays as written, so no digit of it is rounded away; any other
		// YAML spelling, such as +1.5 or .5, becomes the shortest JSON form of the same float.
		if jsonNumber.MatchString(n.Value) {
			return json.Number(n.Value), nil
		}
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
	case "!!str", "!!timestamp", "!!binary":
		return n.Value, nil
	case mergeTag:
		return nil, fmt.Errorf("document %d, line %d: merge keys (<<) are not supported", doc, n.Line)
	default:
		return nil, fmt.Errorf("document %d, line %d: cannot read a value tagged %s", doc, n.Line, n.ShortTag())
	}
}

// mappingValue converts a mapping node, requiring every key to be a string so no number, date or
// other reinterpreted scalar silently becomes a field name.
func mappingValue(doc int, n *yaml.Node) (map[string]any, error) {
	out := make(map[string]any, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		for key.Kind == yaml.AliasNode {
			key = key.Alias
		}
		if key.Kind == yaml.ScalarNode && key.ShortTag() == mergeTag {
			return nil, fmt.Errorf("document %d, line %d: merge keys (<<) are not supported", doc, key.Line)
		}
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return nil, fmt.Errorf("document %d, line %d: key %s is not a string; quote it", doc, key.Line, keyText(key))
		}
		val, err := nodeValue(doc, n.Content[i+1])
		if err != nil {
			return nil, err
		}
		out[key.Value] = val
	}
	return out, nil
}

// keyText renders a mapping key for an error message: its text when it is a scalar, or its tag
// otherwise, since a mapping or sequence key has no single value to show.
func keyText(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	return n.ShortTag()
}

// DecodeSpec decodes the document's spec into v. Numbers in untyped fields stay json.Number,
// exact as written, such as those of a field that holds free-form JSON.
func (d Document) DecodeSpec(v any) error {
	b, err := json.Marshal(d.Object["spec"])
	if err != nil {
		return err
	}
	if err := jsonfile.DecodeExact(b, v); err != nil {
		return fmt.Errorf("%s %q: %w", d.Kind, d.Name, err)
	}
	return nil
}

func (d Document) String() string { return fmt.Sprintf("%s %q", d.Kind, d.Name) }

// FromObject is the document an object decoded from YAML or JSON holds. It rewrites integral
// numbers in m into plain digits; every document passes through here, so every reader of it sees
// 100 where the config wrote 100.0.
func FromObject(m map[string]any) Document {
	integral(m)
	d := Document{Object: m}
	d.APIVersion, _ = m["apiVersion"].(string)
	d.Kind, _ = m["kind"].(string)
	if md, ok := m["metadata"].(map[string]any); ok {
		d.Name, _ = md["name"].(string)
	}
	return d
}

// IsSecret tells whether d is a Secret, whose values reach only the units whose kinds reference
// them.
func (d Document) IsSecret() bool { return d.APIVersion == CoreAPI && d.Kind == "Secret" }

// SortDocuments orders docs by apiVersion, kind and name.
func SortDocuments(docs []Document) {
	slices.SortStableFunc(docs, func(a, b Document) int {
		return cmp.Or(cmp.Compare(a.APIVersion, b.APIVersion), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
}
