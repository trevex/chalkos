package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/uki"
)

// fakeRunner records commands and answers them from the first matching rule. A rule with
// times > 0 answers that many times.
//
// Commands that succeed also change the host as the real tools would: mount and umount edit
// the mount table, systemd-cryptsetup creates and removes /dev/mapper entries, sfdisk
// --part-uuid changes what sfdisk --json reports later, and efibootmgr edits efi when set.
type fakeRunner struct {
	rules []*rule
	calls []string
	envs  map[string][]string

	mountInfo, devRoot string
	// partUUIDs holds the partition UUIDs sfdisk --part-uuid set, by "<device> <number>".
	partUUIDs map[string]string
	efi       *fakeEFI
}

type rule struct {
	prefix string
	out    string
	err    error
	times  int
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunWithEnv(ctx, nil, name, args...)
}

func (f *fakeRunner) RunWithEnv(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if env != nil {
		if f.envs == nil {
			f.envs = map[string][]string{}
		}
		f.envs[line] = env
	}
	out, err := f.answer(line)
	if err != nil {
		return out, err
	}
	return f.apply(name, args, out)
}

func (f *fakeRunner) RunWithInput(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	return f.RunWithEnv(ctx, nil, name, args...)
}

func (f *fakeRunner) answer(line string) ([]byte, error) {
	for _, r := range f.rules {
		if !strings.HasPrefix(line, r.prefix) {
			continue
		}
		if r.times < 0 {
			continue
		}
		if r.times > 0 {
			r.times--
			if r.times == 0 {
				r.times = -1
			}
		}
		return []byte(r.out), r.err
	}
	return nil, nil
}

// apply makes the host reflect a command that succeeded.
func (f *fakeRunner) apply(name string, args []string, out []byte) ([]byte, error) {
	switch {
	case f.mountInfo != "" && name == "mount" && len(args) == 4:
		data, err := os.ReadFile(f.mountInfo)
		if err != nil {
			return nil, err
		}
		data = append(data, fmt.Sprintf("50 30 253:9 / %s rw,relatime - ext4 %s rw\n", args[3], args[2])...)
		return out, os.WriteFile(f.mountInfo, data, 0o644)
	case f.mountInfo != "" && name == "umount" && len(args) == 1:
		data, err := os.ReadFile(f.mountInfo)
		if err != nil {
			return nil, err
		}
		var kept []string
		for _, l := range strings.SplitAfter(string(data), "\n") {
			if fields := strings.Fields(l); len(fields) > 4 && fields[4] == args[0] {
				continue
			}
			kept = append(kept, l)
		}
		if len(kept) == len(strings.SplitAfter(string(data), "\n")) {
			return nil, &node.ToolError{Command: "umount", Code: 32, Stderr: "umount: " + args[0] + ": not mounted."}
		}
		return out, os.WriteFile(f.mountInfo, []byte(strings.Join(kept, "")), 0o644)
	case f.devRoot != "" && name == "systemd-cryptsetup" && len(args) >= 3 && args[0] == "attach":
		return out, os.WriteFile(filepath.Join(f.devRoot, "mapper", args[1]), []byte(args[2]), 0o644)
	case f.devRoot != "" && name == "systemd-cryptsetup" && len(args) == 2 && args[0] == "detach":
		// systemd-cryptsetup reports an inactive volume and succeeds.
		if err := os.Remove(filepath.Join(f.devRoot, "mapper", args[1])); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return out, nil
	case name == "sfdisk" && len(args) == 4 && args[0] == "--part-uuid":
		if f.partUUIDs == nil {
			f.partUUIDs = map[string]string{}
		}
		f.partUUIDs[args[1]+" "+args[2]] = args[3]
		return out, nil
	case name == "sfdisk" && len(args) == 2 && args[0] == "--json" && len(f.partUUIDs) > 0 && len(out) > 0:
		return f.withPartUUIDs(args[1], out)
	case f.efi != nil && name == "efibootmgr":
		return f.efi.run(f, args)
	}
	return out, nil
}

