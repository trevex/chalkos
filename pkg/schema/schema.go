// Package schema reads the config contract of a chalkos image: metadata.json, which names the
// image and the config kinds it accepts, and a schemas directory, one JSON Schema per kind. Load
// reads a build directory, which has both. A running node ships only the schemas directory, as
// /etc/chalkos/schemas, with no metadata.json alongside it — metadata's rootHash is computed over
// the image itself — so chalkd reads the running image's contract with LoadSchemas instead, which
// finds each schema's kind from the schema's own "x-chalkos-kind". The package validates config
// documents against those schemas.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	// Revision and Tag are the git revision and tag the image was built from, as mkImages was
	// given them; empty when it was not. They say where the image came from and decide nothing.
	Revision string `json:"revision"`
	Tag      string `json:"tag"`
	// Variant is the name of the mkImages variant the image is built as.
	Variant string `json:"variant"`
	// Platform is the platform the image is built for, such as metal or kvm.
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
	// Withheld tells whether the node withholds the kind's documents from its services: they stay
	// in the node's stored config for chalkd alone, and no unit, renderer, validator or effect
	// reads them. A layer declares it for a kind chalkd reads itself, such as one whose documents
	// carry secrets of their own.
	Withheld bool `json:"withheld,omitempty"`
}

// rootHash is a store's dm-verity root hash as metadata.json and an image's header write it.
var rootHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// IsRootHash tells whether s is a store's dm-verity root hash as metadata.json writes it: 64
// lower-case hex digits.
func IsRootHash(s string) bool { return rootHash.MatchString(s) }

// Schemas is the compiled schema of every kind named by the *.json files of a schemas directory,
// such as an image's schemas/ in its build output or /etc/chalkos/schemas on a running node, or by
// the files LoadFiles is given. It is built from each file's "x-chalkos-kind" alone, so it needs
// no metadata.json.
type Schemas struct {
	// kinds is in the order of the files' names.
	kinds   []Kind
	schemas map[Kind]*jsonschema.Schema
	// raw holds each kind's schema document as its file holds it, for readers of annotations.
	raw map[Kind]any
}

// LoadSchemas compiles every *.json file in dir, as compile does, naming each by its path in
// errors. Directories are skipped; a link is followed.
func LoadSchemas(dir string) (*Schemas, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = data
	}
	return compile(files, dir)
}

// compile compiles the schema files, by name, in the order of their names, and builds the kind
// list from each file's "x-chalkos-kind": {"group","version","kind","multiple"}, with "withheld"
// for a withheld kind. A file's name
// must equal "<group>_<version>_<kind>.json" by its own declared identity, and no two files may
// declare the same kind; either failure names the offending file or kind. Errors name a file by
// its path below dir, or by its name alone when dir is empty.
func compile(files map[string][]byte, dir string) (*Schemas, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(noLoader{})
	// Draft 2020-12 takes "format" as an annotation; the kinds' schemas assert theirs.
	c.AssertFormat()
	for _, f := range formats {
		c.RegisterFormat(f)
	}
	ss := &Schemas{schemas: map[Kind]*jsonschema.Schema{}, raw: map[Kind]any{}}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(dir, name)
		doc, err := decodeDoc(files[name], path)
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

// fileName is the name a schema of k must have, as lib/kinds/schema.nix also builds it.
func fileName(k Kind) string {
	group, version, _ := strings.Cut(k.APIVersion, "/")
	return group + "_" + version + "_" + k.Kind + ".json"
}

// decodeDoc decodes the JSON document of the file at path with jsonschema.UnmarshalJSON, which
// keeps numbers exact, the way compileSchema needs it and the way kindOf reads "x-chalkos-kind".
func decodeDoc(data []byte, path string) (any, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
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
	withheld, _ := xk["withheld"].(bool)
	if group == "" || version == "" || k == "" {
		return Kind{}, fmt.Errorf("%s: x-chalkos-kind is missing group, version or kind", path)
	}
	return Kind{APIVersion: group + "/" + version, Kind: k, Multiple: multiple, Withheld: withheld}, nil
}

// Kinds returns the kinds the schemas declare.
func (s *Schemas) Kinds() []Kind {
	return append([]Kind(nil), s.kinds...)
}

// Withheld tells whether the schemas declare the kind of that apiVersion and name withheld from
// the node's services. A kind they do not declare is not withheld.
func (s *Schemas) Withheld(apiVersion, kind string) bool {
	i := slices.IndexFunc(s.kinds, func(k Kind) bool { return k.APIVersion == apiVersion && k.Kind == kind })
	return i >= 0 && s.kinds[i].Withheld
}

// Validate checks a document — decoded JSON such as jsonschema.UnmarshalJSON returns, which keeps
// numbers exact — against the schema of its apiVersion and kind.
func (s *Schemas) Validate(doc map[string]any) error {
	apiVersion, _ := doc["apiVersion"].(string)
	docKind, _ := doc["kind"].(string)
	sch, ok := s.schemas[key(apiVersion, docKind)]
	if !ok {
		return fmt.Errorf("does not accept kind %q of apiVersion %q", docKind, apiVersion)
	}
	if err := sch.Validate(doc); err != nil {
		name := ""
		if md, ok := doc["metadata"].(map[string]any); ok {
			name, _ = md["name"].(string)
		}
		return fmt.Errorf("%s %q: %s", docKind, name, explain(err, docKind, name))
	}
	return nil
}

// ReadMetadata reads an image directory's metadata.json alone, refusing a version this package
// does not read.
func ReadMetadata(dir string) (Metadata, error) {
	b, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		return Metadata{}, err
	}
	md, err := ParseMetadata(b)
	if err != nil {
		return Metadata{}, fmt.Errorf("%s: %w", dir, err)
	}
	return md, nil
}

