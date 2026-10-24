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
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
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

func identityWith(storageSection string) string {
	return `{"hostname": "n1", "platform": "metal", "storage": ` + storageSection + `}`
}

// documentsOf is the config of node n1 whose storage section an identity renders: the SystemDisk
// and Volume documents that translate into the section, with their defaults. The storage rules read these documents; the identity is
// what the tests deliver; one that does not parse, which the node refuses first, has none.
func documentsOf(identity string) []config.Document {
	var id struct {
		Storage storage.Section `json:"storage"`
	}
	if err := json.Unmarshal([]byte(identity), &id); err != nil {
		return nil
	}
	sec := id.Storage
	ref := func(r storage.Ref) map[string]any {
		m := map[string]any{"path": nil, "model": nil, "serial": nil, "wwn": nil, "size": nil, "type": nil}
		for key, v := range map[string]string{"path": r.Path, "model": r.Selector.Model, "serial": r.Selector.Serial, "wwn": r.Selector.WWN, "size": r.Selector.Size, "type": r.Selector.Type} {
			if v != "" {
				m[key] = v
			}
		}
		return m
	}
	orNull := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	// A volume's mode is the node's unless it overrides it.
	mode := func(m string) any {
		if m == sec.Encryption {
			return nil
		}
		return m
	}
	doc := func(apiVersion, kind, name string, spec map[string]any) config.Document {
		return config.FromObject(map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}, "spec": spec})
	}
	v := sec.Volumes[storage.VarVolume]
	docs := []config.Document{
		doc(config.StorageAPI, "SystemDisk", "system", map[string]any{
			"disk":       ref(sec.Disks[storage.SystemDisk].Ref),
			"encryption": map[string]any{"mode": sec.Encryption, "fallback": sec.Fallback},
			"var":        map[string]any{"size": orNull(v.Size), "encryption": map[string]any{"mode": mode(v.Encryption)}},
		}),
	}
	for _, name := range sortedNames(sec.Volumes) {
		vol := sec.Volumes[name]
		if name == storage.VarVolume {
			continue
		}
		var disk any
		if vol.Disk != storage.SystemDisk {
			disk = ref(sec.Disks[vol.Disk].Ref)
		}
		docs = append(docs, doc(config.StorageAPI, "Volume", name, map[string]any{
			"disk": disk, "size": orNull(vol.Size), "format": orNull(vol.Format), "mountPoint": orNull(vol.MountPoint),
			"encryption": map[string]any{"mode": mode(vol.Encryption)}, "repart": map[string]any{},
		}))
	}
	return docs
}

// installedServer is a normal-mode server of node n1, as its node certificate names it, whose
// STATE records the section and its pins.
func installedServer(t *testing.T, recorded string, extraPinned bool, more ...testDisk) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := newTestServer(t, normal, append([]testDisk{vda, vdb}, more...)...)
	withNodeCertificate(t, s)
	dir := filepath.Join(s.Paths.StateDir, "storage")
	write(t, filepath.Join(dir, "storage.json"), recorded)
	pins := `"system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": {"var": "` + varUUID + `"}}`
	if extraPinned {
		pins += `, "extra": {"ref": {"serial": "chalk-extra"}, "identity": {"serial": "chalk-extra", "path": "pci-0000:00:05.0", "size": 17179869184, "type": "hdd"}, "partitions": {"extra": "` + extraUUID + `"}}`
	}
	write(t, filepath.Join(dir, "disks.json"), `{"disks": {`+pins+`}}`)
	if err := store.Write(slotOf(t, s), store.Stored{Documents: documentsOf(identityWith(recorded)), Identity: []byte(identityWith(recorded))}); err != nil {
		t.Fatal(err)
	}
	// As at boot: the identity is in place.
	if err := s.Loader.Apply([]byte(identityWith(recorded))); err != nil {
		t.Fatal(err)
	}
	r.Rules = []tooltest.Rule{
		{Prefix: "blkid -p -o export /dev/vdb", Err: &tool.Error{Command: "blkid", Code: 2}},
		{Prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "extra"), Out: `[{"file":"` + filepath.Join(dir, "disks", "extra", "10-extra.conf") + `","uuid":"` + newUUID + `"}]`},
		{Prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "system"), Out: `[{"file":"` + filepath.Join(dir, "disks", "system", "50-var.conf") + `","uuid":"` + varUUID + `"}]`},
		{Prefix: "blkid -p -o value -s TYPE", Out: "crypto_LUKS\n"},
		{Prefix: "cryptsetup luksDump --dump-json-metadata", Out: luksDump},
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

