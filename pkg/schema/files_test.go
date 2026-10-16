package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFiles(t *testing.T) {
	entries, err := os.ReadDir(builtin)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(builtin, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = data
	}
	s, err := LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Kinds()) != len(files) {
		t.Errorf("kinds = %v, want one per file of %d", s.Kinds(), len(files))
	}
	for _, name := range []string{"../x.json", ".x.json", "x.yaml", "a/x.json"} {
		_, err := LoadFiles(map[string][]byte{name: []byte("{}")})
		if err == nil || !strings.Contains(err.Error(), "is not the name of a schema file") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// kindSchema is the schema file of the kind Probe of example.org/v1 with the schema's other
// keywords body, a JSON object's members.
func kindSchema(body string) map[string][]byte {
	return map[string][]byte{"example.org_v1_Probe.json": []byte(`{"x-chalkos-kind": {"group": "example.org", "version": "v1", "kind": "Probe", "multiple": false}, ` + body + `}`)}
}

// TestSchemasFetchNothing: a schema refers to nothing outside the image's schemas, so a file on
// the machine, a relative reference to a schema that is not there and a URL all fail to load.
func TestSchemasFetchNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "other.json")
	if err := os.WriteFile(file, []byte(`{"type": "string"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"file://" + file, "http://example.org/schema.json", "other.json"} {
		_, err := LoadFiles(kindSchema(`"$ref": "` + ref + `"`))
		if err == nil || !strings.Contains(err.Error(), "may refer only to the image's schemas") {
			t.Errorf("%s: err = %v", ref, err)
		}
	}
}

// TestLoadRefusesASchemaTheLibraryPanicsOn: a multipleOf too small for math/big makes the
// library panic as it compiles; that is an error, not a crash.
func TestLoadRefusesASchemaTheLibraryPanicsOn(t *testing.T) {
	_, err := LoadFiles(kindSchema(`"type": "object", "properties": {"n": {"type": "number", "multipleOf": 1e-5000000}}`))
	if err == nil || !strings.Contains(err.Error(), "does not compile") {
		t.Fatalf("err = %v, want the library's panic as an error", err)
	}
}