// ParseMetadata parses an image's metadata.json, refusing a version this package does not read.
func ParseMetadata(data []byte) (Metadata, error) {
	var md Metadata
	if err := json.Unmarshal(data, &md); err != nil {
		return Metadata{}, fmt.Errorf("parse metadata.json: %w", err)
	}
	if md.SchemaVersion != metadataVersion {
		return Metadata{}, fmt.Errorf("metadata schemaVersion %d is not supported; this chalkos reads version %d", md.SchemaVersion, metadataVersion)
	}
	return md, nil
}

// compileSchema compiles a schema document already read by readDoc. Its identity inside the
// compiler, needed to resolve its internal $refs, is id, a URL built from the schema's file name
// alone: jsonschema otherwise treats the filesystem path itself as a URL, which misreads or
// rejects a path whose directory contains '%', '#' or '?'. Errors still name the real path.
func compileSchema(c *jsonschema.Compiler, doc any, path, name string) (s *jsonschema.Schema, err error) {
	// The library panics on some schemas, such as a multipleOf too small for math/big; a schema
	// is data, which must not crash chalkd or chalkctl.
	defer func() {
		if r := recover(); r != nil {
			s, err = nil, fmt.Errorf("%s: the schema does not compile: %v", path, r)
		}
	}()
	id := "chalkos:///schemas/" + name
	if err := c.AddResource(id, doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s, err = c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// noLoader fetches no schema: a schema may refer to itself, the image's other schemas and the
// JSON Schema metaschemas, which the compiler holds, but never to a file or a URL, which the
// compiler would otherwise read.
type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("a schema may refer only to the image's schemas, not to %s", url)
}

// The lookup key ignores Multiple, which the schema does not depend on.
func key(apiVersion, kind string) Kind {
	return Kind{APIVersion: apiVersion, Kind: kind}
}

// explain renders a schema validation error as its leaf causes alone, one per line, dropping the
// "jsonschema validation failed with ..." header the library puts on every error, and the
// allOf/anyOf/$ref nodes that group the causes without saying anything of their own.
func explain(err error, docKind, name string) string {
	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err.Error()
	}
	var lines []string
	for _, leaf := range leafCauses(verr) {
		lines = append(lines, "- "+leafText(leaf, docKind, name))
	}
	// The causes come in the order the library walks maps in, which varies from run to run.
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// leafText is what explain says of a failed assertion of the docKind document by name. The kinds
// module refuses the names that references to a kind reserve with a not on metadata.name, of which
// the library says only that 'not' failed.
func leafText(e *jsonschema.ValidationError, docKind, name string) string {
	if _, ok := e.ErrorKind.(*kind.Not); ok && slices.Equal(e.InstanceLocation, []string{"metadata", "name"}) {
		return fmt.Sprintf("at '/metadata/name': the name %q is reserved: fields that reference a %s use it for something other than a document", name, docKind)
	}
	return e.Error()
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
