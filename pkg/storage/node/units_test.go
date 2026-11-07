package node

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnitsOf(t *testing.T) {
	st := &Storage{StateDir: t.TempDir(), BootPartitions: "/dev/disk/chalk-boot"}
	dir := st.StorageDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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
	units, err := st.Generate("systemd-cryptsetup")
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
	// A system-disk volume names the boot disk's link to its partition, not its PARTUUID, which
	// a copy of the system disk shares.
	if mount := units.files["srv-plain.mount"]; !strings.Contains(mount, "\nWhat=/dev/disk/chalk-boot/plain\n") {
		t.Errorf("srv-plain.mount does not mount /dev/disk/chalk-boot/plain:\n%s", mount)
	}
}