// withPartUUIDs changes the partition UUIDs in sfdisk --json output for dev to those set.
func (f *fakeRunner) withPartUUIDs(dev string, out []byte) ([]byte, error) {
	var dump map[string]map[string]any
	if err := json.Unmarshal(out, &dump); err != nil {
		return nil, err
	}
	parts, _ := dump["partitiontable"]["partitions"].([]any)
	for _, p := range parts {
		p := p.(map[string]any)
		n := strings.TrimPrefix(strings.TrimPrefix(p["node"].(string), dev), "p")
		if uuid, ok := f.partUUIDs[dev+" "+n]; ok {
			p["uuid"] = strings.ToUpper(uuid)
		}
	}
	return json.Marshal(dump)
}

// holds reports whether a partition of dev is mounted or opened by the device mapper.
func (f *fakeRunner) holds(dev string) bool {
	data, _ := os.ReadFile(f.mountInfo)
	for _, l := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(l); len(fields) > 8 && strings.HasPrefix(fields[8], dev) {
			return true
		}
	}
	mappers, _ := os.ReadDir(filepath.Join(f.devRoot, "mapper"))
	for _, m := range mappers {
		if backing, _ := os.ReadFile(filepath.Join(f.devRoot, "mapper", m.Name())); strings.HasPrefix(string(backing), dev) {
			return true
		}
	}
	return false
}

// fakeEFI holds UEFI boot entries the way efibootmgr shows and changes them.
type fakeEFI struct {
	entries  []efiEntry
	next     int
	bootNext string
	deleted  []string
}

type efiEntry struct{ num, label, partUUID string }

func (e *fakeEFI) run(f *fakeRunner, args []string) ([]byte, error) {
	flag := func(name string) string {
		for n, a := range args {
			if a == name && n+1 < len(args) {
				return args[n+1]
			}
		}
		return ""
	}
	switch {
	case len(args) > 0 && args[0] == "--create":
		num := fmt.Sprintf("%04X", e.next)
		e.next++
		e.entries = append(e.entries, efiEntry{num, flag("--label"), strings.ToLower(f.partUUIDs[flag("--disk")+" "+flag("--part")])})
	case len(args) == 1 && args[0] == "--verbose":
		var out strings.Builder
		out.WriteString("BootCurrent: 0001\n")
		if e.bootNext != "" {
			fmt.Fprintf(&out, "BootNext: %s\n", e.bootNext)
		}
		for _, en := range e.entries {
			path := "PciRoot(0x0)/Pci(0x4,0x0){auto_created_boot_option}"
			if en.partUUID != "" {
				path = "HD(1,GPT," + en.partUUID + ",0x800,0x80000)/\\EFI\\BOOT\\BOOTX64.EFI"
			}
			fmt.Fprintf(&out, "Boot%s* %s\t%s\n", en.num, en.label, path)
		}
		return []byte(out.String()), nil
	case len(args) > 0 && args[0] == "--bootnext":
		e.bootNext = args[1]
	case len(args) == 1 && args[0] == "--delete-bootnext":
		if e.bootNext == "" {
			return nil, &node.ToolError{Command: "efibootmgr", Code: 2, Stderr: "Could not delete BootNext: No such file or directory"}
		}
		e.bootNext = ""
	case len(args) > 0 && args[0] == "--delete-bootnum":
		num := flag("--bootnum")
		e.deleted = append(e.deleted, num)
		for n, en := range e.entries {
			if en.num == num {
				e.entries = append(e.entries[:n], e.entries[n+1:]...)
				return nil, nil
			}
		}
		return nil, &node.ToolError{Command: "efibootmgr", Code: 2, Stderr: "Could not delete Boot" + num}
	}
	return nil, nil
}

func (f *fakeRunner) add(rules ...*rule) { f.rules = append(f.rules, rules...) }

