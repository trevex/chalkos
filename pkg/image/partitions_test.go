package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAndFindPartitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repart-output.json")
	json := `[
	  {"type":"esp","label":"esp","offset":1048576,"raw_size":268435456},
	  {"type":"usr-x86-64-verity","label":"store-verity","offset":269484032,"raw_size":67108864,"roothash":"ab"},
	  {"type":"usr-x86-64","label":"store","offset":336592896,"raw_size":2147483648}
	]`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}

	parts, err := ReadPartitions(path)
	if err != nil {
		t.Fatal(err)
	}
	esp, err := FindPartition(parts, "esp")
	if err != nil || esp.Offset != 1048576 || esp.RawSize != 268435456 {
		t.Fatalf("esp = %+v, %v", esp, err)
	}
	if _, err := FindPartition(parts, "var"); err == nil {
		t.Fatal("found a partition type that does not exist")
	}
}
