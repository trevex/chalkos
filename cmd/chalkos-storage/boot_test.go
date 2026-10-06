package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage"
)

// fakeRunner records commands and answers them from the first rule whose prefix matches.
type fakeRunner struct {
	rules []rule
	calls []string
}

type rule struct {
	prefix string
	out    string
	err    error
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	for _, r := range f.rules {
		if strings.HasPrefix(line, r.prefix) {
			return []byte(r.out), r.err
		}
	}
	return nil, nil
}

type testDisk struct {
	name, devnum string
	sectors      uint64
	props        map[string]string
}

var (
	bootDisk = testDisk{"vda", "253:0", 16 << 30 / 512, map[string]string{"ID_PATH": "pci-0000:00:04.0"}}
	dataDisk = testDisk{"vdb", "253:16", 2 << 30 / 512, map[string]string{"ID_PATH": "pci-0000:00:05.0", "ID_SERIAL": "chalk-data"}}
)

// newTestHost builds sysfs, udev and /dev trees with the disks; vda is the boot disk.
func newTestHost(t *testing.T, disks ...testDisk) storage.Host {
	t.Helper()
	root := t.TempDir()
	h := storage.Host{
		SysRoot:  filepath.Join(root, "sys"),
		UdevRoot: filepath.Join(root, "udev"),
		DevRoot:  filepath.Join(root, "dev"),
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range disks {
		dir := filepath.Join(h.SysRoot, "block", d.name)
		write(filepath.Join(dir, "dev"), d.devnum+"\n")
		write(filepath.Join(dir, "size"), strconv.FormatUint(d.sectors, 10)+"\n")
		write(filepath.Join(dir, "queue", "rotational"), "1\n")
		var props string
		for k, v := range d.props {
			props += "E:" + k + "=" + v + "\n"
		}
		write(filepath.Join(h.UdevRoot, "b"+d.devnum), props)
		write(filepath.Join(h.DevRoot, d.name), "")
	}
	if err := os.MkdirAll(filepath.Join(h.DevRoot, "disk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../vda", filepath.Join(h.DevRoot, "disk", "chalk-boot-disk")); err != nil {
		t.Fatal(err)
	}
	return h
}

func newTestBoot(t *testing.T, r *fakeRunner, disks ...testDisk) *boot {
	t.Helper()
	root := t.TempDir()
	return &boot{
		run:            r,
		host:           newTestHost(t, disks...),
		stateDir:       filepath.Join(root, "state"),
		varDir:         filepath.Join(root, "var"),
		bootDisk:       "/dev/disk/chalk-boot-disk",
		bootPartitions: "/dev/disk/chalk-boot",
		statusFile:     filepath.Join(root, "run", "storage-status.json"),
	}
}

const (
	systemSeed = "2869f04c-5655-50f4-28b9-6b2eb9700a02"
	dataSeed   = "088717f0-ffd4-dba5-a524-7d3d44310d1e"
	varUUID    = "7ad19bdf-77ff-4273-8a5c-d403d2a5f95b"
	dataUUID   = "d506b831-fde9-4335-b2be-9710f18219a6"
)

// writeStorage records a section with VAR on the system disk and a volume on a disk selected
// by its serial.
func writeStorage(t *testing.T, b *boot, fallback string) {
	t.Helper()
	section := `{
	  "disks": {
	    "system": {"ref": "/dev/vda", "seed": "` + systemSeed + `", "repart": {"50-var.conf": "[Partition]\nLabel=var\n"}},
	    "data": {"ref": {"serial": "chalk-data"}, "seed": "` + dataSeed + `", "repart": {"10-data.conf": "[Partition]\nLabel=data\n"}}
	  },
	  "volumes": {
	    "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": null},
	    "data": {"disk": "data", "label": "data", "format": "xfs", "mountPoint": "/var/lib/data", "encryption": "tpm2", "size": null}
	  },
	  "fallback": "` + fallback + `"
	}`
	path := filepath.Join(b.stateDir, "storage", "storage.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(section), 0o600); err != nil {
		t.Fatal(err)
	}
}

func repartRow(file, uuid string) string {
	return `[{"file":"` + file + `","uuid":"` + uuid + `","activity":"create"}]`
}

// firstBootRules answer like a node whose second disk is empty and whose TPM unseals VAR.
func firstBootRules(b *boot) []rule {
	return []rule{
		{prefix: "blkid -p -o export /dev/vdb", err: &toolError{command: "blkid", code: 2}},
		{prefix: "systemd-repart --dry-run=no --json=short --definitions=" + b.storageDir() + "/disks/system ", out: repartRow(b.storageDir()+"/disks/system/50-var.conf", varUUID)},
		{prefix: "systemd-repart --dry-run=no --json=short --definitions=" + b.storageDir() + "/disks/data ", out: repartRow(b.storageDir()+"/disks/data/10-data.conf", dataUUID)},
		{prefix: "blkid -p -o value -s TYPE /dev/disk/chalk-boot/var", out: "crypto_LUKS\n"},
	}
}

func readStatus(t *testing.T, b *boot) storage.Status {
	t.Helper()
	data, err := os.ReadFile(b.statusFile)
	if err != nil {
		t.Fatal(err)
	}
	var s storage.Status
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got: %s\nwant: %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
}

func TestOpenStateEncrypted(t *testing.T) {
	r := &fakeRunner{rules: []rule{{prefix: "blkid", out: "crypto_LUKS\n"}}}
	b := newTestBoot(t, r, bootDisk)

	if err := b.openState(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, r.calls, []string{
		"udevadm wait --timeout=60 /dev/disk/chalk-boot/state",
		"blkid -p -o value -s TYPE /dev/disk/chalk-boot/state",
		"systemd-cryptsetup attach state /dev/disk/chalk-boot/state - tpm2-device=auto",
		"mount -t ext4 /dev/mapper/state " + b.stateDir,
	})
}

func TestOpenStateUnencrypted(t *testing.T) {
	r := &fakeRunner{rules: []rule{{prefix: "blkid", out: "ext4\n"}}}
	b := newTestBoot(t, r, bootDisk)

	if err := b.openState(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, r.calls, []string{
		"udevadm wait --timeout=60 /dev/disk/chalk-boot/state",
		"blkid -p -o value -s TYPE /dev/disk/chalk-boot/state",
		"mount -t ext4 /dev/disk/chalk-boot/state " + b.stateDir,
	})
}

func TestSetUpWithoutSection(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk)

	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v without a storage section", r.calls)
	}
	if s := readStatus(t, b); s.Installed {
		t.Errorf("status = %+v, want not installed", s)
	}
}

