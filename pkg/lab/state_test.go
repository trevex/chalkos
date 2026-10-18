package lab

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestLabRecordsItsName records the lab's name, which names its state directory, and names the
// lab by it.
func TestLabRecordsItsName(t *testing.T) {
	dir := t.TempDir()
	if err := (&Lab{Name: "mylab"}).Write(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "lab.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"name": "mylab"`) || strings.Contains(string(data), "cluster") {
		t.Errorf("lab.json = %s", data)
	}
	l, err := ReadLab(dir)
	if err != nil || l.Name != "mylab" {
		t.Fatalf("ReadLab = %+v, %v", l, err)
	}
	if _, err := l.Node("cp1"); err == nil || !strings.Contains(err.Error(), "the lab mylab has no node cp1") {
		t.Errorf("Node(cp1) = %v", err)
	}
}
