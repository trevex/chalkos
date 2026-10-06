package storage

import (
	"reflect"
	"testing"
)

// Output of systemd-repart 261 --json=short for a disk with one new and one existing partition.
const repartOutput = `[{"type":"63dd1eb4-1dd2-0d0b-0c1e-27c204214ec8","label":"data","uuid":"d506b831-fde9-4335-b2be-9710f18219a6","partno":0,"file":"/sysroot/state/storage/disks/data/10-data.conf","node":"/dev/vdb1","offset":1048576,"old_size":0,"raw_size":16777216,"old_padding":0,"raw_padding":0,"activity":"create"},{"type":"65f335d7-a1f7-f6df-b954-a97d9a5db9e6","label":"var","uuid":"7ad19bdf-77ff-4273-8a5c-d403d2a5f95b","partno":1,"file":"/sysroot/state/storage/disks/system/50-var.conf","node":"/dev/vda7","offset":17825792,"old_size":49262592,"raw_size":49262592,"old_padding":0,"raw_padding":0,"activity":"unchanged"}]`

func TestParsePartitions(t *testing.T) {
	got, err := ParsePartitions([]byte(repartOutput))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"data": "d506b831-fde9-4335-b2be-9710f18219a6",
		"var":  "7ad19bdf-77ff-4273-8a5c-d403d2a5f95b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("partitions = %v, want %v", got, want)
	}
	if _, err := ParsePartitions([]byte("Can't fit requested partitions")); err == nil {
		t.Error("non-JSON output accepted")
	}
}
