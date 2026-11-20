package gpt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteTable writes a GPT that sfdisk verifies and reads back as written: types, UUIDs,
// names, sectors and the read-only attribute.
func TestWriteTable(t *testing.T) {
	if _, err := exec.LookPath("sfdisk"); err != nil {
		t.Skip("sfdisk not in PATH")
	}
	entries := []Entry{
		{Start: 1 << 20, Size: 4 << 20, Type: TypeESP, UUID: "dac4058a-2fc0-4808-a7ef-b8b4501c00ce", Name: "esp"},
		{Start: 5 << 20, Size: 1 << 20, Type: "77ff5f63-e7b6-4633-acf4-1565b864c0e6", UUID: "cec69e96-e85e-ec8d-cc96-7cd391cda76f", Name: "store-verity_ae887bd4972539c9", Attributes: AttrReadOnly},
		{Start: 6 << 20, Size: 2 << 20, Type: "8484680c-9521-48c6-9c11-b0720656f69e", UUID: "ae887bd4-9725-39c9-7a49-8acc64a067d9", Name: "store_ae887bd4972539c9", Attributes: AttrReadOnly},
	}
	path := filepath.Join(t.TempDir(), "disk.raw")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	size := TableSize(8 << 20)
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := WriteTable(f, size, "b581fef7-24ed-4f31-990b-099ec86bba03", entries); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sfdisk", "--verify", path).CombinedOutput(); err != nil {
		t.Fatalf("sfdisk --verify: %v: %s", err, out)
	}
	out, err := exec.Command("sfdisk", "--json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	parts, err := ParseTable(path, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != len(entries) {
		t.Fatalf("sfdisk read %d partitions, want %d", len(parts), len(entries))
	}
	for i, p := range parts {
		e := entries[i]
		if p.Start*512 != e.Start || p.Bytes() != e.Size || p.Type != e.Type || p.UUID != e.UUID || p.Name != e.Name {
			t.Errorf("partition %d is %+v, want %+v", i+1, p, e)
		}
	}
	dump, err := exec.Command("sfdisk", "--dump", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"label-id: B581FEF7-24ED-4F31-990B-099EC86BBA03", "first-lba: 2048", "last-lba: 16383", `name="store_ae887bd4972539c9", attrs="GUID:60"`} {
		if !strings.Contains(string(dump), want) {
			t.Errorf("sfdisk --dump prints no %q:\n%s", want, dump)
		}
	}
}

// TestWriteTableRefusesAPartitionOutsideTheDisk refuses a partition before 1 MiB or over the
// backup table.
func TestWriteTableRefusesAPartitionOutsideTheDisk(t *testing.T) {
	size := TableSize(8 << 20)
	for _, e := range []Entry{
		{Start: 512, Size: 1 << 20, Type: TypeESP, UUID: NewUUID(), Name: "esp"},
		{Start: 1 << 20, Size: 8 << 20, Type: TypeESP, UUID: NewUUID(), Name: "esp"},
	} {
		if err := WriteTable(discard{}, size, NewUUID(), []Entry{e}); err == nil || !strings.Contains(err.Error(), "outside the disk's usable sectors") {
			t.Errorf("%+v: err = %v", e, err)
		}
	}
}

type discard struct{}

func (discard) WriteAt(p []byte, _ int64) (int, error) { return len(p), nil }
