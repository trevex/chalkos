package chalkd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

const (
	varUUID   = "7ad19bdf-77ff-4273-8a5c-d403d2a5f95b"
	extraUUID = "d506b831-fde9-4335-b2be-9710f18219a6"
	newUUID   = "1b9a3f0e-2c4d-4e5f-8a6b-7c8d9e0f1a2b"
	secret    = "tlvujiln-vffneuhk-idrhucle-evlgjnvd-ljvggbvr-djbthrbc-cgfrvhcb-vucrunlc"
)

// section renders a storage section with VAR and, when extraFormat is set, a volume on the disk
// with the serial chalk-extra.
func section(varSize, extraFormat string) string {
	size := "null"
	if varSize != "" {
		size = `"` + varSize + `"`
	}
	disks := `"system": {"ref": "/dev/vda", "seed": "2869f04c-5655-50f4-28b9-6b2eb9700a02", "repart": {"50-var.conf": "[Partition]\nLabel=var\n"}}`
	volumes := `"var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": ` + size + `}`
	if extraFormat != "" {
		disks += `, "extra": {"ref": {"serial": "chalk-extra"}, "seed": "088717f0-ffd4-dba5-a524-7d3d44310d1e", "repart": {"10-extra.conf": "[Partition]\nLabel=extra\n"}}`
		volumes += `, "extra": {"disk": "extra", "label": "extra", "format": "` + extraFormat + `", "mountPoint": "/srv/extra", "encryption": "tpm2", "size": null}`
	}
	return `{"disks": {` + disks + `}, "volumes": {` + volumes + `}, "fallback": "recovery-key", "encryption": "tpm2"}`
}

func identityWith(rack, storageSection string) string {
	return `{"hostname": "n1", "networkUnits": {}, "extensions": {"rack": {"location": "` + rack + `"}}, "storage": ` + storageSection + `}`
}

// installedServer is a normal-mode server whose STATE records the section and its pins.
func installedServer(t *testing.T, recorded string, extraPinned bool) (*Server, *fakeRunner) {
	t.Helper()
	s, r := newTestServer(t, normal, vda, vdb)
	dir := filepath.Join(s.Paths.StateDir, "storage")
	write(t, filepath.Join(dir, "storage.json"), recorded)
	pins := `"system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": {"var": "` + varUUID + `"}}`
	if extraPinned {
		pins += `, "extra": {"ref": {"serial": "chalk-extra"}, "identity": {"serial": "chalk-extra", "path": "pci-0000:00:05.0", "size": 17179869184, "type": "hdd"}, "partitions": {"extra": "` + extraUUID + `"}}`
	}
	write(t, filepath.Join(dir, "disks.json"), `{"disks": {`+pins+`}}`)
	write(t, filepath.Join(s.Paths.StateDir, "identity.json"), identityWith("rack-a", recorded))
	write(t, s.Identity.Consumers, `{"rack-location": {"keys": ["rack.location"], "restartOnChange": true}}`)
	r.rules = []rule{
		{prefix: "blkid -p -o export /dev/vdb", err: &node.ToolError{Command: "blkid", Code: 2}},
		{prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "extra"), out: `[{"file":"` + filepath.Join(dir, "disks", "extra", "10-extra.conf") + `","uuid":"` + newUUID + `"}]`},
		{prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "system"), out: `[{"file":"` + filepath.Join(dir, "disks", "system", "50-var.conf") + `","uuid":"` + varUUID + `"}]`},
		{prefix: "blkid -p -o value -s TYPE", out: "crypto_LUKS\n"},
	}
	return s, r
}

func apply(s *Server, identity, fallback string) (*nodev1.ApplyIdentityResponse, error) {
	resp, err := s.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{Identity: identity, FallbackSecret: fallback}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func TestApplyIdentityAddsVolume(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	next := identityWith("rack-b", section("", "ext4"))

	resp, err := apply(s, next, secret)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Paths.StateDir, "storage")
	partition := "/dev/disk/by-partuuid/" + newUUID
	enroll := "systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password " + partition
	want := []string{
		"udevadm settle --timeout=30",
		"blkid -p -o export /dev/vdb",
		"systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "system") + " --seed=2869f04c-5655-50f4-28b9-6b2eb9700a02 /dev/disk/chalk-boot-disk",
		"systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "extra") + " --seed=088717f0-ffd4-dba5-a524-7d3d44310d1e --empty=allow /dev/vdb",
		"udevadm settle --timeout=30",
		"blkid -p -o value -s TYPE " + partition,
		enroll,
		"systemctl daemon-reload",
		"systemctl start systemd-cryptsetup@extra.service srv-extra.mount",
		"networkctl reload",
		"systemctl try-restart rack-location",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("commands:\n got: %s\nwant: %s", strings.Join(r.calls, "\n      "), strings.Join(want, "\n      "))
	}
	if env := r.envs[enroll]; !reflect.DeepEqual(env, []string{"NEWPASSWORD=" + secret}) {
		t.Errorf("enrollment environment = %v", env)
	}
	if len(resp.Changes) != 1 || resp.Changes[0].Volume != "extra" || resp.Changes[0].Destructive ||
		!reflect.DeepEqual(resp.RestartedUnits, []string{"rack-location"}) {
		t.Errorf("response = %+v", resp)
	}
	recorded, err := storage.ReadSection(filepath.Join(dir, "storage.json"))
	if err != nil || recorded.Volumes["extra"].MountPoint != "/srv/extra" {
		t.Errorf("recorded section = %+v, %v", recorded, err)
	}
	if got, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json")); string(got) != next {
		t.Errorf("identity on STATE = %s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(s.Identity.RunDir, "credentials", "rack.location")); string(got) != "rack-b" {
		t.Errorf("credential = %q", got)
	}
}

