package chalkd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/pki"
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
	return `{"hostname": "n1", "cluster": "lab", "role": "worker", "platform": "metal", "networkUnits": {}, "extensions": {"rack": {"location": "` + rack + `"}}, "storage": ` + storageSection + `}`
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
		{prefix: "cryptsetup luksDump --dump-json-metadata", out: luksDump},
	}
	return s, r
}

// luksDump is the header of a LUKS2 device with the TPM2 keyslot 0 and the password keyslot 2.
const luksDump = `{"keyslots": {"0": {"type": "luks2"}, "2": {"type": "luks2"}}, "tokens": {"0": {"type": "systemd-tpm2", "keyslots": ["0"]}}, "segments": {}}`

const (
	stateDevice = "/dev/disk/chalk-boot/state"
	dumpState   = "cryptsetup luksDump --dump-json-metadata " + stateDevice
	testState   = "cryptsetup open --test-passphrase --key-slot=2 --key-file=- " + stateDevice
)

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
		dumpState,
		testState,
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
	if r.inputs[testState] != secret {
		t.Errorf("the secret was not checked against STATE's password keyslot on standard input: %v", r.inputs)
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

// TestApplyIdentityRefusesAnotherImage refuses an identity of another cluster, role or platform
// than the running image's.
func TestApplyIdentityRefusesAnotherImage(t *testing.T) {
	for _, tc := range []struct{ identity, want string }{
		{strings.Replace(identityWith("rack-b", section("", "")), `"cluster": "lab"`, `"cluster": "prod"`, 1),
			`the identity is of the cluster "prod" and the role "worker", but the node's image is of "lab" and "worker"`},
		{strings.Replace(identityWith("rack-b", section("", "")), `"role": "worker"`, `"role": "controlplane"`, 1),
			`the identity is of the cluster "lab" and the role "controlplane", but the node's image is of "lab" and "worker"`},
		{strings.Replace(identityWith("rack-b", section("", "")), `"platform": "metal"`, `"platform": "kvm"`, 1),
			`the identity names the platform "kvm", but the node's image is built for "metal"`},
	} {
		s, r := installedServer(t, section("", ""), false)
		before, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json"))
		_, err := apply(s, tc.identity, secret)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("err = %v, want %q", err, tc.want)
		}
		if len(r.calls) != 0 {
			t.Errorf("ran %v for a refused identity", r.calls)
		}
		if after, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json")); string(after) != string(before) {
			t.Error("recorded a refused identity")
		}
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
	addPartition(t, s, disk, name, devnum, number, extraUUID, "disk/by-partuuid/"+extraUUID)
}

