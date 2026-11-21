package node

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage"
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

// Volumes are mounted noexec unless they ask for exec. A volume mounted at an exec path takes the
// exec path's options, and the exec path gets no bind mount of its own.
func TestMountOptions(t *testing.T) {
	st := &Storage{BootPartitions: "/dev/disk/chalk-boot", ExecPaths: map[string]string{"/var/lib/kubelet": "exec,nosuid,dev"}}
	for _, c := range []struct {
		name string
		v    storage.Volume
		want string
	}{
		{"default", storage.Volume{MountPoint: "/srv/data"}, "nosuid,nodev,noexec"},
		{"exec", storage.Volume{MountPoint: "/srv/data", Exec: true}, "exec,nosuid,nodev"},
		{"at an exec path", storage.Volume{MountPoint: "/var/lib/kubelet"}, "exec,nosuid,dev"},
	} {
		if got := st.mountOptions(c.v); got != c.want {
			t.Errorf("%s: options = %q, want %q", c.name, got, c.want)
		}
	}

	st.StateDir = t.TempDir()
	if err := os.MkdirAll(st.StorageDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"storage.json": `{"disks": {"system": {"ref": "/dev/vda", "seed": "s", "repart": {}}}, "volumes": {
		  "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "none", "size": null},
		  "kubelet": {"disk": "system", "label": "kubelet", "format": "ext4", "mountPoint": "/var/lib/kubelet", "encryption": "none", "size": null}
		}, "fallback": "none"}`,
		"disks.json": `{"disks": {"system": {"ref": "/dev/vda", "identity": {"path": "p", "size": 1, "type": "hdd"}, "partitions": {"var": "a", "kubelet": "b"}}}}`,
	} {
		if err := os.WriteFile(filepath.Join(st.StorageDir(), name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	units, err := st.Generate("systemd-cryptsetup")
	if err != nil {
		t.Fatal(err)
	}
	mount := units.files["var-lib-kubelet.mount"]
	if !strings.Contains(mount, "\nWhat=/dev/disk/chalk-boot/kubelet\n") || !strings.Contains(mount, "\nOptions=nofail,exec,nosuid,dev\n") {
		t.Errorf("var-lib-kubelet.mount does not mount the volume with the exec path's options:\n%s", mount)
	}
}
