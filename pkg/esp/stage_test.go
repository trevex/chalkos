package esp

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestStageAndName stages UKIs under names systemd-boot ignores, lists the staged ones but the
// one kept, and names one, which Entries then lists.
func TestStageAndName(t *testing.T) {
	dir := t.TempDir()
	a, b := bytes.Repeat([]byte{0xa}, 32), bytes.Repeat([]byte{0xb}, 32)
	staged := map[string]string{}
	for name, root := range map[string][]byte{"a": a, "b": b} {
		path, err := Stage(dir, root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, testUKI(root), 0o644); err != nil {
			t.Fatal(err)
		}
		staged[name] = path
	}
	if list, err := Entries(dir); err != nil || len(list) != 0 {
		t.Fatalf("entries of staged UKIs = %+v, %v; want none", list, err)
	}
	left, err := Leftovers(dir, staged["b"])
	if err != nil || !reflect.DeepEqual(left, []string{filepath.Base(staged["a"])}) {
		t.Fatalf("leftovers = %q, %v", left, err)
	}
	var changes []string
	change := func(what string) error { changes = append(changes, what); return nil }
	if err := Remove(dir, left, change); err != nil {
		t.Fatal(err)
	}
	if err := Name(dir, staged["b"], EntryName("chalkos", 1), change); err != nil {
		t.Fatal(err)
	}
	if want := []string{"remove " + filepath.Base(staged["a"]), "name the UKI chalkos_1.efi"}; !reflect.DeepEqual(changes, want) {
		t.Errorf("changes = %q, want %q", changes, want)
	}
	list, err := Entries(dir)
	if err != nil || len(list) != 1 || list[0].File != "chalkos_1.efi" || !bytes.Equal(list[0].RootHash, b) {
		t.Errorf("entries = %+v, %v", list, err)
	}
	if left, err := Leftovers(dir, ""); err != nil || len(left) != 0 {
		t.Errorf("leftovers = %q, %v; want none", left, err)
	}

	stop := errors.New("stopped")
	path, err := Stage(dir, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, testUKI(a), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Name(dir, path, EntryName("chalkos", 2), func(string) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("name = %v, want it stopped before the rename", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the stopped name moved the staged UKI: %v", err)
	}
}

// TestSyncWithoutUKIs syncs an ESP that holds no UKIs yet, as an install's does before it writes
// one.
func TestSyncWithoutUKIs(t *testing.T) {
	if err := Sync(t.TempDir()); err != nil {
		t.Error(err)
	}
}