// addPartition adds a partition with the PARTUUID udev reports for it, and a link below /dev.
func addPartition(t *testing.T, s *Server, disk, name, devnum, number, uuid, link string) {
	t.Helper()
	dir := filepath.Join(s.Host.SysRoot, "block", disk, name)
	write(t, filepath.Join(dir, "partition"), number+"\n")
	write(t, filepath.Join(dir, "dev"), devnum+"\n")
	write(t, filepath.Join(dir, "size"), "2048\n")
	write(t, filepath.Join(s.Host.UdevRoot, "b"+devnum), "E:ID_PART_ENTRY_UUID="+uuid+"\n")
	write(t, filepath.Join(s.Host.DevRoot, name), "")
	path := filepath.Join(s.Host.DevRoot, link)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(strings.Repeat("../", strings.Count(link, "/"))+name, path); err != nil {
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
		dumpState,
		testState,
		"systemctl stop srv-extra.mount systemd-cryptsetup@extra.service",
		"wipefs --all /dev/vdb1",
		"sfdisk --lock --delete /dev/vdb 1",
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

// editSection returns a rendered storage section changed by f.
func editSection(t *testing.T, rendered string, f func(s *storage.Section)) string {
	t.Helper()
	var s storage.Section
	if err := json.Unmarshal([]byte(rendered), &s); err != nil {
		t.Fatal(err)
	}
	f(&s)
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// withSystemVolume adds the volume data to the system disk, encrypted as given.
func withSystemVolume(t *testing.T, rendered, encryption string) string {
	return editSection(t, rendered, func(s *storage.Section) {
		s.Disks[storage.SystemDisk].Repart["60-data.conf"] = "[Partition]\nLabel=data\n"
		s.Volumes["data"] = storage.Volume{Disk: storage.SystemDisk, Label: "data", Format: "ext4", MountPoint: "/srv/data", Encryption: encryption, Size: "1G"}
	})
}

// invalidSections break the rules a node checks a delivered section by.
func invalidSections(t *testing.T) map[string]string {
	return map[string]string{
		"disk name leaving the directory": editSection(t, section("", "ext4"), func(s *storage.Section) {
			s.Disks["../x"] = s.Disks["extra"]
		}),
		"definition file with a slash": editSection(t, section("", "ext4"), func(s *storage.Section) {
			s.Disks["extra"].Repart["../../x.conf"] = "[Partition]\n"
		}),
		"label of another partition": editSection(t, section("", "ext4"), func(s *storage.Section) {
			s.Volumes["data"] = storage.Volume{Disk: storage.SystemDisk, Label: "state", Format: "ext4", Encryption: "none", Size: "1G"}
		}),
	}
}

func TestApplyIdentityRefusesInvalidSection(t *testing.T) {
	for name, invalid := range invalidSections(t) {
		t.Run(name, func(t *testing.T) {
			s, r := installedServer(t, section("", "ext4"), true)
			_, err := apply(s, identityWith("rack-b", invalid), secret)
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "invalid storage section") {
				t.Fatalf("err = %v", err)
			}
			if len(r.calls) != 0 {
				t.Errorf("ran %v", r.calls)
			}
		})
	}
}

func TestResetVolumeRefusesInvalidSection(t *testing.T) {
	for name, invalid := range invalidSections(t) {
		t.Run(name, func(t *testing.T) {
			s, r := installedServer(t, section("", "ext4"), true)
			partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
			err := reset(s, "extra", identityWith("rack-a", invalid))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "invalid storage section") {
				t.Fatalf("err = %v", err)
			}
			if len(r.calls) != 0 {
				t.Errorf("ran %v", r.calls)
			}
		})
	}
}

// systemVolumeServer is an installed server whose system disk carries the volume data at vda9.
func systemVolumeServer(t *testing.T, partUUID string) (*Server, *fakeRunner) {
	t.Helper()
	recorded := withSystemVolume(t, section("", ""), storage.EncryptionTPM2)
	s, r := installedServer(t, recorded, false)
	dir := filepath.Join(s.Paths.StateDir, "storage")
	write(t, filepath.Join(dir, "disks.json"), `{"disks": {"system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": {"var": "`+varUUID+`", "data": "`+extraUUID+`"}}}}`)
	addPartition(t, s, "vda", "vda9", "253:9", "9", partUUID, "disk/chalk-boot/data")
	return s, r
}

func TestResetVolumeOnSystemDisk(t *testing.T) {
	s, r := systemVolumeServer(t, extraUUID)
	next := editSection(t, withSystemVolume(t, section("", ""), storage.EncryptionTPM2), func(s *storage.Section) {
		v := s.Volumes["data"]
		v.Format = "xfs"
		s.Volumes["data"] = v
	})
	if err := reset(s, "data", identityWith("rack-a", next)); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{
		dumpState,
		testState,
		"systemctl stop srv-data.mount systemd-cryptsetup@data.service",
		"wipefs --all /dev/vda9",
		"sfdisk --lock --delete /dev/vda 9",
	} {
		if i >= len(r.calls) || r.calls[i] != want {
			t.Fatalf("command %d: want %q (all: %v)", i, want, r.calls)
		}
	}
}

func TestResetVolumeChecksPartUUID(t *testing.T) {
	const other = "0f1e2d3c-4b5a-4968-8776-655443322110"
	next := identityWith("rack-a", section("", "xfs"))
	for _, c := range []struct {
		name  string
		setUp func(t *testing.T) (*Server, *fakeRunner, string)
		want  string
	}{
		{"pinned disk, partition with another PARTUUID", func(t *testing.T) (*Server, *fakeRunner, string) {
			s, r := installedServer(t, section("", "ext4"), true)
			addPartition(t, s, "vdb", "vdb1", "253:17", "1", other, "disk/by-partuuid/"+extraUUID)
			return s, r, next
		}, "PARTUUID"},
		{"pinned disk, no recorded PARTUUID", func(t *testing.T) (*Server, *fakeRunner, string) {
			s, r := installedServer(t, section("", "ext4"), true)
			partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
			write(t, filepath.Join(s.Paths.StateDir, "storage", "disks.json"), `{"disks": {"system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": {"var": "`+varUUID+`"}}, "extra": {"ref": {"serial": "chalk-extra"}, "identity": {"serial": "chalk-extra", "path": "pci-0000:00:05.0", "size": 17179869184, "type": "hdd"}, "partitions": {}}}}`)
			return s, r, next
		}, "no recorded partition"},
		{"system disk, partition with another PARTUUID", func(t *testing.T) (*Server, *fakeRunner, string) {
			s, r := systemVolumeServer(t, other)
			return s, r, identityWith("rack-a", withSystemVolume(t, section("", ""), storage.EncryptionTPM2))
		}, "PARTUUID"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r, identity := c.setUp(t)
			volume := "extra"
			if strings.HasPrefix(c.name, "system") {
				volume = "data"
			}
			err := reset(s, volume, identity)
			if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if changed := withoutChecks(r.calls); len(changed) != 0 {
				t.Errorf("ran %v", changed)
			}
		})
	}
}

