package node

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUnitsOf(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"storage.json": `{
		  "disks": {
		    "system": {"ref": "/dev/vda", "seed": "s", "repart": {}},
		    "data": {"ref": {"serial": "chalk-data"}, "seed": "d", "repart": {}}
		  },
		  "volumes": {
		    "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": null},
		    "data": {"disk": "data", "label": "data", "format": "xfs", "mountPoint": "/var/lib/data", "encryption": "tpm2", "size": null},
		    "plain": {"disk": "system", "label": "plain", "format": "ext4", "mountPoint": "/srv/plain", "encryption": "none", "size": "1G"}
		  },
		  "fallback": "recovery-key"
		}`,
		"disks.json": `{"disks": {
		  "system": {"ref": "/dev/vda", "identity": {"path": "p", "size": 1, "type": "hdd"}, "partitions": {"var": "a", "plain": "b"}},
		  "data": {"ref": {"serial": "chalk-data"}, "identity": {"serial": "chalk-data", "size": 1, "type": "hdd"}, "partitions": {"data": "c"}}
		}}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	units, err := Generate(dir, "systemd-cryptsetup")
	if err != nil {
		t.Fatal(err)
	}
	for volume, want := range map[string][]string{
		"data":  {"systemd-cryptsetup@data.service", "var-lib-data.mount"},
		"plain": {`srv-plain.mount`},
		"var":   nil,
	} {
		if got := units.Of(volume); !reflect.DeepEqual(got, want) {
			t.Errorf("units of %s = %v, want %v", volume, got, want)
		}
	}
}