func TestSetUpFirstBoot(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = firstBootRules(b)
	writeStorage(t, b, "recovery-key")

	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}

	assertCalls(t, r.calls, []string{
		"udevadm settle --timeout=30",
		"blkid -p -o export /dev/vdb",
		"systemd-repart --dry-run=no --json=short --definitions=" + b.storageDir() + "/disks/system --seed=" + systemSeed + " /dev/disk/chalk-boot-disk",
		"systemd-repart --dry-run=no --json=short --definitions=" + b.storageDir() + "/disks/data --seed=" + dataSeed + " --empty=allow /dev/vdb",
		"udevadm wait --timeout=60 /dev/disk/chalk-boot/var",
		"blkid -p -o value -s TYPE /dev/disk/chalk-boot/var",
		"systemd-cryptsetup attach var /dev/disk/chalk-boot/var - tpm2-device=auto",
		"mount -t ext4 /dev/mapper/var " + b.varDir,
		"systemd-growfs " + b.varDir,
	})
	pins, err := storage.ReadPins(filepath.Join(b.storageDir(), "disks.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]storage.Pin{
		"system": {
			Ref:        storage.Ref{Path: "/dev/vda"},
			Identity:   storage.Identity{Path: "pci-0000:00:04.0", Size: 16 << 30, Type: "hdd"},
			Partitions: map[string]string{"var": varUUID},
		},
		"data": {
			Ref:        storage.Ref{Selector: storage.Selector{Serial: "chalk-data"}},
			Identity:   storage.Identity{Serial: "chalk-data", Path: "pci-0000:00:05.0", Size: 2 << 30, Type: "hdd"},
			Partitions: map[string]string{"data": dataUUID},
		},
	}
	if !reflect.DeepEqual(pins.Disks, want) {
		t.Errorf("pins = %+v, want %+v", pins.Disks, want)
	}
	if def, err := os.ReadFile(filepath.Join(b.storageDir(), "disks", "data", "10-data.conf")); err != nil || string(def) != "[Partition]\nLabel=data\n" {
		t.Errorf("data definition = %q, %v", def, err)
	}
	s := readStatus(t, b)
	if !s.Installed || s.Disks["data"] != (storage.DiskStatus{Device: "/dev/vdb"}) || s.Disks["system"].Error != "" {
		t.Errorf("status = %+v", s)
	}
}

