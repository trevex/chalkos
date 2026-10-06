package storage

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestPinsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage", "disks.json")
	want := Pins{Disks: map[string]Pin{
		"longhorn": {
			Ref:        Ref{Selector: Selector{Model: "Samsung SSD 870*", Type: "ssd"}},
			Identity:   Identity{Serial: "S6PFNX0T100001", Model: "Samsung SSD 870 QVO 4TB", Size: 4 << 40, Type: "ssd"},
			Partitions: map[string]string{"longhorn": "d506b831-fde9-4335-b2be-9710f18219a6"},
		},
	}}

	if err := WritePins(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPins(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pins = %+v, want %+v", got, want)
	}
}

func TestReadPinsMissingFile(t *testing.T) {
	got, err := ReadPins(filepath.Join(t.TempDir(), "disks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Disks == nil || len(got.Disks) != 0 {
		t.Errorf("pins = %+v, want empty", got)
	}
}