func apply(s *Server, identity, fallback string) (*nodev1.ApplyConfigResponse, error) {
	resp, err := deliver(context.Background(), s, identity, &nodev1.ApplyConfigRequest{FallbackSecret: fallback})
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// deliver runs what ApplyConfig runs once it has translated its config into identity, with the
// rest of req; an empty identity keeps the node's. The storage and Kubernetes tests state the
// identity exactly this way, with the documents of its storage section, which the storage rules
// read.
func deliver(ctx context.Context, s *Server, identity string, req *nodev1.ApplyConfigRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	r := applyRequest{identity: identity, msg: req}
	if identity != "" {
		schemas, err := schema.LoadSchemas(s.Loader.Schemas)
		if err != nil {
			return nil, err
		}
		r.next, r.schemas = store.Stored{Version: "empty", Documents: documentsOf(identity)}, schemas
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apply(ctx, r)
}

func TestApplyConfigAddsVolume(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	next := identityWith(section("", "ext4"))

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
	}
	if !reflect.DeepEqual(r.Calls, want) {
		t.Errorf("commands:\n got: %s\nwant: %s", strings.Join(r.Calls, "\n      "), strings.Join(want, "\n      "))
	}
	if env := r.Envs[enroll]; !reflect.DeepEqual(env, []string{"NEWPASSWORD=" + secret}) {
		t.Errorf("enrollment environment = %v", env)
	}
	if r.Inputs[testState] != secret {
		t.Errorf("the secret was not checked against STATE's password keyslot on standard input: %v", r.Inputs)
	}
	if len(resp.Changes) != 1 || resp.Changes[0].Volume != "extra" ||
		len(resp.RestartedUnits) != 0 {
		t.Errorf("response = %+v", resp)
	}
	recorded, err := storage.ReadSection(filepath.Join(dir, "storage.json"))
	if err != nil || recorded.Volumes["extra"].MountPoint != "/srv/extra" {
		t.Errorf("recorded section = %+v, %v", recorded, err)
	}
	if got, _ := readIdentity(s); string(got) != next {
		t.Errorf("identity on STATE = %s", got)
	}
}

func TestApplyConfigWithoutStorageChanges(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	if _, err := apply(s, identityWith(section("", "")), ""); err != nil {
		t.Fatal(err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("commands = %v", r.Calls)
	}
}

func TestApplyConfigRefusesDestructiveChange(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	withNodeCertificate(t, s)
	before, _ := readIdentity(s)
	_, err := apply(s, identityWith(section("1G", "")), secret)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "only a reinstall of n1 carries it out") {
		t.Fatalf("err = %v, want a refusal naming a reinstall, since a reset cannot change VAR", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("ran %v for a refused identity", r.Calls)
	}
	if after, _ := readIdentity(s); string(after) != string(before) {
		t.Error("recorded a refused identity")
	}
}

// vdc is a disk besides the extra volume's, which a changed disk reference can find.
var vdc = testDisk{"vdc", "253:32", map[string]string{"ID_PATH": "pci-0000:00:06.0", "ID_SERIAL": "chalk-other"}}

// withExtraRef returns a rendered section whose extra disk is referenced by the serial given.
func withExtraRef(t *testing.T, rendered, serial string) string {
	return withExtraSelector(t, rendered, storage.Selector{Serial: serial, Type: "hdd"})
}

// withExtraSelector returns a rendered section whose extra disk is referenced by the selector given.
func withExtraSelector(t *testing.T, rendered string, sel storage.Selector) string {
	return editSection(t, rendered, func(s *storage.Section) {
		d := s.Disks["extra"]
		d.Ref = storage.Ref{Selector: sel}
		s.Disks["extra"] = d
	})
}

// asSSDs makes the named test disks report themselves as SSDs, so that one selector matches them
// all.
func asSSDs(t *testing.T, s *Server, names ...string) {
	t.Helper()
	for _, n := range names {
		write(t, filepath.Join(s.Host.SysRoot, "block", n, "queue", "rotational"), "0\n")
	}
}

// twin gives vdc the serial of vdb, so that two disks have the extra disk's pinned identity.
func twin(t *testing.T, s *Server) {
	t.Helper()
	write(t, filepath.Join(s.Host.UdevRoot, "b"+vdc.devnum), "E:ID_PATH="+vdc.props["ID_PATH"]+"\nE:ID_SERIAL="+vdb.props["ID_SERIAL"]+"\n")
}

// unlistable breaks a test disk's sysfs entry, so that the node cannot list its disks.
func unlistable(t *testing.T, s *Server) {
	t.Helper()
	if err := os.Remove(filepath.Join(s.Host.SysRoot, "block", "vdc", "size")); err != nil {
		t.Fatal(err)
	}
}

// unplug removes a test disk, as if it was not plugged in.
func unplug(t *testing.T, s *Server, name string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(s.Host.SysRoot, "block", name)); err != nil {
		t.Fatal(err)
	}
}

// A destructive change names what carries it out: a reset for a volume it recreates in place, a
// reinstall for a volume the config removes or moves to another disk, a plugged-in disk for a
// volume whose disk is missing, and a fixed reference for a disk reference that cannot be
// resolved. The image's storage rules name them.
func TestApplyConfigNamesTheRemedyOfADestructiveChange(t *testing.T) {
	for _, tc := range []struct {
		name, next string
		// prepare changes the disks before the config is applied.
		prepare func(t *testing.T, s *Server)
		want    []string
		notWant []string
	}{
		{
			name: "reformatted",
			next: section("", "xfs"),
			want: []string{"reset it with chalkctl storage reset n1 extra"},
		},
		{
			name:    "reformatted, its reference changed but finding its disk",
			next:    withExtraRef(t, section("", "xfs"), "chalk-extra"),
			want:    []string{"reset it with chalkctl storage reset n1 extra"},
			notWant: []string{"cannot be resolved", "reinstall"},
		},
		{
			name:    "reformatted on a disk that is not plugged in",
			next:    section("", "xfs"),
			prepare: func(t *testing.T, s *Server) { unplug(t, s, "vdb") },
			want:    []string{"Volume extra: format xfs instead of ext4; reset it with chalkctl storage reset n1 extra", `Volume extra: its disk extra (model "", size 16G, serial "chalk-extra") is not plugged in; plug it in`},
		},
		{
			name:    "reformatted on a disk whose identity two disks have",
			next:    section("", "xfs"),
			prepare: twin,
			want:    []string{"Volume extra: the node cannot find its disk extra: 2 disks have the pinned identity", "refusing to choose", "reset it with chalkctl storage reset n1 extra"},
			notWant: []string{"plug in"},
		},
		// A change the node would make on the disk, such as another mount point, waits for it,
		// so the node stops no unit for it.
		{
			name: "mounted elsewhere on a disk that is not plugged in",
			next: editSection(t, section("", "ext4"), func(s *storage.Section) {
				v := s.Volumes["extra"]
				v.MountPoint = "/srv/other"
				s.Volumes["extra"] = v
			}),
			prepare: func(t *testing.T, s *Server) { unplug(t, s, "vdb") },
			want:    []string{`Volume extra: its disk extra (model "", size 16G, serial "chalk-extra") is not plugged in; plug it in`},
			notWant: []string{"reset"},
		},
		{
			name: "removed",
			next: section("", ""),
			want: []string{"Volume extra: the config removes it; only a reinstall of n1 removes a volume"},
		},
		{
			name: "moved by its reference",
			next: withExtraRef(t, section("", "ext4"), "chalk-other"),
			want: []string{"but the volume lives on", "only a reinstall of n1 carries it out"},
		},
		{
			name:    "its reference finding no disk",
			next:    withExtraRef(t, section("", "ext4"), "chalk-missing"),
			want:    []string{"cannot be resolved", "no disk matches", "fix the disk reference or plug in the disk it names"},
			notWant: []string{"reinstall"},
		},
		{
			name:    "its reference finding two disks",
			next:    withExtraSelector(t, section("", "ext4"), storage.Selector{Type: "ssd"}),
			prepare: func(t *testing.T, s *Server) { asSSDs(t, s, "vdb", "vdc") },
			want:    []string{"cannot be resolved", "2 disks match", "fix the disk reference"},
			notWant: []string{"plug in", "reinstall"},
		},
		{
			name:    "its reference unresolved as the disks cannot be listed",
			next:    withExtraRef(t, section("", "ext4"), "chalk-other"),
			prepare: unlistable,
			want:    []string{"cannot be resolved: list the disks", "try again once the node can list its disks"},
			notWant: []string{"fix the disk reference", "reinstall"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := installedServer(t, section("", "ext4"), true, vdc)
			withNodeCertificate(t, s)
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			_, err := apply(s, identityWith(tc.next), secret)
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("err = %v, want a refusal", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to contain %q", err, want)
				}
			}
			for _, not := range tc.notWant {
				if strings.Contains(err.Error(), not) {
					t.Errorf("err = %v, want it not to contain %q", err, not)
				}
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v for a refused identity", r.Calls)
			}
		})
	}
}

