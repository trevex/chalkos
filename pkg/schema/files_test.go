package schema

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
// the machine, a relative reference to a schema that is not there and a URL all fail alike, and
// the error says nothing of a file's content or whether it exists.
func TestSchemasFetchNothing(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"type": "string", "description": "zq-content-7"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	errOf := func(ref string) string {
		_, err := LoadFiles(kindSchema(`"$ref": "` + ref + `"`))
		var lerr *LoadError
		if !errors.As(err, &lerr) || lerr.File != "example.org_v1_Probe.json" {
			t.Fatalf("%s: err = %v, want a LoadError naming the file", ref, err)
		}
		if !strings.Contains(err.Error(), "may refer only to the image's schemas") {
			t.Errorf("%s: err = %v", ref, err)
		}
		// The temporary directory LoadFiles writes to differs from call to call.
		_, msg, _ := strings.Cut(err.Error(), ".json: ")
		return strings.ReplaceAll(msg, ref, "REF")
	}
	present, absent := errOf("file://"+secret), errOf("file://"+missing)
	if strings.Contains(present, "zq-content-7") || present != absent {
		t.Errorf("a file that exists fails as\n%s\nand one that does not as\n%s", present, absent)
	}
	errOf("http://example.org/schema.json")
	errOf("other.json")
}

// TestDefaultEndsOnASchemaThatRefersToItself: a schema whose group of options holds itself, or
// whose $refs go round, is refused when it loads or when defaults are filled in, not with a
// crash or a hang.
func TestDefaultEndsOnASchemaThatRefersToItself(t *testing.T) {
	doc := map[string]any{"apiVersion": "example.org/v1", "kind": "Probe", "metadata": map[string]any{"name": "p"}}
	for name, body := range map[string]string{
		"a group holding itself": `"type": "object", "properties": {"spec": {"$ref": "#/$defs/g"}},
			"$defs": {"g": {"type": "object", "properties": {"spec": {"$ref": "#/$defs/g"}}}}`,
		"a $ref cycle": `"type": "object", "properties": {"spec": {"$ref": "#/$defs/a"}},
			"$defs": {"a": {"$ref": "#/$defs/b"}, "b": {"$ref": "#/$defs/a"}}`,
		"an anyOf holding itself": `"type": "object", "properties": {"spec": {"$ref": "#/$defs/a"}},
			"$defs": {"a": {"anyOf": [{"$ref": "#/$defs/a"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := LoadFiles(kindSchema(body))
			if err != nil {
				// Refused when it loads is handled too.
				t.Logf("LoadFiles = %v", err)
				return
			}
			done := make(chan error, 1)
			go func() {
				d := map[string]any{}
				for k, v := range doc {
					d[k] = v
				}
				d["spec"] = map[string]any{}
				// Validation follows the library's own guard against cycles; it must end too.
				_ = s.Validate(d)
				_, err := s.Default(d)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Error("Default filled in a schema without end")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Default did not end")
			}
		})
	}
}
