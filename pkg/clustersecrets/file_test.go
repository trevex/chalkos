package clustersecrets

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestReplaceFileKeepsThePreviousVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	if err := Replace(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".prev"); !os.IsNotExist(err) {
		t.Errorf("a new file has a previous version: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	for _, next := range []string{"two", "three"} {
		before, _ := os.ReadFile(path)
		if err := Replace(path, []byte(next), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != next {
			t.Errorf("file holds %q, want %q", got, next)
		}
		if prev, _ := os.ReadFile(path + ".prev"); string(prev) != string(before) {
			t.Errorf("previous version holds %q, want %q", prev, before)
		}
	}
	for _, p := range []string{path, path + ".prev"} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o640 {
			t.Errorf("%s: mode %v, %v; want the file's own 0640", p, info.Mode(), err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("left behind %v", entries)
	}
}

// TestReplaceFileFollowsLinks checks that a secrets file that is a symbolic link is replaced
// where it points, keeping the link.
func TestReplaceFileFollowsLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real", "secrets.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "secrets.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Replace(link, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "two" {
		t.Errorf("the target holds %q", got)
	}
	if prev, _ := os.ReadFile(target + ".prev"); string(prev) != "one" {
		t.Errorf("the previous version beside the target holds %q", prev)
	}
}

// TestDirectorySyncedAfterWrites checks that the directory of a new or replaced secrets file is
// synced, and that a sync failing once the file is in place is an *UnsyncedError, after which
// the file counts as replaced.
func TestDirectorySyncedAfterWrites(t *testing.T) {
	var synced []string
	failing := false
	defer func(orig func(string) error) { SyncDir = orig }(SyncDir)
	SyncDir = func(dir string) error {
		synced = append(synced, dir)
		if failing {
			return errors.New("sync failed")
		}
		return nil
	}
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	os.Mkdir(outDir, 0o755)
	if err := Create(filepath.Join(outDir, "secrets.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(synced, outDir) {
		t.Errorf("synced %v, not the directory of the new file", synced)
	}

	path := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	failing = true
	var unsynced *UnsyncedError
	if err := f.Replace([]byte("two")); !errors.As(err, &unsynced) {
		t.Errorf("err = %v, want an *UnsyncedError", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "two" {
		t.Errorf("the file holds %q, want it replaced", got)
	}
	// The file holds what Replace wrote, so the next Replace finds it unchanged.
	failing = false
	if err := f.Replace([]byte("three")); err != nil {
		t.Errorf("the next replace: %v", err)
	}
}

// TestReplaceRefusesAChangedFile checks that File.Replace leaves a file alone that changed since
// it was read.
func TestReplaceRefusesAChangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, data, err := Open(path)
	if err != nil || string(data) != "one" {
		t.Fatalf("Open = %q, %v", data, err)
	}
	defer f.Close()
	if err := os.WriteFile(path, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Replace([]byte("two")); !errors.Is(err, ErrChanged) {
		t.Errorf("err = %v, want ErrChanged", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "edited" {
		t.Errorf("the file holds %q, want the edit kept", got)
	}
}

// TestOpenLocks checks that a secrets file opened once cannot be opened again until it is closed.
func TestOpenLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Errorf("err = %v, want ErrLocked", err)
	}
	f.Close()
	g, _, err := Open(path)
	if err != nil {
		t.Fatalf("after Close: %v", err)
	}
	g.Close()
}