func TestResetVolumeNeedsFallbackForEveryEncryptedVolume(t *testing.T) {
	s, r := installedServer(t, section("", "ext4"), true)
	partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
	// The reset volume becomes unencrypted, but the identity adds the encrypted volume data.
	next := withSystemVolume(t, editSection(t, section("", "xfs"), func(s *storage.Section) {
		v := s.Volumes["extra"]
		v.Encryption = storage.EncryptionNone
		s.Volumes["extra"] = v
	}), storage.EncryptionTPM2)
	_, err := s.ResetVolume(context.Background(), connect.NewRequest(&nodev1.ResetVolumeRequest{Volume: "extra", Identity: identityWith("rack-a", next)}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "volume data") || !strings.Contains(err.Error(), "no fallback secret") {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
	}
}

func TestResetVolumeRefusesRemoval(t *testing.T) {
	s, r := installedServer(t, section("", "ext4"), true)
	partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
	err := reset(s, "extra", identityWith("rack-a", section("", "")))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "no longer has volume extra") {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
	}
}

// withoutChecks drops the commands that only read a device to check the fallback secret.
func withoutChecks(calls []string) []string {
	var out []string
	for _, c := range calls {
		if !strings.HasPrefix(c, "cryptsetup luksDump ") && !strings.HasPrefix(c, "cryptsetup open --test-passphrase ") {
			out = append(out, c)
		}
	}
	return out
}

// plainSystem makes STATE and VAR unencrypted.
func plainSystem(s *storage.Section) {
	s.Encryption = storage.EncryptionNone
	v := s.Volumes[storage.VarVolume]
	v.Encryption = storage.EncryptionNone
	s.Volumes[storage.VarVolume] = v
}

// mismatch makes cryptsetup reject every passphrase, as it does a wrong one.
func mismatch(r *fakeRunner) {
	r.rules = append([]rule{{prefix: "cryptsetup open --test-passphrase", err: &node.ToolError{Command: "cryptsetup open", Code: 2, Stderr: "No key available with this passphrase."}}}, r.rules...)
}