// installed reports whether STATE holds the installed marker, failing the test on errors.
func installed(t *testing.T, stateDir string) bool {
	t.Helper()
	ok, err := Installed(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// blkidNothing is how blkid reports a device without any signature.
var blkidNothing = &node.ToolError{Command: "blkid", Code: 2}

const (
	systemSeed = "2869f04c-5655-50f4-28b9-6b2eb9700a02"
	varUUID    = "7ad19bdf-77ff-4273-8a5c-d403d2a5f95b"
	secret     = "tlvujiln-vffneuhk-idrhucle-evlgjnvd-ljvggbvr-djbthrbc-cgfrvhcb-vucrunlc"
)

var (
	stateType = storage.PartitionType("state")
	varType   = storage.PartitionType("var")
)

type testDisk struct {
	name, devnum string
	size         uint64
	props        map[string]string
}

var (
	vda = testDisk{"vda", "253:0", 16 << 30, map[string]string{"ID_PATH": "pci-0000:00:04.0"}}
	vdb = testDisk{"vdb", "253:16", 16 << 30, map[string]string{"ID_PATH": "pci-0000:00:05.0", "ID_SERIAL": "chalk-target"}}
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTestInstaller builds sysfs, udev and /dev trees with the disks, of which vda is the boot
// disk, plus the image's system-region definitions and a mount table with STATE mounted.
func newTestInstaller(t *testing.T, r *fakeRunner, disks ...testDisk) *Installer {
	t.Helper()
	root := t.TempDir()
	h := storage.Host{
		SysRoot:  filepath.Join(root, "sys"),
		UdevRoot: filepath.Join(root, "udev"),
		DevRoot:  filepath.Join(root, "dev"),
	}
	for _, d := range disks {
		dir := filepath.Join(h.SysRoot, "block", d.name)
		write(t, filepath.Join(dir, "dev"), d.devnum+"\n")
		write(t, filepath.Join(dir, "size"), strconv.FormatUint(d.size/512, 10)+"\n")
		write(t, filepath.Join(dir, "queue", "rotational"), "1\n")
		var props string
		for k, v := range d.props {
			props += "E:" + k + "=" + v + "\n"
		}
		write(t, filepath.Join(h.UdevRoot, "b"+d.devnum), props)
		write(t, filepath.Join(h.DevRoot, d.name), "")
	}
	if err := os.MkdirAll(filepath.Join(h.DevRoot, "disk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../vda", filepath.Join(h.DevRoot, "disk", "chalk-boot-disk")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(h.DevRoot, "mapper", "state"), "")
	state := filepath.Join(root, "state")
	write(t, filepath.Join(root, "mountinfo"), "30 1 0:27 / / rw - tmpfs tmpfs rw\n41 30 253:1 / "+state+" rw,relatime - ext4 /dev/mapper/state rw\n")
	for name, text := range systemDefinitions {
		write(t, filepath.Join(root, "repart.d", name), text)
	}
	r.mountInfo = filepath.Join(root, "mountinfo")
	r.devRoot = h.DevRoot
	return &Installer{
		Run:          r,
		Host:         h,
		StateDir:     state,
		BootDisk:     "/dev/disk/chalk-boot-disk",
		Definitions:  filepath.Join(root, "repart.d"),
		WorkDir:      filepath.Join(root, "work"),
		MountInfo:    filepath.Join(root, "mountinfo"),
		OpenDisk:     func(context.Context, string) (Disk, error) { return nil, fmt.Errorf("unexpected open") },
		Architecture: "x86-64",
		Now:          func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) },
	}
}

var systemDefinitions = map[string]string{
	"00-esp.conf":   "[Partition]\nType=esp\nFormat=vfat\nSizeMinBytes=256M\nSizeMaxBytes=256M\n",
	"20-store.conf": "[Partition]\nType=usr-x86-64\nSizeMinBytes=2G\nSizeMaxBytes=2G\n",
	"50-state.conf": "[Partition]\nEncrypt=tpm2\nFormat=ext4\nLabel=state\nSizeMaxBytes=64M\nSizeMinBytes=64M\nType=" + stateType + "\n",
}

type part struct {
	n          int
	typ, label string
}

var (
	espPart   = part{1, strings.ToUpper(typeESP), "esp"}
	usrPart   = part{3, "8484680C-9521-48C6-9C11-B0720656F69E", "usr-x86-64"}
	statePart = part{6, strings.ToUpper(stateType), "state"}
	varPart   = part{7, strings.ToUpper(varType), "var"}
)

// sfdisk is `sfdisk --json` output for a GPT on dev with the partitions.
func sfdisk(dev string, parts ...part) string {
	entries := make([]string, len(parts))
	for i, p := range parts {
		entries[i] = fmt.Sprintf(`{"node": "%s%d", "start": 2048, "size": 2048, "type": "%s", "uuid": "%08X-0000-4000-8000-000000000000", "name": "%s"}`, dev, p.n, p.typ, p.n, p.label)
	}
	return `{"partitiontable": {"label": "gpt", "id": "C5A8B6E2-4E09-4D47-9A4C-1E1C2C6F1B2A", "device": "` + dev + `", "unit": "sectors", "sectorsize": 512, "partitions": [` + strings.Join(entries, ", ") + `]}}`
}

func repartRow(file, uuid string) string {
	return `[{"file":"` + file + `","uuid":"` + uuid + `","activity":"create"}]`
}

func testSection(policy, fallback, systemRef string) storage.Section {
	enc := policy
	def := "[Partition]\nType=" + varType + "\nLabel=var\nFormat=ext4\n"
	if enc == storage.EncryptionTPM2 {
		def += "Encrypt=tpm2\n"
	}
	return storage.Section{
		Disks: map[string]storage.Disk{
			storage.SystemDisk: {Ref: storage.Ref{Path: systemRef}, Seed: systemSeed, Repart: map[string]string{"50-var.conf": def}},
		},
		Volumes: map[string]storage.Volume{
			"var": {Disk: storage.SystemDisk, Label: "var", Format: "ext4", MountPoint: "/var", Encryption: enc},
		},
		Fallback:   fallback,
		Encryption: policy,
	}
}

func testRequest(t *testing.T, section storage.Section) Request {
	t.Helper()
	now := time.Now()
	ca, err := pki.NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	nodeCA, err := pki.NewNodeCA(ca, now)
	if err != nil {
		t.Fatal(err)
	}
	n, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}, IPs: []net.IP{net.ParseIP("10.0.0.21")}}, now)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := json.Marshal(map[string]any{"hostname": "n1", "storage": section, "kubernetes": map[string]any{"nodeName": "n1"}})
	if err != nil {
		t.Fatal(err)
	}
	fallback := secret
	if section.Fallback == storage.FallbackNone {
		fallback = ""
	}
	return Request{
		Identity:        identity,
		Section:         section,
		Kubernetes:      &manifest.KubernetesIdentity{NodeName: "n1"},
		NodeCertificate: []byte(n.Certificate),
		NodeKey:         []byte(n.Key),
		CA:              []byte(ca.Certificate),
		FallbackSecret:  fallback,
	}
}