func TestApplyIdentityWithoutStorageChanges(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	if _, err := apply(s, identityWith("rack-b", section("", "")), ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls, []string{"networkctl reload", "systemctl try-restart rack-location"}) {
		t.Errorf("commands = %v", r.calls)
	}
}

func TestApplyIdentityRefusesDestructiveChange(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	before, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json"))
	_, err := apply(s, identityWith("rack-b", section("1G", "")), secret)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "chalkctl storage reset <node> var") {
		t.Fatalf("err = %v, want a refusal naming the reset command", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v for a refused identity", r.calls)
	}
	if after, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json")); string(after) != string(before) {
		t.Error("recorded a refused identity")
	}
}

func TestApplyIdentityNeedsFallbackForEncryptedVolume(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	_, err := apply(s, identityWith("rack-a", section("", "ext4")), "")
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "no fallback secret") {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
	}
}

// partitionLink adds the extra volume's partition, at the given kernel name of a disk, and its
// by-partuuid link.
func partitionLink(t *testing.T, s *Server, disk, name, devnum string, number string) {
	t.Helper()
	dir := filepath.Join(s.Host.SysRoot, "block", disk, name)
	write(t, filepath.Join(dir, "partition"), number+"\n")
	write(t, filepath.Join(dir, "dev"), devnum+"\n")
	write(t, filepath.Join(dir, "size"), "2048\n")
	write(t, filepath.Join(s.Host.DevRoot, name), "")
	link := filepath.Join(s.Host.DevRoot, "disk", "by-partuuid", extraUUID)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../"+name, link); err != nil {
		t.Fatal(err)
	}
}

func reset(s *Server, volume, identity string) error {
	_, err := s.ResetVolume(context.Background(), connect.NewRequest(&nodev1.ResetVolumeRequest{Volume: volume, Identity: identity, FallbackSecret: secret}))
	return err
}

func TestResetVolume(t *testing.T) {
	s, r := installedServer(t, section("", "ext4"), true)
	partitionLink(t, s, "vdb", "vdb1", "253:17", "1")

	if err := reset(s, "extra", identityWith("rack-a", section("", "xfs"))); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Paths.StateDir, "storage")
	for i, want := range []string{
		"systemctl stop srv-extra.mount systemd-cryptsetup@extra.service",
		"wipefs --all /dev/disk/by-partuuid/" + extraUUID,
		"sfdisk --delete /dev/vdb 1",
		"blkid -p -o export /dev/vdb",
		"systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "system") + " --seed=2869f04c-5655-50f4-28b9-6b2eb9700a02 /dev/disk/chalk-boot-disk",
		"systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "extra") + " --seed=088717f0-ffd4-dba5-a524-7d3d44310d1e --empty=allow /dev/vdb",
	} {
		if i >= len(r.calls) || r.calls[i] != want {
			t.Fatalf("command %d = %q, want %q (all: %v)", i, r.calls[min(i, len(r.calls)-1)], want, r.calls)
		}
	}
	if !contains(r.calls, "systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password /dev/disk/by-partuuid/"+newUUID) ||
		!contains(r.calls, "systemctl start systemd-cryptsetup@extra.service srv-extra.mount") {
		t.Errorf("the volume was not created and opened again: %v", r.calls)
	}
	pins, err := storage.ReadPins(filepath.Join(dir, "disks.json"))
	if err != nil || pins.Disks["extra"].Partitions["extra"] != newUUID {
		t.Errorf("pins = %+v, %v", pins.Disks["extra"], err)
	}
	recorded, _ := storage.ReadSection(filepath.Join(dir, "storage.json"))
	if recorded.Volumes["extra"].Format != "xfs" {
		t.Errorf("recorded section = %+v", recorded.Volumes["extra"])
	}
}

func TestResetVolumeRefusals(t *testing.T) {
	for _, c := range []struct {
		name, volume, identity string
		// partitionOn is the disk the volume's partition link resolves to.
		partitionOn string
		want        string
	}{
		{"var", "var", identityWith("rack-a", section("1G", "ext4")), "vdb", "VAR"},
		{"unknown volume", "nope", identityWith("rack-a", section("", "ext4")), "vdb", "no volume nope"},
		{"other volumes change destructively", "extra", identityWith("rack-a", section("1G", "xfs")), "vdb", "other volumes"},
		{"partition on another disk", "extra", identityWith("rack-a", section("", "xfs")), "vda", "refusing to touch it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r := installedServer(t, section("", "ext4"), true)
			if c.partitionOn == "vdb" {
				partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
			} else {
				partitionLink(t, s, "vda", "vda9", "253:9", "9")
			}
			err := reset(s, c.volume, c.identity)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			for _, call := range r.calls {
				if strings.HasPrefix(call, "wipefs") || strings.HasPrefix(call, "sfdisk") {
					t.Errorf("changed a disk: %s", call)
				}
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