func TestApplyIdentityRefusesMismatchingFallbackSecret(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	before, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json"))
	mismatch(r)
	_, err := apply(s, identityWith("rack-b", section("", "ext4")), "mistyped")
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "the fallback secret does not match the node's existing fallback keyslot") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(r.calls, []string{dumpState, testState}) {
		t.Errorf("commands = %v, want only the check", r.calls)
	}
	if r.inputs[testState] != "mistyped" {
		t.Errorf("inputs = %v", r.inputs)
	}
	if after, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json")); string(after) != string(before) {
		t.Error("recorded a refused identity")
	}
}

func TestApplyIdentityRefusesWhenTheCheckFails(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	r.rules = append([]rule{{prefix: "cryptsetup open", err: &node.ToolError{Command: "cryptsetup open", Code: 4, Stderr: "Device does not exist."}}}, r.rules...)
	_, err := apply(s, identityWith("rack-b", section("", "ext4")), secret)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(r.calls, []string{dumpState, testState}) {
		t.Errorf("commands = %v, want only the check", r.calls)
	}
}

func TestResetVolumeRefusesMismatchingFallbackSecret(t *testing.T) {
	s, r := installedServer(t, section("", "ext4"), true)
	partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
	mismatch(r)
	err := reset(s, "extra", identityWith("rack-a", section("", "xfs")))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(r.calls, []string{dumpState, testState}) {
		t.Errorf("commands = %v, want only the check", r.calls)
	}
}

// TestFallbackCheckDevice checks the secret against STATE, else VAR, else another encrypted
// volume the node keeps, and skips the check when the node has none.
func TestFallbackCheckDevice(t *testing.T) {
	addData := func(rendered string) string { return withSystemVolume(t, rendered, storage.EncryptionTPM2) }
	for _, c := range []struct {
		name               string
		recorded, next     string
		extraPinned, reset bool
		want               string
	}{
		{"STATE", section("", ""), section("", "ext4"), false, false, stateDevice},
		{"VAR", editSection(t, section("", ""), func(s *storage.Section) {
			s.Encryption = storage.EncryptionNone
		}), editSection(t, section("", "ext4"), func(s *storage.Section) {
			s.Encryption = storage.EncryptionNone
		}), false, false, "/dev/disk/chalk-boot/var"},
		{"another volume", editSection(t, section("", "ext4"), plainSystem), addData(editSection(t, section("", "ext4"), plainSystem)), true, false, "/dev/disk/by-partuuid/" + extraUUID},
		{"none encrypted", editSection(t, section("", ""), plainSystem), editSection(t, section("", "ext4"), plainSystem), false, false, ""},
		{"no fallback recorded", editSection(t, section("", ""), func(s *storage.Section) {
			s.Fallback = storage.FallbackNone
		}), section("", "ext4"), false, false, ""},
		{"only the reset volume encrypted", editSection(t, section("", "ext4"), plainSystem), editSection(t, section("", "xfs"), plainSystem), true, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r := installedServer(t, c.recorded, c.extraPinned)
			var err error
			if c.reset {
				partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
				err = reset(s, "extra", identityWith("rack-a", c.next))
			} else {
				_, err = apply(s, identityWith("rack-a", c.next), secret)
			}
			if err != nil {
				t.Fatal(err)
			}
			var checks []string
			for _, call := range r.calls {
				if strings.HasPrefix(call, "cryptsetup") {
					checks = append(checks, call)
				}
			}
			var want []string
			if c.want != "" {
				want = []string{"cryptsetup luksDump --dump-json-metadata " + c.want, "cryptsetup open --test-passphrase --key-slot=2 --key-file=- " + c.want}
			}
			if !reflect.DeepEqual(checks, want) {
				t.Errorf("checks = %v, want %v", checks, want)
			}
			if !contains(r.calls, "systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password /dev/disk/by-partuuid/"+newUUID) &&
				!contains(r.calls, "systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password /dev/disk/chalk-boot/data") {
				t.Errorf("no fallback keyslot was enrolled: %v", r.calls)
			}
		})
	}
}