// inPlaceRules answer like a node booted from vda whose STATE is encrypted and whose TPM
// unseals: repart creates VAR as vda7.
func inPlaceRules(i *Installer) []*rule {
	return []*rule{
		{prefix: "sfdisk --json /dev/vda", out: sfdisk("/dev/vda", espPart, usrPart, statePart, varPart)},
		{prefix: "blkid -p -o value -s TYPE", out: "crypto_LUKS\n"},
		{prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(i.StateDir, "storage", "disks", "system"), out: repartRow(filepath.Join(i.StateDir, "storage", "disks", "system", "50-var.conf"), varUUID)},
	}
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got: %s\nwant: %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
}

func hasPrefix(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestInPlaceInstallsOnBootDisk(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, vdb)
	r.add(inPlaceRules(i)...)
	req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))

	if err := i.InPlace(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	defs := filepath.Join(i.StateDir, "storage", "disks", "system")
	enroll := "systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password "
	calls := r.calls
	espUUID := calls[len(calls)-1][len(calls[len(calls)-1])-36:]
	assertCalls(t, calls, []string{
		"umount " + i.StateDir,
		"systemd-cryptsetup detach state",
		"sfdisk --json /dev/vda",
		"udevadm settle --timeout=30",
		"blkid -p -o value -s TYPE /dev/vda6",
		"sfdisk --json /dev/vda",
		"udevadm wait --timeout=60 /dev/vda6",
		"blkid -p -o value -s TYPE /dev/vda6",
		"systemd-cryptsetup attach state /dev/vda6 - tpm2-device=auto,headless=true",
		"e2fsck -p /dev/mapper/state",
		"mount -t ext4 /dev/mapper/state " + i.StateDir,
		"systemd-repart --dry-run=no --json=short --definitions=" + defs + " --seed=" + systemSeed + " /dev/vda",
		"sfdisk --json /dev/vda",
		"udevadm settle --timeout=30",
		"blkid -p -o value -s TYPE /dev/vda6",
		enroll + "/dev/vda6",
		"udevadm settle --timeout=30",
		"blkid -p -o value -s TYPE /dev/vda7",
		enroll + "/dev/vda7",
		"sfdisk --json /dev/vda",
		"sfdisk --part-uuid /dev/vda 1 " + espUUID,
	})
	if espUUID == "00000001-0000-4000-8000-000000000000" {
		t.Error("the ESP kept the image's partition UUID")
	}
	for line, env := range r.envs {
		if !reflect.DeepEqual(env, []string{"NEWPASSWORD=" + secret}) {
			t.Errorf("%s ran with environment %v", line, env)
		}
	}
	if len(r.envs) != 2 {
		t.Errorf("the fallback secret was passed %d times, want twice", len(r.envs))
	}
	for _, c := range r.calls {
		if strings.Contains(c, secret) {
			t.Errorf("the fallback secret is on a command line: %s", c)
		}
	}

	for name, want := range map[string]string{
		"identity.json":   string(req.Identity),
		"chalkd/node.pem": string(req.NodeCertificate) + string(req.NodeKey),
		"chalkd/ca.crt":   string(req.CA),
		"installed":       "2026-10-06T12:00:00Z\n",
	} {
		got, err := os.ReadFile(filepath.Join(i.StateDir, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if info, err := os.Stat(filepath.Join(i.StateDir, "chalkd/node.pem")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("node.pem mode = %v, %v; want 0600", info.Mode(), err)
	}
	if info, err := os.Stat(filepath.Join(i.StateDir, "chalkd")); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("chalkd/ mode = %v, %v; want 0700", info.Mode(), err)
	}
	section, err := storage.ReadSection(filepath.Join(i.StateDir, "storage", "storage.json"))
	if err != nil || !reflect.DeepEqual(section, req.Section) {
		t.Errorf("recorded section = %+v, %v", section, err)
	}
	pins, err := storage.ReadPins(filepath.Join(i.StateDir, "storage", "disks.json"))
	if err != nil || pins.Disks["system"].Partitions["var"] != varUUID {
		t.Errorf("pins = %+v, %v", pins, err)
	}
	if !installed(t, i.StateDir) {
		t.Error("the installed marker is missing")
	}
}

func TestInPlaceRecreatesStateForPolicy(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda)
	// The image created STATE encrypted; once deleted and created again it holds ext4.
	r.add(&rule{prefix: "blkid -p -o value -s TYPE /dev/vda6", out: "crypto_LUKS\n", times: 1},
		&rule{prefix: "blkid -p -o value -s TYPE", out: "ext4\n"})
	r.add(inPlaceRules(i)...)
	req := testRequest(t, testSection(storage.EncryptionNone, "recovery-key", "/dev/vda"))

	if err := i.InPlace(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	system := filepath.Join(i.WorkDir, "system")
	for _, want := range []string{
		"sfdisk --delete /dev/vda 6",
		"systemd-repart --dry-run=no --seed=random --definitions=" + system + " --tpm2-pcrs=7 /dev/vda",
		"mount -t ext4 /dev/vda6 " + i.StateDir,
	} {
		if !hasPrefix(r.calls, want) {
			t.Errorf("missing %q in %v", want, r.calls)
		}
	}
	if hasPrefix(r.calls, "systemd-cryptenroll") || hasPrefix(r.calls, "systemd-cryptsetup attach") {
		t.Errorf("treated an unencrypted node as encrypted: %v", r.calls)
	}
	def, err := os.ReadFile(filepath.Join(system, "50-state.conf"))
	if err != nil || strings.Contains(string(def), "Encrypt") || !strings.Contains(string(def), "Label=state") {
		t.Errorf("STATE definition = %q, %v; want it without Encrypt=", def, err)
	}
	if def, _ := os.ReadFile(filepath.Join(system, "00-esp.conf")); string(def) != systemDefinitions["00-esp.conf"] {
		t.Errorf("ESP definition = %q, want it unchanged", def)
	}
}

func TestInPlaceKeepsMatchingState(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda)
	r.add(inPlaceRules(i)...)
	if err := i.InPlace(context.Background(), testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))); err != nil {
		t.Fatal(err)
	}
	if hasPrefix(r.calls, "sfdisk --delete") || hasPrefix(r.calls, "systemd-repart --dry-run=no --seed=random --definitions="+filepath.Join(i.WorkDir, "system")) {
		t.Errorf("recreated a STATE that matches the policy: %v", r.calls)
	}
}

