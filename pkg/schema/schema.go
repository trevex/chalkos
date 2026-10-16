// Package schema reads the config contract of a chalkos image: metadata.json, which names the
// image and the config kinds it accepts, and a schemas directory, one JSON Schema per kind. Load
// reads a build directory, which has both. A running node ships only the schemas directory, as
// /etc/chalkos/schemas, with no metadata.json alongside it — metadata's rootHash is computed over
// the image itself — so chalkd reads the running image's contract with LoadSchemas instead, which
// finds each schema's kind from the schema's own "x-chalkos-kind". The package validates config
// documents against those schemas.
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
	// Kubernetes is the Kubernetes release and pod network the image is built with; nil on an
	// image without Kubernetes.
	Kubernetes *MetadataKubernetes `json:"kubernetes"`
	Kinds      []Kind              `json:"kinds"`
}

// Kind is a config kind an image accepts.
type Kind struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// Multiple tells whether a node's config may hold more than one document of the kind.
	Multiple bool `json:"multiple"`
}

// MetadataKubernetes is what metadata.json says of an image's Kubernetes: its release, such as
// 1.37.1, and its CNI provider, flannel or none.
type MetadataKubernetes struct {
	Version string `json:"version"`
	CNI     string `json:"cni"`
}

// Schemas is the compiled schema of every kind named by the *.json files of a schemas directory,
// such as an image's schemas/ in its build output or /etc/chalkos/schemas on a running node.
// LoadSchemas builds it from each file's "x-chalkos-kind" alone, so it needs no metadata.json.
type Schemas struct {
	// kinds is in the order LoadSchemas read the directory, which os.ReadDir sorts by name.
	kinds   []Kind
	schemas map[Kind]*jsonschema.Schema
	// raw holds each kind's schema document as its file holds it, for readers of annotations.
	raw map[Kind]any
}

// LoadSchemas compiles every *.json file in dir and builds the kind list from each file's
// "x-chalkos-kind": {"group","version","kind","multiple"}. A file's name must equal
// "<group>_<version>_<kind>.json" by its own declared identity, and no two files may declare the
// same kind; either failure names the offending file or kind.
func LoadSchemas(dir string) (*Schemas, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	ss := &Schemas{schemas: map[Kind]*jsonschema.Schema{}, raw: map[Kind]any{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name)
		doc, err := readDoc(path)
		if err != nil {
			return nil, err
		}
		k, err := kindOf(doc, path)
		if err != nil {
			return nil, err
		}
		if _, dup := ss.schemas[key(k.APIVersion, k.Kind)]; dup {
			return nil, fmt.Errorf("%s: duplicate kind %s %s", path, k.APIVersion, k.Kind)
		}
		if want := fileName(k); want != name {
			return nil, fmt.Errorf("%s: should be named %s for its kind %s %s", path, want, k.APIVersion, k.Kind)
		}
		s, err := compileSchema(c, doc, path, name)
		if err != nil {
			return nil, fmt.Errorf("schema of %s %s: %w", k.APIVersion, k.Kind, err)
		}
		ss.schemas[key(k.APIVersion, k.Kind)] = s
		ss.raw[key(k.APIVersion, k.Kind)] = doc
		ss.kinds = append(ss.kinds, k)
	}
	return ss, nil
}

// fileName is the name a schema of k must have, as modules/node/kinds/schema.nix also builds it.
func fileName(k Kind) string {
	group, version, _ := strings.Cut(k.APIVersion, "/")
	return group + "_" + version + "_" + k.Kind + ".json"
}