func TestPasswordSlots(t *testing.T) {
	for dump, want := range map[string][]string{
		luksDump: {"2"},
		`{"keyslots": {"0": {}, "1": {}, "10": {}}, "tokens": {"0": {"type": "systemd-tpm2", "keyslots": ["0"]}, "1": {"type": "systemd-recovery", "keyslots": ["10"]}}}`: {"1"},
		`{"keyslots": {"0": {}}, "tokens": {"0": {"type": "systemd-tpm2", "keyslots": ["0"]}}}`:                                                                           nil,
		`{"keyslots": {"3": {}, "1": {}}, "tokens": {}}`:                                                                                                                  {"1", "3"},
	} {
		got, err := passwordSlots([]byte(dump))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("passwordSlots(%s) = %v, %v, want %v", dump, got, err, want)
		}
	}
	if _, err := passwordSlots([]byte(`{"keyslots": {"0; rm": {}}}`)); err == nil {
		t.Error("accepted a keyslot that is not a number")
	}
}

// withNodeCertificate gives s a node certificate for n1 from a node CA of the test OS CA, and
// returns that node CA.
func withNodeCertificate(t *testing.T, s *Server) pki.CertKey {
	t.Helper()
	nodeCA := testNodeCA(t)
	cert, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Paths.StateDir, "chalkd")
	write(t, filepath.Join(dir, NodeCertificateFile), cert.Certificate+cert.Key)
	if s.Certificate, err = LoadNodeCertificate(dir); err != nil {
		t.Fatal(err)
	}
	return nodeCA
}

func TestApplyIdentityDeliversNodeCertificateAlone(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	nodeCA := withNodeCertificate(t, s)
	idPath := filepath.Join(s.Paths.StateDir, "identity.json")
	recorded, _ := os.ReadFile(idPath)
	before := s.Certificate.Fingerprint()
	renewed, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, _ := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n2"}, time.Now())
	for name, req := range map[string]*nodev1.ApplyIdentityRequest{
		"nothing":                    {},
		"another node's certificate": {NodeCertificate: []byte(other.Certificate), NodeKey: []byte(other.Key)},
		"another key":                {NodeCertificate: []byte(renewed.Certificate), NodeKey: []byte(other.Key)},
		// Refused before the identity is applied.
		"another node's with an identity": {Identity: identityWith("rack-b", section("", "")), NodeCertificate: []byte(other.Certificate), NodeKey: []byte(other.Key)},
	} {
		_, err := s.ApplyIdentity(context.Background(), connect.NewRequest(req))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want invalid_argument", name, err)
		} else if strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("%s: the error holds a key", name)
		}
	}
	if s.Certificate.Fingerprint() != before || len(r.calls) != 0 {
		t.Fatalf("a refused request changed the certificate or ran %v", r.calls)
	}

	if _, err := s.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{
		NodeCertificate: []byte(renewed.Certificate), NodeKey: []byte(renewed.Key),
	})); err != nil {
		t.Fatal(err)
	}
	if s.Certificate.Fingerprint() == before {
		t.Error("the delivered certificate is not served")
	}
	if got, _ := os.ReadFile(idPath); string(got) != string(recorded) || len(r.calls) != 0 {
		t.Errorf("delivering a certificate alone changed the identity or ran %v", r.calls)
	}
}

func TestApplyIdentityReloadsTimeServers(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	withTime := func(host string) string {
		return `{"hostname": "n1", "cluster": "lab", "role": "worker", "platform": "metal", "networkUnits": {}, "storage": ` + section("", "") + `, "time": {"servers": [{"host": "` + host + `", "nts": true}]}}`
	}
	count := func() int {
		n := 0
		for _, c := range r.calls {
			if c == "chronyc reload sources" {
				n++
			}
		}
		return n
	}
	apply := func(id string) error {
		_, err := s.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{Identity: id}))
		return err
	}
	if err := apply(withTime("ptbtime1.ptb.de")); err != nil {
		t.Fatal(err)
	}
	if err := apply(withTime("ptbtime1.ptb.de")); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Errorf("chrony reloaded its sources %d times, want once for one change: %v", count(), r.calls)
	}
	if err := apply(withTime("a b")); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a time server that is no host: %v", err)
	}
}