func TestSetUpUsesPinnedDisks(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = firstBootRules(b)
	writeStorage(t, b, "recovery-key")
	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The second boot finds the data disk by its pinned identity and does not probe it again.
	r.calls = nil
	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls {
		if strings.HasPrefix(call, "blkid -p -o export") {
			t.Errorf("probed a pinned disk again: %s", call)
		}
		if strings.HasPrefix(call, "udevadm settle") {
			t.Errorf("waited for udev without a reference to resolve: %s", call)
		}
	}
}

func TestSetUpRefusesUnidentifiableDisk(t *testing.T) {
	r := &fakeRunner{}
	anonymous := dataDisk
	anonymous.props = map[string]string{"ID_MODEL": "QEMU_HARDDISK"}
	b := newTestBoot(t, r, bootDisk, anonymous)
	r.rules = firstBootRules(b)
	writeSection(t, b, `{
	  "disks": {
	    "system": {"ref": "/dev/vda", "seed": "`+systemSeed+`", "repart": {"50-var.conf": "[Partition]\nLabel=var\n"}},
	    "data": {"ref": "/dev/vdb", "seed": "`+dataSeed+`", "repart": {"10-data.conf": "[Partition]\nLabel=data\n"}}
	  },
	  "volumes": {
	    "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": null},
	    "data": {"disk": "data", "label": "data", "format": "xfs", "mountPoint": "/var/lib/data", "encryption": "tpm2", "size": null}
	  },
	  "fallback": "recovery-key"
	}`)

	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "/dev/vdb") {
			t.Errorf("touched a disk that cannot be recognised again: %s", call)
		}
	}
	pins, err := storage.ReadPins(filepath.Join(b.storageDir(), "disks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins.Disks["data"]; ok {
		t.Error("pinned a disk without WWN, serial or path")
	}
	if s := readStatus(t, b); !strings.Contains(s.Disks["data"].Error, "cannot be recognised") {
		t.Errorf("data status = %+v", s.Disks["data"])
	}
}

