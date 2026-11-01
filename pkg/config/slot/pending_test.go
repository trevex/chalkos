package slot

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestPending keeps the pending config as a slot beside the images' slots, Secrets readable by
// root alone, with the time it was written. List and Retire pass it by, and Remove removes it
// with the directories of interrupted writes; removed twice, it is gone already.
func TestPending(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	a := bytes.Repeat([]byte{0xaa}, 32)
	if err := Write(Dir(root, a), stored(t, "v1")); err != nil {
		t.Fatal(err)
	}
	pending := Pending(root)
	if _, found, err := Written(pending); err != nil || found {
		t.Errorf("written without a pending config: found %v, %v", found, err)
	}
	before := time.Now().Add(-time.Second)
	docs := parse(t, set)
	if err := Write(pending, Stored{Delivered: docs, Version: "p1"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := Read(pending)
	if err != nil || !found || got.Version != "p1" || len(got.Delivered) != 3 || len(got.Documents) != 0 {
		t.Errorf("pending = %+v, found %v, %v", got, found, err)
	}
	if m := mode(t, filepath.Join(pending, "delivered", "secrets", "demo.json")); m != 0o600 {
		t.Errorf("a pending Secret's mode = %v, want 0600", m)
	}
	if at, found, err := Written(pending); err != nil || !found || at.Before(before) || at.After(time.Now()) {
		t.Errorf("written at %v, found %v, %v", at, found, err)
	}
	if hashes, err := List(root); err != nil || !reflect.DeepEqual(hashes, [][]byte{a}) {
		t.Errorf("slots = %x, %v; want aa… alone", hashes, err)
	}
	if err := Retire(root, [][]byte{a}); err != nil {
		t.Fatal(err)
	}
	if v, err := Version(pending); err != nil || v != "p1" {
		t.Errorf("pending version after a retirement = %q, %v; want it kept", v, err)
	}
	interrupted(t, pending)
	if err := Remove(pending); err != nil {
		t.Fatal(err)
	}
	if v, err := Version(pending); err != nil || v != "" {
		t.Errorf("pending version after the removal = %q, %v; want none", v, err)
	}
	if l := leftovers(t, pending); len(l) != 0 {
		t.Errorf("directories of the pending config left: %v", l)
	}
	if v, err := Version(Dir(root, a)); err != nil || v != "v1" {
		t.Errorf("slot aa… = %q, %v; want it kept", v, err)
	}
	if err := Remove(pending); err != nil {
		t.Errorf("remove it again: %v", err)
	}
}
