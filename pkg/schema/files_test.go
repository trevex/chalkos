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
