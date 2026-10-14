package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// Document is one config document.
type Document struct {
	APIVersion string
	Kind       string
	Name       string
	// Object is the whole document as decoded JSON, with numbers kept exact.
	Object map[string]any
}

// Parse reads the documents of a YAML stream, separated by ---. Empty documents are skipped.
func Parse(r io.Reader) ([]Document, error) {
	dec := yaml.NewDecoder(r)
	var docs []Document
	for i := 1; ; i++ {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		if v == nil {
			continue
		}
		// Through JSON, so documents hold the same types whether they came from YAML or JSON.
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		obj, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		m, ok := obj.(map[string]any)
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
