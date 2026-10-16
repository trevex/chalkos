package inventory

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestEncodeReadsBack writes an inventory that reads back as the same, its paths absolute, from
// another directory.
func TestEncodeReadsBack(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chalkos.yaml"), homelab)
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := Encode(inv.Name, inv.Spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "chalkos.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != inv.Name || !reflect.DeepEqual(again.Spec, inv.Spec) {
		t.Errorf("read back %+v, want %+v", again.Spec, inv.Spec)
	}
}
