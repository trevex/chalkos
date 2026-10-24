package jsonfile

import (
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
