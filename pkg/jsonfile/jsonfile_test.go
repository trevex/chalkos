package jsonfile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRead(t *testing.T) {
	dir := t.TempDir()
	v := map[string]int{"kept": 1}
	if err := Read(filepath.Join(dir, "missing.json"), &v); err != nil || v["kept"] != 1 {
		t.Fatalf("a missing file: v = %v, err = %v", v, err)
	}
	path := filepath.Join(dir, "a.json")
	os.WriteFile(path, []byte(`{"a": 2}`), 0o600)
	if err := Read(path, &v); err != nil || v["a"] != 2 {
		t.Fatalf("v = %v, err = %v", v, err)
	}
	os.WriteFile(path, []byte(`{`), 0o600)
	if err := Read(path, &v); err == nil || !strings.HasPrefix(err.Error(), path+": ") {
		t.Fatalf("err = %v, want one naming %s", err, path)
	}
}

func TestDecodeStrict(t *testing.T) {
	var v struct{ A int }
	if err := DecodeStrict([]byte(`{"A": 1}`), &v); err != nil || v.A != 1 {
		t.Errorf("DecodeStrict = %v, %+v", err, v)
	}
	if err := DecodeStrict([]byte(`{"B": 1}`), &v); err == nil || errors.Is(err, ErrTrailingData) {
		t.Errorf("an unknown field: %v", err)
	}
	if err := DecodeStrict([]byte(`{"A": 1} {}`), &v); !errors.Is(err, ErrTrailingData) {
		t.Errorf("data after the value: %v", err)
	}
}

func TestDecodeExact(t *testing.T) {
	var v any
	if err := DecodeExact([]byte(`{"n": 12345678901234567890}`), &v); err != nil || v.(map[string]any)["n"] != json.Number("12345678901234567890") {
		t.Errorf("DecodeExact = %v, %v", err, v)
	}
	if err := DecodeExact([]byte(`1 2`), &v); !errors.Is(err, ErrTrailingData) {
		t.Errorf("data after the value: %v", err)
	}
}

func TestReadDefs(t *testing.T) {
	dir := t.TempDir()
	positive := func(_ string, n int) error {
		if n <= 0 {
			return errors.New("not positive")
		}
		return nil
	}
	if defs, err := ReadDefs(filepath.Join(dir, "missing.json"), "number", positive); err != nil || defs == nil || len(defs) != 0 {
		t.Fatalf("a missing file: %v, %v; want no definitions", defs, err)
	}
	path := filepath.Join(dir, "defs.json")
	os.WriteFile(path, []byte(`null`), 0o600)
	if defs, err := ReadDefs(path, "number", positive); err != nil || defs == nil || len(defs) != 0 {
		t.Fatalf("null: %v, %v; want no definitions", defs, err)
	}
	os.WriteFile(path, []byte(`{"b": 0, "a": -1, "c": 1}`), 0o600)
	_, err := ReadDefs(path, "number", positive)
	if want := path + ": number a: not positive\n" + path + ": number b: not positive"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if defs, err := ParseDefs(path, []byte(`{"c": 1}`), "number", positive); err != nil || defs["c"] != 1 {
		t.Fatalf("ParseDefs = %v, %v", defs, err)
	}
	if _, err := ParseDefs(path, []byte(`[]`), "number", positive); err == nil || !strings.HasPrefix(err.Error(), path+": ") {
		t.Fatalf("ParseDefs of no object: %v", err)
	}
}

// A definition file that holds a field the definitions lack is refused, so a field one side of
// the image's build renames or adds alone does not go unread.
func TestParseDefsRefusesUnknownFields(t *testing.T) {
	type def struct {
		Run []string `json:"run"`
	}
	accept := func(string, def) error { return nil }
	if defs, err := ParseDefs("defs.json", []byte(`{"a": {"run": ["/bin/a"]}}`), "def", accept); err != nil || len(defs["a"].Run) != 1 {
		t.Fatalf("ParseDefs = %v, %v", defs, err)
	}
	_, err := ParseDefs("defs.json", []byte(`{"a": {"run": ["/bin/a"], "waitFor": {}}}`), "def", accept)
	if err == nil || !strings.Contains(err.Error(), `unknown field "waitFor"`) {
		t.Fatalf("err = %v, want one for the unknown field waitFor", err)
	}
}
