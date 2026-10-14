package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const fixture = "../../test/fixtures/kinds/image"

func decode(t *testing.T, doc string) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLoadReadsMetadata(t *testing.T) {
	img, err := Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	md := img.Metadata
	if md.ID != "chalkos" || md.Platform != "metal" || len(md.RootHash) != 64 {
		t.Errorf("metadata = %+v", md)
	}
	if len(md.Kinds) != 1 || md.Kinds[0] != (Kind{APIVersion: "demo.example.org/v1alpha1", Kind: "Demo", Multiple: true}) {
		t.Errorf("kinds = %+v", md.Kinds)
	}
}

func TestLoadRejectsUnknownMetadataVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"schemaVersion": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "schemaVersion 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadFailsOnMissingSchema(t *testing.T) {
	dir := t.TempDir()
	md := `{"schemaVersion": 0, "kinds": [{"apiVersion": "a.example.org/v1", "kind": "Gone"}]}`
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "a.example.org_v1_Gone.json") {
		t.Fatalf("err = %v", err)
	}
}

// A mount or an image's build directory may contain characters, such as '%', '#' and '?', that
// are meaningful in a URL. Load must not mistake the directory for one.
func TestLoadFromDirWithURLMetacharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d #1 100%?")
	if err := copyDir(fixture, dir); err != nil {
		t.Fatal(err)
	}
	img, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, `{
		"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"},
		"spec": {"name": "x"}
	}`)
	if err := img.Validate(doc); err != nil {
		t.Fatal(err)
	}
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, strings.TrimPrefix(path, src))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

func TestValidateAcceptsDocument(t *testing.T) {
	img, err := Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, `{
		"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "a"},
		"spec": {"name": "x", "replicas": 3, "token": {"secretRef": {"name": "s", "key": "k"}}}
	}`)
	if err := img.Validate(doc); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	img, err := Load(fixture)
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
		"not an object": {`[]`, "object"},
	} {
		t.Run(name, func(t *testing.T) {
			err := img.Validate(decode(t, tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "jsonschema validation failed") {
				t.Errorf("err = %v, want the library's header stripped", err)
			}
		})
	}
}
