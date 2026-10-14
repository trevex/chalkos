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
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
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
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	img := &Image{Metadata: md, schemas: map[Kind]*jsonschema.Schema{}}
	for _, k := range md.Kinds {
		group, version, ok := strings.Cut(k.APIVersion, "/")
		if !ok {
			return nil, fmt.Errorf("%s: apiVersion %q of kind %s has no group", dir, k.APIVersion, k.Kind)
		}
		name := group + "_" + version + "_" + k.Kind + ".json"
		s, err := compileSchema(c, filepath.Join(dir, "schemas", name), name)
		if err != nil {
			return nil, fmt.Errorf("schema of %s %s: %w", k.APIVersion, k.Kind, err)
		}
		img.schemas[key(k.APIVersion, k.Kind)] = s
	}
	return img, nil
}

// compileSchema reads and compiles the schema at path. Its identity inside the compiler, needed
// to resolve its internal $refs, is id, a URL built from the schema's file name alone: jsonschema
// otherwise treats the filesystem path itself as a URL, which misreads or rejects a path whose
// directory contains '%', '#' or '?'. Errors still name the real path.
func compileSchema(c *jsonschema.Compiler, path, name string) (*jsonschema.Schema, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	id := "chalkos:///schemas/" + name
	if err := c.AddResource(id, doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// The lookup key ignores Multiple, which the schema does not depend on.
func key(apiVersion, kind string) Kind {
	return Kind{APIVersion: apiVersion, Kind: kind}
}

// Validate checks a document — decoded JSON such as jsonschema.UnmarshalJSON returns, which keeps
// numbers exact — against the schema of its apiVersion and kind.
func (i *Image) Validate(doc any) error {
	m, ok := doc.(map[string]any)
	if !ok {
		return errors.New("a config document must be an object")
	}
	apiVersion, _ := m["apiVersion"].(string)
	docKind, _ := m["kind"].(string)
	s, ok := i.schemas[key(apiVersion, docKind)]
	if !ok {
		return fmt.Errorf("image %s %s does not accept kind %q of apiVersion %q", i.Metadata.ID, i.Metadata.Version, docKind, apiVersion)
	}
	if err := s.Validate(doc); err != nil {
		name := ""
		if md, ok := m["metadata"].(map[string]any); ok {
			name, _ = md["name"].(string)
		}
		return fmt.Errorf("%s %q: %s", docKind, name, explain(err))
	}
	return nil
}

// explain renders a schema validation error as its leaf causes alone, one per line, dropping the
// "jsonschema validation failed with ..." header the library puts on every error, and the
// allOf/anyOf/$ref nodes that group the causes without saying anything of their own.
func explain(err error) string {
	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err.Error()
	}
	var lines []string
	for _, leaf := range leafCauses(verr) {
		lines = append(lines, "- "+leaf.Error())
	}
	return strings.Join(lines, "\n")
}

// leafCauses walks e's causes down to the assertions that actually failed. For an anyOf — the
// shape networkd's "one or many" sections and chalkos's optional secretRef both use — a branch
// that fails only because the instance has the wrong JSON type is noise once another branch fails
// for a more specific reason, so it is dropped; if every branch merely disagrees on type, all of
// them are kept, since none says more than that.
func leafCauses(e *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(e.Causes) == 0 {
		return []*jsonschema.ValidationError{e}
	}
	causes := e.Causes
	if _, ok := e.ErrorKind.(*kind.AnyOf); ok {
		if better := dropTypeMismatches(causes); better != nil {
			causes = better
		}
	}
	var leaves []*jsonschema.ValidationError
	for _, c := range causes {
		leaves = append(leaves, leafCauses(c)...)
	}
	return leaves
}

// dropTypeMismatches returns the branches of an anyOf that are not a bare "wrong JSON type"
// failure, or nil when that is none of the branches or all of them, telling the caller to keep
// every branch as it is.
func dropTypeMismatches(causes []*jsonschema.ValidationError) []*jsonschema.ValidationError {
	var kept []*jsonschema.ValidationError
	for _, c := range causes {
		if isBareTypeMismatch(c) {
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 || len(kept) == len(causes) {
		return nil
	}
	return kept
}

// isBareTypeMismatch reports whether e says only that the instance has the wrong JSON type. A
// schema reached through $ref, as networkd's section definitions are, wraps that same verdict in
// a kind.Reference, so the check follows a single-cause chain of those too.
func isBareTypeMismatch(e *jsonschema.ValidationError) bool {
	if _, ok := e.ErrorKind.(*kind.Type); ok {
		return len(e.Causes) == 0
	}
	if _, ok := e.ErrorKind.(*kind.Reference); ok && len(e.Causes) == 1 {
		return isBareTypeMismatch(e.Causes[0])
	}
	return false
}
