package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"

	"go.yaml.in/yaml/v3"
)

// Document is one config document.
type Document struct {
	APIVersion string
	Kind       string
	Name       string
	// Object is the whole document as YAML read it, with no scalar reinterpreted: strings,
	// timestamps and binary values stay exactly the text written, and every integer and float is
	// a json.Number holding that same text, so a value no decoder has rounded or reformatted.
	Object map[string]any
}

// mergeTag is the short form of the YAML tag a plain "<<" scalar resolves to.
const mergeTag = "!!merge"

// Parse reads the documents of a YAML stream, separated by ---. Empty documents are skipped.
// Every scalar keeps the text it was written with: Parse does not let yaml.v3's resolver turn an
// unquoted value such as a date into a time.Time, or a number into a float64 that cannot hold it
// exactly. A mapping key must be a plain or quoted string; any other key, and a merge key (<<),
// are errors naming the document and line.
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
		d := Document{Object: m}
		d.APIVersion, _ = m["apiVersion"].(string)
		d.Kind, _ = m["kind"].(string)
		if md, ok := m["metadata"].(map[string]any); ok {
			d.Name, _ = md["name"].(string)
		}
		docs = append(docs, d)
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
// json.Number for !!int and !!float, keeping their text exact, and the node's own text for
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
		return json.Number(n.Value), nil
	case "!!float":
		var f float64
		if err := n.Decode(&f); err != nil {
			return nil, fmt.Errorf("document %d, line %d: %w", doc, n.Line, err)
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("document %d, line %d: %s is not a finite number", doc, n.Line, n.Value)
		}
		return json.Number(n.Value), nil
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

// decodeSpec decodes the document's spec into v.
func (d Document) decodeSpec(v any) error {
	b, err := json.Marshal(d.Object["spec"])
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s %q: %w", d.Kind, d.Name, err)
	}
	return nil
}

func (d Document) String() string { return fmt.Sprintf("%s %q", d.Kind, d.Name) }
