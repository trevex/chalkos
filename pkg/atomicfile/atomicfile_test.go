package atomicfile

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileCreatesWithPerm(t *testing.T) {
	for _, perm := range []fs.FileMode{0o600, 0o644, 0o755} {
		path := filepath.Join(t.TempDir(), "a", "b", "file")
		if err := WriteFile(path, []byte("data"), perm); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != perm {
			t.Errorf("mode %v, want %v", info.Mode().Perm(), perm)
		}
		assertContent(t, path, "data")
		assertOnly(t, filepath.Dir(path), "file")
	}
}

func TestWriteFileReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("old content, longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertContent(t, path, "new")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
	assertOnly(t, dir, "file")
}

func TestWriteFileLeavesNoTemporaryFileOnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	// A directory with an entry at path cannot be renamed over.
	if err := os.MkdirAll(filepath.Join(path, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("data"), 0o644); err == nil {
		t.Fatal("WriteFile over a directory succeeded")
	}
	assertOnly(t, dir, "file")
}

func TestStageLeavesTheFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp, err := Stage(path, []byte("new"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(tmp) != dir {
		t.Errorf("staged in %s, want %s", filepath.Dir(tmp), dir)
	}
	assertContent(t, path, "old")
	assertContent(t, tmp, "new")
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s holds %q, want %q", path, got, want)
	}
}

// assertOnly fails unless dir holds exactly the names.
func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != len(names) {
		t.Fatalf("%s holds %v, want %v", dir, got, names)
	}
	for i := range got {
		if got[i] != names[i] {
			t.Fatalf("%s holds %v, want %v", dir, got, names)
		}
	}
}