func TestApplyConfigNeedsFallbackForEncryptedVolume(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	_, err := apply(s, identityWith(section("", "ext4")), "")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "Volume extra is encrypted and the node's fallback is recovery-key, but no fallback secret was sent") {
		t.Fatalf("err = %v", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("ran %v", r.Calls)
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
	return resetWith(s, volume, identity, secret)
}

// resetWith resets the volume to what identity defines, with the documents of its storage section
// checked against the image's schemas, as ResetVolume translates them, and the fallback secret.
func resetWith(s *Server, volume, identity, fallback string) error {
	schemas, err := schema.LoadSchemas(s.Loader.Schemas)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resetVolume(context.Background(), "n1", volume, store.Stored{Documents: documentsOf(identity), Identity: []byte(identity)}, schemas, fallback)
}

// A reset recreates a volume on its disk, also when the config references the disk anew.
func TestResetVolume(t *testing.T) {
	for _, tc := range []struct{ name, next string }{
		{"reformatted", section("", "xfs")},
		{"reformatted, its reference changed but finding its disk", withExtraRef(t, section("", "xfs"), "chalk-extra")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := installedServer(t, section("", "ext4"), true)
			partitionLink(t, s, "vdb", "vdb1", "253:17", "1")

			if err := reset(s, "extra", identityWith(tc.next)); err != nil {
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
				if i >= len(r.Calls) || r.Calls[i] != want {
					t.Fatalf("command %d = %q, want %q (all: %v)", i, r.Calls[min(i, len(r.Calls)-1)], want, r.Calls)
				}
			}
			if !contains(r.Calls, "systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password /dev/disk/by-partuuid/"+newUUID) ||
				!contains(r.Calls, "systemctl start systemd-cryptsetup@extra.service srv-extra.mount") {
				t.Errorf("the volume was not created and opened again: %v", r.Calls)
			}
			pins, err := storage.ReadPins(filepath.Join(dir, "disks.json"))
			if err != nil || pins.Disks["extra"].Partitions["extra"] != newUUID {
				t.Errorf("pins = %+v, %v", pins.Disks["extra"], err)
			}
			recorded, _ := storage.ReadSection(filepath.Join(dir, "storage.json"))
			if recorded.Volumes["extra"].Format != "xfs" {
				t.Errorf("recorded section = %+v", recorded.Volumes["extra"])
			}
		})
	}
}

