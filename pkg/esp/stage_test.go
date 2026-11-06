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
		path, err := Temporary(dir, root)
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
	path, err := Temporary(dir, a)
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

// TestStaged reads the UKI of an image staged in the inactive slot under its staged name, with the
// generation the name gives, which Entries does not list, as systemd-boot does not boot it, and
// which Leftovers leaves; a file of the prefix that is no UKI of the image, or whose name gives no
// generation, is none, and StagedFiles lists them all.
func TestStaged(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := Staged(dir, "chalkos"); ok || err != nil {
		t.Fatalf("staged on an empty ESP = %v, %v; want none", ok, err)
	}
	root := bytes.Repeat([]byte{0xc}, 32)
	tmp, err := Temporary(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, testUKI(root), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Name(dir, tmp, StagedName("chalkos", 3), nil); err != nil {
		t.Fatal(err)
	}
	e, ok, err := Staged(dir, "chalkos")
	if err != nil || !ok || e.File != ".staged-chalkos_3.efi" || e.Generation != 3 || !bytes.Equal(e.RootHash, root) || e.TriesLeft != -1 || e.ID != "" {
		t.Errorf("staged = %+v, %v, %v", e, ok, err)
	}
	if list, err := Entries(dir); err != nil || len(list) != 0 {
		t.Errorf("entries = %+v, %v; want none", list, err)
	}
	if left, err := Leftovers(dir, ""); err != nil || len(left) != 0 {
		t.Errorf("leftovers = %q, %v; want none", left, err)
	}
	if _, ok, err := Staged(dir, "other"); ok || err != nil {
		t.Errorf("staged of another image = %v, %v; want none", ok, err)
	}
	for _, name := range []string{StagedName("chalkos", 4), ".staged-chalkos_05.efi", ".staged-x.efi"} {
		if err := os.WriteFile(filepath.Join(dir, LinuxDir, name), []byte("no UKI"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if e, ok, err := Staged(dir, "chalkos"); !ok || err != nil || e.File != ".staged-chalkos_3.efi" {
		t.Errorf("staged beside files that are none = %+v, %v, %v", e, ok, err)
	}
	if files, err := StagedFiles(dir); err != nil || !reflect.DeepEqual(files, []string{".staged-chalkos_05.efi", ".staged-chalkos_3.efi", ".staged-chalkos_4.efi", ".staged-x.efi"}) {
		t.Errorf("staged files = %q, %v", files, err)
	}
	// Every staged name counts for the next generation, whatever the file holds, as an entry's.
	if g, err := NextStagedGeneration(dir, []Entry{{File: "chalkos_2.efi", ID: "chalkos_2.efi"}}, "chalkos"); err != nil || g != 6 {
		t.Errorf("the next staged generation = %d, %v, want 6 above .staged-chalkos_05.efi", g, err)
	}
	if g, err := NextStagedGeneration(dir, []Entry{{File: "chalkos_9+1.efi", ID: "chalkos_9.efi"}}, "chalkos"); err != nil || g != 10 {
		t.Errorf("the next staged generation = %d, %v, want 10 above chalkos_9+1.efi", g, err)
	}
}
