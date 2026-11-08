package schema

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const fixture = "../../test/fixtures/kinds/image"

func decode(t *testing.T, doc string) map[string]any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object", doc)
	}
	return m
}

func TestReadMetadataFields(t *testing.T) {
	md, err := ReadMetadata(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if md.ID != "chalkos" || md.Variant != "worker" || md.Platform != "metal" || len(md.RootHash) != 64 || md.Revision != "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678" || md.Tag != "v1.5.0" {
		t.Errorf("metadata = %+v", md)
	}
	if len(md.Kinds) != 1 || md.Kinds[0] != (Kind{APIVersion: "demo.example.org/v1alpha1", Kind: "Demo", Multiple: true}) {
		t.Errorf("kinds = %+v", md.Kinds)
	}
}

func TestParseMetadata(t *testing.T) {
	if _, err := ParseMetadata([]byte(`{"schemaVersion": 7}`)); err == nil || !strings.Contains(err.Error(), "schemaVersion 7") {
		t.Errorf("err = %v", err)
	}
	if _, err := ParseMetadata([]byte("{")); err == nil || !strings.Contains(err.Error(), "parse metadata.json") {
		t.Errorf("err = %v", err)
	}
}

// A node has no metadata.json, only the schemas directory; chalkd reads the contract this way.
func TestLoadSchemasOnFixture(t *testing.T) {
	ss, err := LoadSchemas(filepath.Join(fixture, "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := ss.Kinds()
	if len(kinds) != 1 || kinds[0] != (Kind{APIVersion: "demo.example.org/v1alpha1", Kind: "Demo", Multiple: true}) {
		t.Fatalf("kinds = %+v", kinds)
	}
	doc := decode(t, `{
		"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"},
		"spec": {"name": "x"}
	}`)
	if err := ss.Validate(doc); err != nil {
		t.Fatal(err)
	}
}

// KindsInDir names the kinds of the files FileName names, and of no other file or directory.
func TestKindsInDir(t *testing.T) {
	dir := t.TempDir()
	want := []Kind{{APIVersion: "a.example.org/v1", Kind: "Thing"}, {APIVersion: "b.example.org/v1alpha1", Kind: "Other"}}
	for _, k := range want {
		if err := os.WriteFile(filepath.Join(dir, FileName(k)), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"wrong_name.json", "a_b_C.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "c.example.org_v1_Dir.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := KindsInDir(dir)
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("KindsInDir = %+v, %v; want %+v", got, err, want)
	}
	if _, err := KindsInDir(filepath.Join(dir, "none")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("KindsInDir without the directory: err = %v, want fs.ErrNotExist", err)
	}
}

func writeSchema(t *testing.T, dir, name string, xChalkosKind string) {
	t.Helper()
	doc := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"x-chalkos-kind": ` + xChalkosKind + `
	}`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSchemasRejectsMismatchedFileName(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "wrong_name.json",
		`{"group": "a.example.org", "version": "v1", "kind": "Thing", "multiple": false}`)
	_, err := LoadSchemas(dir)
	if err == nil || !strings.Contains(err.Error(), "wrong_name.json") {
		t.Fatalf("err = %v, want it to name wrong_name.json", err)
	}
}

// A kind's x-chalkos-kind declares it withheld; a kind without the field, and a kind the schemas
// do not declare, are not.
func TestLoadSchemasReadsWithheld(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "a.example.org_v1_Kept.json",
		`{"group": "a.example.org", "version": "v1", "kind": "Kept", "multiple": true, "withheld": true}`)
	writeSchema(t, dir, "a.example.org_v1_Thing.json",
		`{"group": "a.example.org", "version": "v1", "kind": "Thing", "multiple": false}`)
	ss, err := LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ss.Kinds(); len(got) != 2 || got[0] != (Kind{APIVersion: "a.example.org/v1", Kind: "Kept", Multiple: true, Withheld: true}) || got[1].Withheld {
		t.Errorf("kinds = %+v", got)
	}
	for _, c := range []struct {
		apiVersion, kind string
		want             bool
	}{
		{"a.example.org/v1", "Kept", true},
		{"a.example.org/v1", "Thing", false},
		{"b.example.org/v1", "Kept", false},
		{"a.example.org/v1", "Other", false},
	} {
		if got := ss.Withheld(c.apiVersion, c.kind); got != c.want {
			t.Errorf("Withheld(%s, %s) = %v, want %v", c.apiVersion, c.kind, got, c.want)
		}
	}
}

func TestLoadSchemasRejectsDuplicateKind(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "a.example.org_v1_Thing.json",
		`{"group": "a.example.org", "version": "v1", "kind": "Thing", "multiple": false}`)
	writeSchema(t, dir, "a.example.org_v1_Thing_again.json",
		`{"group": "a.example.org", "version": "v1", "kind": "Thing", "multiple": false}`)
	_, err := LoadSchemas(dir)
	if err == nil || !strings.Contains(err.Error(), "a.example.org/v1 Thing") {
		t.Fatalf("err = %v, want it to name the duplicate kind", err)
	}
}

// A mount or an image's build directory may contain characters, such as '%', '#' and '?', that
// are meaningful in a URL. LoadSchemas must not mistake the directory for one.
func TestLoadSchemasFromDirWithURLMetacharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d #1 100%?")
	if err := os.CopyFS(dir, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	ss, err := LoadSchemas(filepath.Join(dir, "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, `{
		"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"},
		"spec": {"name": "x"}
	}`)
	if err := ss.Validate(doc); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAcceptsDocument(t *testing.T) {
	ss, err := LoadSchemas(filepath.Join(fixture, "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, `{
		"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"},
		"spec": {"name": "x", "replicas": 3, "token": {"secretRef": {"name": "s", "key": "k"}}}
	}`)
	if err := ss.Validate(doc); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	ss, err := LoadSchemas(filepath.Join(fixture, "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ doc, want string }{
		"missing required field": {
			`{"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"}, "spec": {}}`,
			"name",
		},
		"unknown field": {
			`{"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"}, "spec": {"name": "x", "nmae": "y"}}`,
			"nmae",
		},
		"value out of range": {
			`{"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"}, "spec": {"name": "x", "replicas": 300}}`,
			"replicas",
		},
		"value outside enum": {
			`{"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"}, "spec": {"name": "x", "mode": "slow"}}`,
			"mode",
		},
		"incomplete secret reference": {
			`{"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"}, "spec": {"name": "x", "token": {"secretRef": {"name": "s"}}}}`,
			"key",
		},
		"invalid name": {
			`{"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "A_b"}, "spec": {"name": "x"}}`,
			"metadata",
		},
		"unknown kind": {
			`{"apiVersion": "demo.example.org/v1alpha1", "kind": "Other", "metadata": {"name": "a"}, "spec": {}}`,
			`does not accept kind "Other"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := ss.Validate(decode(t, tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "jsonschema validation failed") {
				t.Errorf("err = %v, want the library's header stripped", err)
			}
		})
	}
}

// TestReadMetadata reads an image directory's metadata.json alone and refuses a version it does
// not know.
func TestReadMetadata(t *testing.T) {
	md, err := ReadMetadata("../../test/fixtures/kinds/image")
	if err != nil {
		t.Fatal(err)
	}
	if md.ID == "" || md.Platform == "" {
		t.Errorf("metadata = %+v", md)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"schemaVersion": 7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMetadata(dir); err == nil || !strings.Contains(err.Error(), "schemaVersion 7") {
		t.Errorf("version 7: %v", err)
	}
}

// A name another kind's references reserve, refused by the not the kinds module adds to
// metadata.name, says so instead of 'not' failed.
func TestValidateSaysANameIsReserved(t *testing.T) {
	doc := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title": "VRF", "type": "object",
		"properties": {
			"apiVersion": {"const": "r.example.org/v1"}, "kind": {"const": "VRF"},
			"metadata": {"type": "object", "properties": {"name": {"type": "string", "not": {"enum": ["default"]}}}, "required": ["name"]},
			"spec": {"type": "object"}
		},
		"x-chalkos-kind": {"group": "r.example.org", "version": "v1", "kind": "VRF", "multiple": true}
	}`
	s, err := LoadFiles(map[string][]byte{"r.example.org_v1_VRF.json": []byte(doc)})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Validate(map[string]any{"apiVersion": "r.example.org/v1", "kind": "VRF", "metadata": map[string]any{"name": "default"}, "spec": map[string]any{}})
	want := `VRF "default": - at '/metadata/name': the name "default" is reserved: fields that reference a VRF use it for something other than a document`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %s", err, want)
	}
}