func TestSetUpRefusesUnidentifiableBootDisk(t *testing.T) {
	r := &fakeRunner{}
	anonymous := bootDisk
	anonymous.props = nil
	b := newTestBoot(t, r, anonymous, dataDisk)
	r.rules = firstBootRules(b)
	writeStorage(t, b, "recovery-key")

	if err := b.setUp(context.Background()); err != nil {
		t.Fatalf("an unidentifiable boot disk stopped the boot: %v", err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "disks/system") || strings.HasPrefix(call, "mount") {
			t.Errorf("set up VAR on a disk that cannot be recognised again: %s", call)
		}
	}
	pins, err := storage.ReadPins(filepath.Join(b.storageDir(), "disks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins.Disks["system"]; ok {
		t.Error("pinned a boot disk without WWN, serial or path")
	}
	if s := readStatus(t, b); !strings.Contains(s.Disks["system"].Error, "cannot be recognised") {
		t.Errorf("system status = %+v", s.Disks["system"])
	}
}

func TestSetUpWaitsForUdevBeforeResolving(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = append([]rule{{prefix: "udevadm settle", err: &toolError{command: "udevadm settle", code: 1, stderr: "timeout"}}}, firstBootRules(b)...)
	writeStorage(t, b, "recovery-key")

	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "/dev/vdb") {
			t.Errorf("resolved a reference before udev settled: %s", call)
		}
	}
	if !contains(r.calls, "mount -t ext4 /dev/mapper/var "+b.varDir) {
		t.Errorf("VAR not mounted: %v", r.calls)
	}
	if s := readStatus(t, b); !strings.Contains(s.Disks["data"].Error, "udev") {
		t.Errorf("data status = %+v", s.Disks["data"])
	}
}

func TestSetUpPinnedDiskMissing(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = firstBootRules(b)
	writeStorage(t, b, "recovery-key")
	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Boot again without the data disk.
	r.calls = nil
	b.host = newTestHost(t, bootDisk)
	if err := b.setUp(context.Background()); err != nil {
		t.Fatalf("a missing extra disk stopped the boot: %v", err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "disks/data") {
			t.Errorf("ran repart for the missing disk: %s", call)
		}
	}
	if !contains(r.calls, "mount -t ext4 /dev/mapper/var "+b.varDir) {
		t.Errorf("VAR not mounted: %v", r.calls)
	}
	if s := readStatus(t, b); !strings.Contains(s.Disks["data"].Error, `pinned disk model "", size 2G, serial "chalk-data" is missing`) {
		t.Errorf("data status = %+v", s.Disks["data"])
	}
}

