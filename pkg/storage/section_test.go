package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sectionJSON = `{
  "disks": {
    "system": {
      "ref": "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100002",
      "seed": "94e63e0a-dc65-7786-02fb-a2e8001f4cfb",
      "repart": { "50-var.conf": "[Partition]\nLabel=var\n" }
    },
    "longhorn": {
      "ref": { "model": "Samsung SSD 870*", "type": "ssd" },
      "seed": "7dc5cf32-67a9-6501-acce-377a47f9df6c",
      "repart": { "10-longhorn.conf": "[Partition]\nLabel=longhorn\n" }
    }
  },
  "volumes": {
    "var": { "disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": "200G" },
    "longhorn": { "disk": "longhorn", "label": "longhorn", "format": "xfs", "mountPoint": "/var/lib/longhorn", "encryption": "tpm2", "size": null },
    "scratch": { "disk": "system", "label": "scratch", "format": null, "mountPoint": null, "encryption": "none", "size": "10G" }
  },
  "fallback": "recovery-key"
}`

func writeSection(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadSection(t *testing.T) {
	s, err := ReadSection(writeSection(t, sectionJSON))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Disks["system"].Ref; got.Path != "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100002" {
		t.Errorf("system ref = %+v", got)
	}
	if got, want := s.Disks["longhorn"].Ref, (Ref{Selector: Selector{Model: "Samsung SSD 870*", Type: "ssd"}}); got != want {
		t.Errorf("longhorn ref = %+v, want %+v", got, want)
	}
	if got, want := s.Volumes["scratch"], (Volume{Disk: "system", Label: "scratch", Encryption: "none", Size: "10G"}); got != want {
		t.Errorf("scratch = %+v, want %+v", got, want)
	}
	if got := s.Volumes["longhorn"].Size; got != "" {
		t.Errorf("longhorn size = %q, want empty for null", got)
	}
	if s.Fallback != "recovery-key" {
		t.Errorf("fallback = %q", s.Fallback)
	}
	if got, want := s.DiskNames(), []string{"system", "longhorn"}; !reflect.DeepEqual(got, want) {
		t.Errorf("disk names = %v, want %v", got, want)
	}
}

func TestReadSectionMissing(t *testing.T) {
	_, err := ReadSection(filepath.Join(t.TempDir(), "storage.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestReadSectionIgnoresUnknownFields(t *testing.T) {
	// A section written by a newer chalkos, which an older image reads after a rollback.
	s, err := ReadSection(writeSection(t, `{
	  "disks": {"d": {"ref": {"model": "Samsung*", "vendor": "x"}, "seed": "s", "repart": {}, "layout": 2}},
	  "volumes": {"d": {"disk": "d", "label": "d", "format": "ext4", "mountPoint": null, "encryption": "none", "size": null, "options": ["ro"]}},
	  "fallback": "none",
	  "surprise": 1
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Disks["d"]; got.Ref.Selector.Model != "Samsung*" || got.Seed != "s" {
		t.Errorf("disk = %+v", got)
	}
	if got, want := s.Volumes["d"], (Volume{Disk: "d", Label: "d", Format: "ext4", Encryption: "none"}); got != want {
		t.Errorf("volume = %+v, want %+v", got, want)
	}
	// Resolving by the known keys alone could pick another disk.
	if _, err := s.Disks["d"].Ref.Selector.Matches(Identity{Model: "Samsung SSD"}); err == nil || !strings.Contains(err.Error(), "vendor") {
		t.Errorf("Matches err = %v, want the unknown key named", err)
	}
}

func TestRefMarshalRoundTrip(t *testing.T) {
	for _, ref := range []Ref{{Path: "/dev/sda"}, {Selector: Selector{Serial: "S1", Size: ">= 1T"}}} {
		data, err := ref.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var got Ref
		if err := got.UnmarshalJSON(data); err != nil {
			t.Fatal(err)
		}
		if got != ref {
			t.Errorf("round trip of %s = %+v", data, got)
		}
	}
}

func TestWriteDefinitions(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "system", "60-old.conf")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("[Partition]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSection(writeSection(t, sectionJSON))
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteDefinitions(dir, s); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "longhorn", "10-longhorn.conf"))
	if err != nil || string(got) != "[Partition]\nLabel=longhorn\n" {
		t.Errorf("10-longhorn.conf = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "system", "50-var.conf")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale definition kept: %v", err)
	}
}

func TestVolumeOfDefinition(t *testing.T) {
	for file, want := range map[string]string{
		"50-var.conf":      "var",
		"60-my-data.conf":  "my-data",
		"10-longhorn.conf": "longhorn",
	} {
		if got := volumeOfDefinition(file); got != want {
			t.Errorf("volumeOfDefinition(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestRefString(t *testing.T) {
	got := Ref{Selector: Selector{Model: "Samsung*", Type: "nvme"}}.String()
	if !strings.Contains(got, `model "Samsung*"`) || !strings.Contains(got, `type "nvme"`) {
		t.Errorf("String() = %s", got)
	}
}

func TestPartitionTypes(t *testing.T) {
	d := Disk{Repart: map[string]string{
		"50-var.conf":  "[Partition]\nType=7AD19BDF-77FF-4273-8A5C-D403D2A5F95B\nLabel=var\n",
		"60-data.conf": "[Partition]\nLabel=data\nType = d506b831-fde9-4335-b2be-9710f18219a6\nEncrypt=tpm2\n",
		"70-note.conf": "# Type=0fc63daf-8483-4772-8e79-3d69d8477de4\n[Partition]\nLabel=note\n",
	}}
	got := d.PartitionTypes()
	want := map[string]bool{
		"7ad19bdf-77ff-4273-8a5c-d403d2a5f95b": true,
		"d506b831-fde9-4335-b2be-9710f18219a6": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PartitionTypes() = %v, want %v", got, want)
	}
}