func TestResetVolumeRefusals(t *testing.T) {
	ssds := func(t *testing.T, s *Server) { asSSDs(t, s, "vdb", "vdc") }
	unplugged := func(t *testing.T, s *Server) { unplug(t, s, "vdb") }
	for _, c := range []struct {
		name, volume, identity string
		// partitionOn is the disk the volume's partition link resolves to.
		partitionOn string
		// prepare changes the disks before the reset.
		prepare func(t *testing.T, s *Server)
		want    []string
		notWant []string
	}{
		{"var", "var", identityWith(section("1G", "ext4")), "vdb", nil, []string{"VAR"}, nil},
		{"unknown volume", "nope", identityWith(section("", "ext4")), "vdb", nil, []string{"no volume nope"}, nil},
		{"other volumes change destructively", "extra", identityWith(section("1G", "xfs")), "vdb", nil, []string{"a reset of volume extra does not carry out the config", "VAR: size 1G instead of filling its disk"}, nil},
		{"partition on another disk", "extra", identityWith(section("", "xfs")), "vda", nil, []string{"refusing to touch it"}, nil},
		{"reference finding another disk", "extra", identityWith(withExtraRef(t, section("", "xfs"), "chalk-other")), "vdb", nil, []string{"but the volume lives on", "only a reinstall of n1 carries it out"}, nil},
		{"reference finding no disk", "extra", identityWith(withExtraRef(t, section("", "xfs"), "chalk-missing")), "vdb", nil, []string{"cannot be resolved: no disk matches", "; fix the disk reference or plug in the disk it names"}, nil},
		{"reference finding two disks", "extra", identityWith(withExtraSelector(t, section("", "xfs"), storage.Selector{Type: "ssd"})), "vdb", ssds, []string{"cannot be resolved: 2 disks match", "; fix the disk reference"}, []string{"plug in"}},
		{"reference unresolved as the disks cannot be listed", "extra", identityWith(withExtraRef(t, section("", "xfs"), "chalk-other")), "vdb", unlistable, []string{"cannot be resolved: list the disks", "; try again once the node can list its disks"}, []string{"fix the disk reference"}},
		{"two disks with the pinned identity", "extra", identityWith(section("", "xfs")), "vdb", twin, []string{"the node cannot find its disk extra: 2 disks have the pinned identity", "refusing to choose"}, []string{"plug in"}},
		{"disk not plugged in", "extra", identityWith(section("", "xfs")), "vdb", unplugged, []string{`its disk extra (model "", size 16G, serial "chalk-extra") is not plugged in; plug it in`}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r := installedServer(t, section("", "ext4"), true, vdc)
			if c.partitionOn == "vdb" {
				partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
			} else {
				partitionLink(t, s, "vda", "vda9", "253:9", "9")
			}
			if c.prepare != nil {
				c.prepare(t, s)
			}
			err := reset(s, c.volume, c.identity)
			if err == nil {
				t.Fatal("reset the volume")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to contain %q", err, want)
				}
			}
			for _, not := range c.notWant {
				if strings.Contains(err.Error(), not) {
					t.Errorf("err = %v, want it not to contain %q", err, not)
				}
			}
			for _, call := range r.Calls {
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

func TestApplyConfigRefusesInvalidSection(t *testing.T) {
	for name, invalid := range invalidSections(t) {
		t.Run(name, func(t *testing.T) {
			s, r := installedServer(t, section("", "ext4"), true)
			_, err := apply(s, identityWith(invalid), secret)
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "invalid storage section") {
				t.Fatalf("err = %v", err)
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v", r.Calls)
			}
		})
	}
}

func TestResetVolumeRefusesInvalidSection(t *testing.T) {
	for name, invalid := range invalidSections(t) {
		t.Run(name, func(t *testing.T) {
			s, r := installedServer(t, section("", "ext4"), true)
			partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
			err := reset(s, "extra", identityWith(invalid))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "invalid storage section") {
				t.Fatalf("err = %v", err)
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v", r.Calls)
			}
		})
	}
}

// systemVolumeServer is an installed server whose system disk carries the volume data at vda9.
func systemVolumeServer(t *testing.T, partUUID string) (*Server, *tooltest.Runner) {
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
	if err := reset(s, "data", identityWith(next)); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{
		dumpState,
		testState,
		"systemctl stop srv-data.mount systemd-cryptsetup@data.service",
		"wipefs --all /dev/vda9",
		"sfdisk --lock --delete /dev/vda 9",
	} {
		if i >= len(r.Calls) || r.Calls[i] != want {
			t.Fatalf("command %d: want %q (all: %v)", i, want, r.Calls)
		}
	}
}

func TestResetVolumeChecksPartUUID(t *testing.T) {
	const other = "0f1e2d3c-4b5a-4968-8776-655443322110"
	next := identityWith(section("", "xfs"))
	for _, c := range []struct {
		name  string
		setUp func(t *testing.T) (*Server, *tooltest.Runner, string)
		want  string
	}{
		{"pinned disk, partition with another PARTUUID", func(t *testing.T) (*Server, *tooltest.Runner, string) {
			s, r := installedServer(t, section("", "ext4"), true)
			addPartition(t, s, "vdb", "vdb1", "253:17", "1", other, "disk/by-partuuid/"+extraUUID)
			return s, r, next
		}, "PARTUUID"},
		{"pinned disk, no recorded PARTUUID", func(t *testing.T) (*Server, *tooltest.Runner, string) {
			s, r := installedServer(t, section("", "ext4"), true)
			partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
			write(t, filepath.Join(s.Paths.StateDir, "storage", "disks.json"), `{"disks": {"system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": {"var": "`+varUUID+`"}}, "extra": {"ref": {"serial": "chalk-extra"}, "identity": {"serial": "chalk-extra", "path": "pci-0000:00:05.0", "size": 17179869184, "type": "hdd"}, "partitions": {}}}}`)
			return s, r, next
		}, "no recorded partition"},
		{"system disk, partition with another PARTUUID", func(t *testing.T) (*Server, *tooltest.Runner, string) {
			s, r := systemVolumeServer(t, other)
			return s, r, identityWith(withSystemVolume(t, section("", ""), storage.EncryptionTPM2))
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
			if changed := withoutChecks(r.Calls); len(changed) != 0 {
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
	err := resetWith(s, "extra", identityWith(next), "")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "Volume data is encrypted") || !strings.Contains(err.Error(), "no fallback secret") {
		t.Fatalf("err = %v", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("ran %v", r.Calls)
	}
}

func TestResetVolumeRefusesRemoval(t *testing.T) {
	s, r := installedServer(t, section("", "ext4"), true)
	partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
	err := reset(s, "extra", identityWith(section("", "")))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "no longer has volume extra") {
		t.Fatalf("err = %v", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("ran %v", r.Calls)
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
func mismatch(r *tooltest.Runner) {
	r.Rules = append([]tooltest.Rule{{Prefix: "cryptsetup open --test-passphrase", Err: &tool.Error{Command: "cryptsetup open", Code: 2, Stderr: "No key available with this passphrase."}}}, r.Rules...)
}

func TestApplyConfigRefusesMismatchingFallbackSecret(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	before, _ := readIdentity(s)
	mismatch(r)
	_, err := apply(s, identityWith(section("", "ext4")), "mistyped")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the fallback secret does not match the node's existing fallback keyslot") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(r.Calls, []string{dumpState, testState}) {
		t.Errorf("commands = %v, want only the check", r.Calls)
	}
	if r.Inputs[testState] != "mistyped" {
		t.Errorf("inputs = %v", r.Inputs)
	}
	if after, _ := readIdentity(s); string(after) != string(before) {
		t.Error("recorded a refused identity")
	}
}

func TestApplyConfigRefusesWhenTheCheckFails(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	r.Rules = append([]tooltest.Rule{{Prefix: "cryptsetup open", Err: &tool.Error{Command: "cryptsetup open", Code: 4, Stderr: "Device does not exist."}}}, r.Rules...)
	_, err := apply(s, identityWith(section("", "ext4")), secret)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(r.Calls, []string{dumpState, testState}) {
		t.Errorf("commands = %v, want only the check", r.Calls)
	}
}

func TestResetVolumeRefusesMismatchingFallbackSecret(t *testing.T) {
	s, r := installedServer(t, section("", "ext4"), true)
	partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
	mismatch(r)
	err := reset(s, "extra", identityWith(section("", "xfs")))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(r.Calls, []string{dumpState, testState}) {
		t.Errorf("commands = %v, want only the check", r.Calls)
	}
}

// TestFallbackCheckDevice checks the secret against STATE, else VAR, else another encrypted
// volume the node records, and skips the check when the node has none.
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
		// The reset volume still has its keyslot when the secret is checked, before it is wiped.
		{"only the reset volume encrypted", editSection(t, section("", "ext4"), plainSystem), editSection(t, section("", "xfs"), plainSystem), true, true, "/dev/disk/by-partuuid/" + extraUUID},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r := installedServer(t, c.recorded, c.extraPinned)
			var err error
			if c.reset {
				partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
				err = reset(s, "extra", identityWith(c.next))
			} else {
				_, err = apply(s, identityWith(c.next), secret)
			}
			if err != nil {
				t.Fatal(err)
			}
			var checks []string
			for _, call := range r.Calls {
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
			if !contains(r.Calls, "systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password /dev/disk/by-partuuid/"+newUUID) &&
				!contains(r.Calls, "systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password /dev/disk/chalk-boot/data") {
				t.Errorf("no fallback keyslot was enrolled: %v", r.Calls)
			}
		})
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

func TestApplyConfigDeliversNodeCertificateAlone(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	nodeCA := withNodeCertificate(t, s)
	recorded, _ := readIdentity(s)
	before := s.Certificate.Fingerprint()
	renewed, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, _ := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n2"}, time.Now())
	for name, tc := range map[string]struct {
		identity string
		req      *nodev1.ApplyConfigRequest
	}{
		"nothing":                    {"", &nodev1.ApplyConfigRequest{}},
		"another node's certificate": {"", &nodev1.ApplyConfigRequest{NodeCertificate: []byte(other.Certificate), NodeKey: []byte(other.Key)}},
		"another key":                {"", &nodev1.ApplyConfigRequest{NodeCertificate: []byte(renewed.Certificate), NodeKey: []byte(other.Key)}},
		// Refused before the config is applied.
		"another node's with config": {identityWith(section("", "")), &nodev1.ApplyConfigRequest{NodeCertificate: []byte(other.Certificate), NodeKey: []byte(other.Key)}},
	} {
		_, err := deliver(context.Background(), s, tc.identity, tc.req)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want invalid_argument", name, err)
		} else if strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("%s: the error holds a key", name)
		}
	}
	if s.Certificate.Fingerprint() != before || len(r.Calls) != 0 {
		t.Fatalf("a refused request changed the certificate or ran %v", r.Calls)
	}

	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{
		NodeCertificate: []byte(renewed.Certificate), NodeKey: []byte(renewed.Key),
	})); err != nil {
		t.Fatal(err)
	}
	if s.Certificate.Fingerprint() == before {
		t.Error("the delivered certificate is not served")
	}
	if got, _ := readIdentity(s); string(got) != string(recorded) || len(r.Calls) != 0 {
		t.Errorf("delivering a certificate alone changed the identity or ran %v", r.Calls)
	}
}