func TestSetUpRefusesOtherBootDisk(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = firstBootRules(b)
	writeStorage(t, b, "recovery-key")
	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The same image boots from a disk on another port: VAR's disk is not there.
	r.calls = nil
	moved := bootDisk
	moved.props = map[string]string{"ID_PATH": "pci-0000:00:09.0"}
	b.host = newTestHost(t, moved, dataDisk)
	err := b.setUp(context.Background())
	if err == nil || !strings.Contains(err.Error(), "VAR lives on the disk") || !strings.Contains(err.Error(), "pci-0000:00:04.0") {
		t.Fatalf("err = %v, want VAR's disk named", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v on a foreign boot disk", r.calls)
	}
}

func TestSetUpRefusesDiskWithData(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = append([]rule{{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nTYPE=ext4\n"}}, firstBootRules(b)...)
	writeStorage(t, b, "recovery-key")

	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "/dev/vdb") && !strings.HasPrefix(call, "blkid") {
			t.Errorf("touched a disk with a file system: %s", call)
		}
	}
	pins, err := storage.ReadPins(filepath.Join(b.storageDir(), "disks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins.Disks["data"]; ok {
		t.Error("pinned a disk that carries a file system")
	}
	if s := readStatus(t, b); !strings.Contains(s.Disks["data"].Error, "carries data (ext4)") {
		t.Errorf("data status = %+v", s.Disks["data"])
	}
}

const dataType = "4ddbee6c-635c-dcb9-bb95-26d82d7c4cff"

// sfdiskTable is `sfdisk --json` output for a GPT on /dev/vdb with partitions of the types.
func sfdiskTable(types ...string) string {
	parts := make([]string, len(types))
	for i, typ := range types {
		parts[i] = `{"node": "/dev/vdb` + strconv.Itoa(i+1) + `", "start": 2048, "size": 2048, "type": "` + typ + `", "uuid": "` + dataUUID + `"}`
	}
	table := `{"partitiontable": {"label": "gpt", "id": "C5A8B6E2-4E09-4D47-9A4C-1E1C2C6F1B2A", "device": "/dev/vdb", "unit": "sectors", "firstlba": 2048, "lastlba": 4194270, "sectorsize": 512`
	if len(parts) > 0 {
		table += `, "partitions": [` + strings.Join(parts, ", ") + `]`
	}
	return table + "}}"
}

func TestSetUpChecksNewDiskIsUnused(t *testing.T) {
	gpt := rule{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nPTUUID=c5a8b6e2-4e09-4d47-9a4c-1e1c2c6f1b2a\nPTTYPE=gpt\n"}
	for _, c := range []struct {
		name  string
		rules []rule
		// refusal is the status error, or empty when the disk may be partitioned.
		refusal string
	}{
		{"blkid finds nothing", nil, ""},
		{"blkid fails without finding anything", []rule{{prefix: "blkid -p -o export /dev/vdb", err: &toolError{command: "blkid", code: 2, stderr: "error: /dev/vdb: Input/output error"}}}, "Input/output error"},
		{"blkid fails otherwise", []rule{{prefix: "blkid -p -o export /dev/vdb", err: &toolError{command: "blkid", code: 4}}}, "probe /dev/vdb"},
		{"file system", []rule{{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nTYPE=ext4\n"}}, "carries data (ext4)"},
		{"empty GPT", []rule{gpt, {prefix: "sfdisk --json /dev/vdb", out: sfdiskTable()}}, ""},
		{"GPT with chalkos partitions", []rule{gpt, {prefix: "sfdisk --json /dev/vdb", out: sfdiskTable(strings.ToUpper(dataType))}}, ""},
		{"GPT with a foreign partition", []rule{gpt, {prefix: "sfdisk --json /dev/vdb", out: sfdiskTable(dataType, "0FC63DAF-8483-4772-8E79-3D69D8477DE4")}}, "/dev/vdb2 has the partition type 0FC63DAF-8483-4772-8E79-3D69D8477DE4"},
		{"partition table unreadable", []rule{gpt, {prefix: "sfdisk --json /dev/vdb", err: &toolError{command: "sfdisk", code: 1}}}, "read the partition table"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeRunner{}
			b := newTestBoot(t, r, bootDisk, dataDisk)
			r.rules = append(c.rules, firstBootRules(b)...)
			writeSection(t, b, `{
			  "disks": {
			    "system": {"ref": "/dev/vda", "seed": "`+systemSeed+`", "repart": {"50-var.conf": "[Partition]\nLabel=var\n"}},
			    "data": {"ref": {"serial": "chalk-data"}, "seed": "`+dataSeed+`", "repart": {"10-data.conf": "[Partition]\nType=`+dataType+`\nLabel=data\n"}}
			  },
			  "volumes": {
			    "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": null},
			    "data": {"disk": "data", "label": "data", "format": "xfs", "mountPoint": "/var/lib/data", "encryption": "tpm2", "size": null}
			  },
			  "fallback": "recovery-key"
			}`)

			if err := b.setUp(context.Background()); err != nil {
				t.Fatal(err)
			}
			repartRan := false
			for _, call := range r.calls {
				repartRan = repartRan || strings.HasPrefix(call, "systemd-repart") && strings.HasSuffix(call, " /dev/vdb")
			}
			status := readStatus(t, b).Disks["data"]
			if c.refusal == "" {
				if !repartRan || status.Error != "" {
					t.Errorf("refused an unused disk: %+v", status)
				}
				return
			}
			if repartRan {
				t.Error("ran repart on a disk that is not provably unused")
			}
			if !strings.Contains(status.Error, c.refusal) {
				t.Errorf("data status = %+v, want %q", status, c.refusal)
			}
		})
	}
}

func TestSetUpRepartFailureKeepsBooting(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = append([]rule{{
		prefix: "systemd-repart --dry-run=no --json=short --definitions=" + b.storageDir() + "/disks/system ",
		err:    &toolError{command: "systemd-repart", code: 1, stderr: "Can't fit requested partitions into available free space"},
	}}, firstBootRules(b)...)
	writeStorage(t, b, "recovery-key")

	if err := b.setUp(context.Background()); err != nil {
		t.Fatalf("a repart failure stopped the boot: %v", err)
	}
	for _, call := range r.calls {
		if strings.HasPrefix(call, "mount") {
			t.Errorf("mounted VAR that repart never created: %s", call)
		}
	}
	if s := readStatus(t, b); !strings.Contains(s.Disks["system"].Error, "Can't fit") {
		t.Errorf("system status = %+v", s.Disks["system"])
	}
}

func TestSetUpRefusesUnencryptedVarMeantToBeEncrypted(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = append([]rule{{prefix: "blkid -p -o value -s TYPE /dev/disk/chalk-boot/var", out: "ext4\n"}}, firstBootRules(b)...)
	writeStorage(t, b, "recovery-key")

	err := b.setUp(context.Background())
	if err == nil || !strings.Contains(err.Error(), "var") || !strings.Contains(err.Error(), "LUKS") {
		t.Fatalf("err = %v, want a refusal naming var and LUKS", err)
	}
	for _, call := range r.calls {
		if strings.HasPrefix(call, "mount") {
			t.Errorf("mounted a plain VAR that is meant to be encrypted: %s", call)
		}
	}
}

func TestSetUpMountsUnencryptedVar(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk)
	r.rules = append([]rule{{prefix: "blkid -p -o value -s TYPE /dev/disk/chalk-boot/var", out: "ext4\n"}}, firstBootRules(b)...)
	writeSection(t, b, `{
	  "disks": {"system": {"ref": "/dev/vda", "seed": "`+systemSeed+`", "repart": {"50-var.conf": "[Partition]\nLabel=var\n"}}},
	  "volumes": {"var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "none", "size": null}},
	  "fallback": "recovery-key"
	}`)

	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !contains(r.calls, "mount -t ext4 /dev/disk/chalk-boot/var "+b.varDir) {
		t.Errorf("unencrypted VAR not mounted: %v", r.calls)
	}
}

func TestSetUpWithoutFallbackDoesNotPrompt(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = append([]rule{{prefix: "systemd-cryptsetup attach var", err: &toolError{command: "systemd-cryptsetup", code: 1, stderr: "No TPM2 metadata matching the current system state found in LUKS2 header, falling back to traditional unlocking.\nPassword querying disabled via 'headless' option.\n"}}}, firstBootRules(b)...)
	writeStorage(t, b, "none")

	err := b.setUp(context.Background())
	if !contains(r.calls, "systemd-cryptsetup attach var /dev/disk/chalk-boot/var - tpm2-device=auto,headless=true") {
		t.Errorf("commands = %v, want a headless attach", r.calls)
	}
	if err == nil || !strings.Contains(err.Error(), "chalkctl storage reset <node> var") {
		t.Errorf("err = %v, want the reset hint", err)
	}
}

// writeSection records a section given as JSON.
func writeSection(t *testing.T, b *boot, section string) {
	t.Helper()
	path := filepath.Join(b.stateDir, "storage", "storage.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(section), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSetUpRefusesReferenceToPinnedDisk(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = firstBootRules(b)
	writeStorage(t, b, "recovery-key")
	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A new disk, ordered before the pinned one, references the pinned data disk by its path.
	writeSection(t, b, `{
	  "disks": {
	    "system": {"ref": "/dev/vda", "seed": "`+systemSeed+`", "repart": {"50-var.conf": "[Partition]\nLabel=var\n"}},
	    "alpha": {"ref": "/dev/vdb", "seed": "`+dataSeed+`", "repart": {"10-alpha.conf": "[Partition]\nLabel=alpha\n"}},
	    "data": {"ref": {"serial": "chalk-data"}, "seed": "`+dataSeed+`", "repart": {"10-data.conf": "[Partition]\nLabel=data\n"}}
	  },
	  "volumes": {
	    "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": null},
	    "alpha": {"disk": "alpha", "label": "alpha", "format": "ext4", "mountPoint": "/srv/alpha", "encryption": "tpm2", "size": null},
	    "data": {"disk": "data", "label": "data", "format": "xfs", "mountPoint": "/var/lib/data", "encryption": "tpm2", "size": null}
	  },
	  "fallback": "recovery-key"
	}`)
	r.calls = nil
	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "disks/alpha") {
			t.Errorf("ran repart for a reference to a pinned disk: %s", call)
		}
	}
	pins, err := storage.ReadPins(filepath.Join(b.storageDir(), "disks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins.Disks["alpha"]; ok {
		t.Error("pinned a disk that another disk name already holds")
	}
	if s := readStatus(t, b); !strings.Contains(s.Disks["alpha"].Error, "disk data already uses") {
		t.Errorf("alpha status = %+v", s.Disks["alpha"])
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

func TestSetUpWithoutFallbackReportsOtherAttachFailures(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = append([]rule{{prefix: "systemd-cryptsetup attach var", err: &toolError{command: "systemd-cryptsetup", code: 1, stderr: "Failed to load LUKS superblock on device /dev/disk/chalk-boot/var: Input/output error\n"}}}, firstBootRules(b)...)
	writeStorage(t, b, "none")

	err := b.setUp(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("err = %v, want the attach error", err)
	}
	if strings.Contains(err.Error(), "reset") {
		t.Errorf("err = %v suggests a reset for a failure that is not the TPM's", err)
	}
}

func TestSetUpRefusesEmptySeed(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = firstBootRules(b)
	writeSection(t, b, `{
	  "disks": {
	    "system": {"ref": "/dev/vda", "seed": "`+systemSeed+`", "repart": {"50-var.conf": "[Partition]\nLabel=var\n"}},
	    "data": {"ref": {"serial": "chalk-data"}, "seed": "", "repart": {"10-data.conf": "[Partition]\nLabel=data\n"}}
	  },
	  "volumes": {
	    "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": null},
	    "data": {"disk": "data", "label": "data", "format": "xfs", "mountPoint": "/var/lib/data", "encryption": "tpm2", "size": null}
	  },
	  "fallback": "recovery-key"
	}`)

	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "--seed=") && strings.Contains(call, "disks/data") {
			t.Errorf("ran repart without a seed: %s", call)
		}
	}
	if s := readStatus(t, b); !strings.Contains(s.Disks["data"].Error, "seed") {
		t.Errorf("data status = %+v", s.Disks["data"])
	}
}

func TestSetUpRefreshesPinThatGainedIdentifiers(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBoot(t, r, bootDisk, dataDisk)
	r.rules = firstBootRules(b)
	writeStorage(t, b, "recovery-key")
	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}

	// udev now reports a WWN for the data disk, which it did not when the disk was pinned.
	r.calls = nil
	gained := dataDisk
	gained.props = map[string]string{"ID_PATH": "pci-0000:00:05.0", "ID_SERIAL": "chalk-data", "ID_WWN": "0x5000c500a1b2c3d4"}
	b.host = newTestHost(t, bootDisk, gained)
	if err := b.setUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !contains(r.calls, "systemd-repart --dry-run=no --json=short --definitions="+b.storageDir()+"/disks/data --seed="+dataSeed+" --empty=allow /dev/vdb") {
		t.Errorf("the data disk was not found again: %v", r.calls)
	}
	pins, err := storage.ReadPins(filepath.Join(b.storageDir(), "disks.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := storage.Identity{WWN: "0x5000c500a1b2c3d4", Serial: "chalk-data", Path: "pci-0000:00:05.0", Size: 2 << 30, Type: "hdd"}
	if got := pins.Disks["data"].Identity; got != want {
		t.Errorf("data identity = %+v, want %+v", got, want)
	}
	if got := pins.Disks["data"].Partitions["data"]; got != dataUUID {
		t.Errorf("data partition = %q, want %q", got, dataUUID)
	}
}
