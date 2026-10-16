package lab

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestVMDisks gives a VM its disks in the lab's order, the first backed by the node's image and
// the others empty with their serials, and one disk to a node recorded without disks.
func TestVMDisks(t *testing.T) {
	l := &Lab{}
	n := LabNode{Name: "w1", Disks: []LabDisk{{Size: "16G"}, {Size: "1G", Serial: "chalk-data"}}}
	want := []Disk{{Path: filepath.Join("/lab", "w1", "disk.qcow2")}, {Path: filepath.Join("/lab", "w1", "disk1.qcow2"), Serial: "chalk-data"}}
	if got := l.VMConfig("/lab", n).Disks; !reflect.DeepEqual(got, want) {
		t.Errorf("disks = %+v, want %+v", got, want)
	}
	if got := l.VMConfig("/lab", LabNode{Name: "w1"}).Disks; !reflect.DeepEqual(got, want[:1]) {
		t.Errorf("disks of a node without any = %+v", got)
	}
}