func TestInPlaceRefusals(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, i *Installer, req *Request)
		want   string
	}{
		{"installed", func(t *testing.T, i *Installer, req *Request) {
			write(t, filepath.Join(i.StateDir, "installed"), "")
		}, "installed already"},
		{"installed marker unreadable", func(t *testing.T, i *Installer, req *Request) {
			// A link to itself makes stat fail with ELOOP, not with "does not exist".
			if err := os.MkdirAll(i.StateDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("installed", filepath.Join(i.StateDir, "installed")); err != nil {
				t.Fatal(err)
			}
		}, "too many levels of symbolic links"},
		{"other system disk", func(t *testing.T, i *Installer, req *Request) {
			req.Section = testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vdb")
		}, "install it with the installer"},
		{"no fallback secret", func(t *testing.T, i *Installer, req *Request) {
			req.FallbackSecret = ""
		}, "no fallback secret"},
		{"foreign node key", func(t *testing.T, i *Installer, req *Request) {
			other := testRequest(t, req.Section)
			req.NodeKey = other.NodeKey
		}, "node certificate"},
		{"node certificate of another OS CA", func(t *testing.T, i *Installer, req *Request) {
			other := testRequest(t, req.Section)
			req.CA = other.CA
		}, "does not verify against the OS CA"},
		{"unknown policy", func(t *testing.T, i *Installer, req *Request) {
			req.Section.Encryption = "kms"
		}, "kms"},
		{"disk name leaving the definitions directory", func(t *testing.T, i *Installer, req *Request) {
			req.Section.Disks["../x"] = req.Section.Disks[storage.SystemDisk]
		}, `disk name "../x"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeRunner{}
			i := newTestInstaller(t, r, vda, vdb)
			r.add(inPlaceRules(i)...)
			req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))
			c.change(t, i, &req)
			err := i.InPlace(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if len(r.calls) != 0 {
				t.Errorf("ran %v before refusing", r.calls)
			}
		})
	}
}

func TestInPlaceStopsOnDiskError(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, vdb)
	r.add(&rule{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nTYPE=ext4\n"})
	r.add(inPlaceRules(i)...)
	section := testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda")
	section.Disks["data"] = storage.Disk{Ref: storage.Ref{Selector: storage.Selector{Serial: "chalk-target"}}, Seed: systemSeed, Repart: map[string]string{"10-data.conf": "[Partition]\nLabel=data\n"}}
	section.Volumes["data"] = storage.Volume{Disk: "data", Label: "data", Format: "xfs", MountPoint: "/srv/data", Encryption: "tpm2"}

	err := i.InPlace(context.Background(), testRequest(t, section))
	if err == nil || !strings.Contains(err.Error(), "disk data") || !strings.Contains(err.Error(), "carries data") {
		t.Fatalf("err = %v, want the data disk's problem", err)
	}
	if hasPrefix(r.calls, "systemd-cryptenroll") || installed(t, i.StateDir) {
		t.Error("went on installing after a disk could not be set up")
	}
}

func TestInPlaceCanRunAgain(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda)
	r.add(&rule{prefix: "systemd-cryptenroll", err: &node.ToolError{Command: "systemd-cryptenroll", Code: 1, Stderr: "Failed to unseal secret using TPM2"}, times: 1})
	r.add(inPlaceRules(i)...)
	req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))

	err := i.InPlace(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "enroll the fallback key on state") {
		t.Fatalf("err = %v, want the enrollment failure", err)
	}
	if installed(t, i.StateDir) {
		t.Fatal("an interrupted install wrote the installed marker")
	}
	r.calls = nil
	if err := i.InPlace(context.Background(), req); err != nil {
		t.Fatalf("the install did not run again: %v", err)
	}
	if !installed(t, i.StateDir) || hasPrefix(r.calls, "sfdisk --delete") {
		t.Errorf("second install: installed = %v, commands %v", installed(t, i.StateDir), r.calls)
	}
}

func TestInstallRefusesUnencryptedVolumeMeantToBeEncrypted(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda)
	r.add(&rule{prefix: "blkid -p -o value -s TYPE /dev/vda7", out: "ext4\n"})
	r.add(inPlaceRules(i)...)
	err := i.InPlace(context.Background(), testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda")))
	if err == nil || !strings.Contains(err.Error(), "volume var is meant to be encrypted") {
		t.Fatalf("err = %v", err)
	}
	if installed(t, i.StateDir) {
		t.Error("installed a node whose VAR is not encrypted")
	}
}

func TestInstallWithoutFallbackEnrollsNothing(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda)
	r.add(inPlaceRules(i)...)
	if err := i.InPlace(context.Background(), testRequest(t, testSection(storage.EncryptionTPM2, storage.FallbackNone, "/dev/vda"))); err != nil {
		t.Fatal(err)
	}
	if hasPrefix(r.calls, "systemd-cryptenroll") {
		t.Errorf("enrolled a fallback for a node without one: %v", r.calls)
	}
}

func TestBootLoader(t *testing.T) {
	for arch, want := range map[string]string{
		"x86-64": `\EFI\BOOT\BOOTX64.EFI`,
		"arm64":  `\EFI\BOOT\BOOTAA64.EFI`,
		"":       "",
	} {
		if got := (&Installer{Architecture: arch}).loader(); got != want {
			t.Errorf("the boot loader of %q = %q, want %q", arch, got, want)
		}
	}
	if arch := Default(false).Architecture; arch != uki.GoArchitecture(runtime.GOARCH) {
		t.Errorf("Default installs %q on %s", arch, runtime.GOARCH)
	}
}

func TestInPlaceStoresKubernetesShare(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, vdb)
	r.add(inPlaceRules(i)...)
	req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))
	k, err := pki.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	share, err := kpki.WorkerShare(k, "n1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if req.KubernetesShare, err = share.Encode(); err != nil {
		t.Fatal(err)
	}
	if err := i.InPlace(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(i.StateDir, "kubernetes", "share.json")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(req.KubernetesShare) {
		t.Errorf("share.json = %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("share.json mode = %v, %v; want 0600", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("kubernetes/ mode = %v, %v; want 0700", info, err)
	}
}

func TestInstallRefusesInvalidShare(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, vdb)
	req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))
	req.KubernetesShare = []byte(`{"kind": "worker", "ca": {"certificate": "not a certificate"}}`)
	if err := i.InPlace(context.Background(), req); err == nil || !strings.Contains(err.Error(), "Kubernetes share") {
		t.Fatalf("err = %v, want the share refused", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v before refusing the share", r.calls)
	}
}

// TestInstallRefusesShareForAnotherNode guards against a node running another node's kubelet
// certificate: ParseShare alone cannot catch this, since the share is internally consistent and
// only wrong for this node.
func TestInstallRefusesShareForAnotherNode(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, vdb)
	req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))
	k, err := pki.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	share, err := kpki.WorkerShare(k, "other", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if req.KubernetesShare, err = share.Encode(); err != nil {
		t.Fatal(err)
	}
	if err := i.InPlace(context.Background(), req); err == nil || !strings.Contains(err.Error(), "Kubernetes share") {
		t.Fatalf("err = %v, want the share refused", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v before refusing the share", r.calls)
	}
}

// TestInstallRefusesShareWithExtraFields checks that Install relies on ParseShare's strict
// decoding, which rejects a share carrying a field Share does not declare.
func TestInstallRefusesShareWithExtraFields(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, vdb)
	req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))
	k, err := pki.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	share, err := kpki.WorkerShare(k, "n1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := share.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["extra"] = json.RawMessage(`"unexpected"`)
	if req.KubernetesShare, err = json.Marshal(fields); err != nil {
		t.Fatal(err)
	}
	if err := i.InPlace(context.Background(), req); err == nil || !strings.Contains(err.Error(), "Kubernetes share") {
		t.Fatalf("err = %v, want the share refused", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v before refusing the share", r.calls)
	}
}

// TestInPlaceStoresCanonicalShare checks that STATE holds the share Encode produces, not
// whatever bytes the request carried, even when both describe the same share.
func TestInPlaceStoresCanonicalShare(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, vdb)
	r.add(inPlaceRules(i)...)
	req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))
	k, err := pki.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	share, err := kpki.WorkerShare(k, "n1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := share.Encode()
	if err != nil {
		t.Fatal(err)
	}
	indented, err := json.MarshalIndent(share, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(indented, canonical) {
		t.Fatal("the indented and canonical encodings are identical; this test proves nothing")
	}
	req.KubernetesShare = indented

	if err := i.InPlace(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(i.StateDir, "kubernetes", "share.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, canonical) {
		t.Errorf("share.json = %s, want the canonical encoding %s", got, canonical)
	}
}

// TestInstallRefusesForeignNodeCA guards against a control plane renewing node certificates no
// node trusts.
func TestInstallRefusesForeignNodeCA(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, vdb)
	req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))
	k, err := pki.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nodeCA, err := pki.NewNodeCA(other, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if req.KubernetesShare, err = kpki.ControlPlaneShare(k, nodeCA).Encode(); err != nil {
		t.Fatal(err)
	}
	if err := i.InPlace(context.Background(), req); err == nil || !strings.Contains(err.Error(), "node CA") {
		t.Fatalf("err = %v, want the node CA refused", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v before refusing the share", r.calls)
	}
}

// A node certificate arrives as two PEM strings that are joined into one file; whatever their
// bytes, the file written must be one chalkd loads, or the install must be refused.
func TestInPlaceWritesALoadableNodeCertificate(t *testing.T) {
	other, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(chain string) string{
		"without a final newline":       func(chain string) string { return strings.TrimRight(chain, "\n") },
		"with another key in the chain": func(chain string) string { return chain + other.Key },
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{}
			i := newTestInstaller(t, r, vda)
			r.add(inPlaceRules(i)...)
			req := testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vda"))
			req.NodeCertificate = []byte(edit(string(req.NodeCertificate)))
			if err := i.InPlace(context.Background(), req); err != nil {
				if installed(t, i.StateDir) {
					t.Error("a refused install wrote the installed marker")
				}
				return
			}
			data, err := os.ReadFile(filepath.Join(i.StateDir, "chalkd/node.pem"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pki.VerifyNode(string(data), string(data), string(req.CA), time.Time{}); err != nil {
				t.Errorf("node.pem cannot be loaded: %v", err)
			}
		})
	}
}
