// Package schema reads the config contract of a chalkos image directory: metadata.json, which
// names the image and the config kinds it accepts, and schemas/, one JSON Schema per kind. It
// validates config documents against those schemas.
package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// metadataVersion is the version of metadata.json this package reads.
const metadataVersion = 0

// Metadata is an image directory's metadata.json.
type Metadata struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	Version       string `json:"version"`
	// Platform is empty for the installer, which serves every platform.
	Platform string `json:"platform"`
	// RootHash is the store's dm-verity root hash, in hex; it identifies the image's content.
	RootHash string `json:"rootHash"`
	Kinds    []Kind `json:"kinds"`
}

// Kind is a config kind an image accepts.
type Kind struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// Multiple tells whether a node's config may hold more than one document of the kind.
	Multiple bool `json:"multiple"`
}

// Image is the config contract of one image directory.
type Image struct {
	Metadata Metadata
	schemas  map[Kind]*jsonschema.Schema
}

// Load reads metadata.json and compiles the schema of every kind it lists.
func Load(dir string) (*Image, error) {
	b, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		return nil, err
	}
	var md Metadata
	if err := json.Unmarshal(b, &md); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, "metadata.json"), err)
	}
	if md.SchemaVersion != metadataVersion {
		return nil, fmt.Errorf("%s: metadata schemaVersion %d is not supported; this chalkos reads version %d", dir, md.SchemaVersion, metadataVersion)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	img := &Image{Metadata: md, schemas: map[Kind]*jsonschema.Schema{}}
	for _, k := range md.Kinds {
		group, version, ok := strings.Cut(k.APIVersion, "/")
		if !ok {
			return nil, fmt.Errorf("%s: apiVersion %q of kind %s has no group", dir, k.APIVersion, k.Kind)
		}
		path := filepath.Join(abs, "schemas", group+"_"+version+"_"+k.Kind+".json")
		s, err := c.Compile(path)
		if err != nil {
			return nil, fmt.Errorf("schema of %s %s: %w", k.APIVersion, k.Kind, err)
		}
		img.schemas[key(k.APIVersion, k.Kind)] = s
	}
	return img, nil
}

// The lookup key ignores Multiple, which the schema does not depend on.
func key(apiVersion, kind string) Kind {
	return Kind{APIVersion: apiVersion, Kind: kind}
}

// Validate checks a document, decoded by jsonschema.UnmarshalJSON, against the schema of its
// apiVersion and kind.
func (i *Image) Validate(doc any) error {
	m, ok := doc.(map[string]any)
	if !ok {
		return errors.New("a config document must be an object")
	}
	apiVersion, _ := m["apiVersion"].(string)
	kind, _ := m["kind"].(string)
	s, ok := i.schemas[key(apiVersion, kind)]
	if !ok {
		return fmt.Errorf("image %s %s does not accept kind %q of apiVersion %q", i.Metadata.ID, i.Metadata.Version, kind, apiVersion)
	}
	if err := s.Validate(doc); err != nil {
		name := ""
		if md, ok := m["metadata"].(map[string]any); ok {
			name, _ = md["name"].(string)
		}
		return fmt.Errorf("%s %q: %w", kind, name, err)
	}
	return nil
}