// readDoc reads and decodes the JSON document at path with jsonschema.UnmarshalJSON, which keeps
// numbers exact, the way compileSchema needs it and the way kindOf reads "x-chalkos-kind".
func readDoc(path string) (any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

// kindOf reads a schema document's "x-chalkos-kind" object, every schema's own record of the
// kind it validates.
func kindOf(doc any, path string) (Kind, error) {
	m, ok := doc.(map[string]any)
	if !ok {
		return Kind{}, fmt.Errorf("%s: not a JSON object", path)
	}
	xk, ok := m["x-chalkos-kind"].(map[string]any)
	if !ok {
		return Kind{}, fmt.Errorf("%s: has no x-chalkos-kind object", path)
	}
	group, _ := xk["group"].(string)
	version, _ := xk["version"].(string)
	k, _ := xk["kind"].(string)
	multiple, _ := xk["multiple"].(bool)
	if group == "" || version == "" || k == "" {
		return Kind{}, fmt.Errorf("%s: x-chalkos-kind is missing group, version or kind", path)
	}
	return Kind{APIVersion: group + "/" + version, Kind: k, Multiple: multiple}, nil
}

// Kinds returns the kinds the schemas declare.
func (s *Schemas) Kinds() []Kind {
	return append([]Kind(nil), s.kinds...)
}

// Validate checks a document — decoded JSON such as jsonschema.UnmarshalJSON returns, which keeps
// numbers exact — against the schema of its apiVersion and kind.
func (s *Schemas) Validate(doc any) error {
	m, ok := doc.(map[string]any)
	if !ok {
		return errors.New("a config document must be an object")
	}
	apiVersion, _ := m["apiVersion"].(string)
	docKind, _ := m["kind"].(string)
	name := ""
	if md, ok := m["metadata"].(map[string]any); ok {
		name, _ = md["name"].(string)
	}
	sch, ok := s.schemas[key(apiVersion, docKind)]
	if !ok {
		return &ValidationError{APIVersion: apiVersion, Kind: docKind, Name: name}
	}
	if err := sch.Validate(doc); err != nil {
		return &ValidationError{APIVersion: apiVersion, Kind: docKind, Name: name, err: err}
	}
	return nil
}

// ValidationError is a document its kind's schema refuses, or one of a kind without a schema.
type ValidationError struct {
	APIVersion, Kind, Name string
	// err is the schema's verdict; nil for a kind without a schema.
	err error
}

func (e *ValidationError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("does not accept kind %q of apiVersion %q", e.Kind, e.APIVersion)
	}
	return fmt.Sprintf("%s %q: %s", e.Kind, e.Name, explain(e.err))
}

// Redacted says where the document fails and which keyword refuses it, without the values the
// schema's messages quote: for a document checked against schemas its reader chose, whose
// messages could show values the reader may not see.
func (e *ValidationError) Redacted() string {
	if e.err == nil {
		return fmt.Sprintf("%s %q: no schema of the kind %s of apiVersion %s", e.Kind, e.Name, e.Kind, e.APIVersion)
	}
	verr, ok := e.err.(*jsonschema.ValidationError)
	if !ok {
		return fmt.Sprintf("%s %q: refused by its schema", e.Kind, e.Name)
	}
	var lines []string
	for _, leaf := range leafCauses(verr) {
		keyword := strings.Join(leaf.ErrorKind.KeywordPath(), "/")
		if keyword == "" {
			keyword = "schema"
		}
		lines = append(lines, fmt.Sprintf("- at '/%s': %s", strings.Join(leaf.InstanceLocation, "/"), keyword))
	}
	return fmt.Sprintf("%s %q: %s", e.Kind, e.Name, strings.Join(lines, "\n"))
}

// Image is the config contract of one image build directory.
type Image struct {
	Metadata Metadata
	*Schemas
}

// Load reads metadata.json and the schemas it lists. It fails when metadata's kind list and the
// kinds found in dir/schemas differ, naming every kind present on only one side.
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
	schemas, err := LoadSchemas(filepath.Join(dir, "schemas"))
	if err != nil {
		return nil, err
	}
	if err := checkKindsMatch(md.Kinds, schemas.Kinds()); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return &Image{Metadata: md, Schemas: schemas}, nil
}

// checkKindsMatch fails when metadataKinds and schemaKinds are not the same set of kinds, naming
// every kind that is on one side but not the other.
func checkKindsMatch(metadataKinds, schemaKinds []Kind) error {
	inSchemas := map[Kind]bool{}
	for _, k := range schemaKinds {
		inSchemas[key(k.APIVersion, k.Kind)] = true
	}
	inMetadata := map[Kind]bool{}
	for _, k := range metadataKinds {
		inMetadata[key(k.APIVersion, k.Kind)] = true
	}
	var diffs []string
	for _, k := range metadataKinds {
		if !inSchemas[key(k.APIVersion, k.Kind)] {
			diffs = append(diffs, fmt.Sprintf("metadata lists %s %s, which schemas/ has no schema for", k.APIVersion, k.Kind))
		}
	}
	for _, k := range schemaKinds {
		if !inMetadata[key(k.APIVersion, k.Kind)] {
			diffs = append(diffs, fmt.Sprintf("schemas/ has a schema for %s %s, which metadata does not list", k.APIVersion, k.Kind))
		}
	}
	if len(diffs) == 0 {
		return nil
	}
	return errors.New(strings.Join(diffs, "; "))
}

// compileSchema compiles a schema document already read by readDoc. Its identity inside the
// compiler, needed to resolve its internal $refs, is id, a URL built from the schema's file name
// alone: jsonschema otherwise treats the filesystem path itself as a URL, which misreads or
// rejects a path whose directory contains '%', '#' or '?'. Errors still name the real path.
func compileSchema(c *jsonschema.Compiler, doc any, path, name string) (*jsonschema.Schema, error) {
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
