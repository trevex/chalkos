package lab

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLabRecordsItsName records the lab's name, which names its state directory, and names the
// lab by it. The state directory is where the lab was read from, not part of the record.
func TestLabRecordsItsName(t *testing.T) {
	dir := t.TempDir()
	if err := (&Lab{Dir: dir, Name: "mylab"}).Write(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "lab.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"name": "mylab"`) || strings.Contains(string(data), dir) {
		t.Errorf("lab.json = %s", data)
	}
	l, err := ReadLab(dir)
	if err != nil || l.Name != "mylab" || l.Dir != dir {
		t.Fatalf("ReadLab = %+v, %v", l, err)
	}
	if _, err := l.Node("cp1"); err == nil || !strings.Contains(err.Error(), "the lab mylab has no node cp1") {
		t.Errorf("Node(cp1) = %v", err)
	}
}

// TestVMConfigForwards forwards the node's chalkd, a control plane's API server and the further
// ports of the lab file, in that order.
func TestVMConfigForwards(t *testing.T) {
	l := &Lab{Dir: t.TempDir()}
	got := l.VMConfig(Node{Name: "cp1", ChalkdPort: 15000, APIPort: 16443, Forwards: []Forward{{Host: 15005, Guest: 5000}}}).Forwards
	want := []Forward{{Host: 15000, Guest: ChalkdPort}, {Host: 16443, Guest: 6443}, {Host: 15005, Guest: 5000}}
	if !slices.Equal(got, want) {
		t.Errorf("forwards = %v, want %v", got, want)
	}
}
