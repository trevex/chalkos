package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// target is the installer's target disk; small, as tests read it back whole.
var target = testDisk{"vdb", "253:16", 64 * mib, map[string]string{"ID_PATH": "pci-0000:00:05.0", "ID_SERIAL": "chalk-target"}}

// diskFile stands for the target disk: a file of the disk's size. It returns the file and how
// often the disk's partitions were reread.
func diskFile(t *testing.T, i *Installer, size int64) (string, *int) {
	reread := new(int)
	t.Helper()
	path := filepath.Join(t.TempDir(), "vdb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Old data at both ends, as a used disk has.
	f.WriteAt(bytes.Repeat([]byte{0xa5}, mib), 0)
	f.WriteAt(bytes.Repeat([]byte{0xa5}, mib), size-mib)
	f.Close()
	i.OpenDisk = func(dev string) (Disk, error) {
		if dev != "/dev/vdb" {
			t.Errorf("opened %s, want /dev/vdb", dev)
		}
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		return fileDisk{File: f, reread: reread}, err
	}
	return path, reread
}

// fileDisk is a file standing for a disk; it counts how often its partitions were reread.
type fileDisk struct {
	*os.File
	reread *int
}

func (d fileDisk) RereadPartitions() error { *d.reread++; return nil }

func testImage(t *testing.T) []byte {
	t.Helper()
	image := make([]byte, 3*mib+512)
	if _, err := rand.Read(image); err != nil {
		t.Fatal(err)
	}
	return image
}

func mediaRequest(t *testing.T, image []byte) MediaRequest {
	t.Helper()
	sum := sha256.Sum256(image)
	return MediaRequest{
		Request:           testRequest(t, testSection(storage.EncryptionTPM2, "recovery-key", "/dev/vdb")),
		Target:            storage.Ref{Selector: storage.Selector{Serial: "chalk-target"}},
		Image:             bytes.NewReader(image),
		ImageSize:         int64(len(image)),
		ImageSHA256:       sum[:],
		SystemDefinitions: systemDefinitions,
	}
}

// mediaRules answer like an installer whose target vdb is empty and gets STATE and VAR.
func mediaRules(i *Installer) []*rule {
	return []*rule{
		{prefix: "blkid -p -o export /dev/vdb", err: blkidNothing},
		// The written image has no STATE until repart creates it.
		{prefix: "sfdisk --json /dev/vdb", out: sfdisk("/dev/vdb", espPart, usrPart), times: 1},
		{prefix: "sfdisk --json /dev/vdb", out: sfdisk("/dev/vdb", espPart, usrPart, statePart, varPart)},
		{prefix: "blkid -p -o value -s TYPE", out: "crypto_LUKS\n"},
		{prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(i.StateDir, "storage", "disks", "system"), out: repartRow(filepath.Join(i.StateDir, "storage", "disks", "system", "50-var.conf"), varUUID)},
		{prefix: "efibootmgr --verbose", out: "BootCurrent: 0001\nBootOrder: 0004,0001\nBoot0001* UEFI Misc Device\tPciRoot(0x0)/Pci(0x4,0x0){auto_created_boot_option}\nBoot0004* chalkos\tHD(1,GPT,00000001-0000-4000-8000-000000000000,0x800,0x80000)/\\EFI\\BOOT\\BOOTX64.EFI\n"},
	}
}

func TestFromMediaInstallsOnTarget(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, target)
	r.add(mediaRules(i)...)
	path, reread := diskFile(t, i, int64(target.size))
	image := testImage(t)
	req := mediaRequest(t, image)
	if err := i.FromMedia(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written[:len(image)], image) {
		t.Error("the disk does not start with the image")
	}
	if !bytes.Equal(written[len(written)-mib:], make([]byte, mib)) {
		t.Error("the end of the disk, where an old backup GPT lives, was not cleared")
	}
	if *reread != 1 {
		t.Errorf("the partition table was reread %d times, want once after writing the image", *reread)
	}

	system := filepath.Join(i.WorkDir, "system")
	for _, want := range []string{
		"blkid -p -o export /dev/vdb",
		"systemd-repart --dry-run=no --definitions=" + system + " --tpm2-pcrs=7 /dev/vdb",
		"systemd-cryptsetup attach state /dev/vdb6 - tpm2-device=auto,headless=true",
		"systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(i.StateDir, "storage", "disks", "system") + " --seed=" + systemSeed + " /dev/vdb",
		"systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password /dev/vdb6",
		"systemd-cryptenroll --unlock-tpm2-device=auto --password --wipe-slot=password /dev/vdb7",
		"efibootmgr --create --disk /dev/vdb --part 1 --label chalkos --loader \\EFI\\BOOT\\BOOTX64.EFI",
		"efibootmgr --bootnext 0004",
	} {
		if !hasPrefix(r.calls, want) {
			t.Errorf("missing %q in\n%s", want, strings.Join(r.calls, "\n"))
		}
	}
	for _, c := range r.calls {
		if strings.Contains(c, "/dev/vda") {
			t.Errorf("touched the installer's own disk: %s", c)
		}
	}
	for name, text := range systemDefinitions {
		got, err := os.ReadFile(filepath.Join(system, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "50-state.conf" && !strings.HasSuffix(string(got), "Encrypt=tpm2\n") {
			t.Errorf("STATE definition = %q, want it encrypted", got)
		} else if name != "50-state.conf" && string(got) != text {
			t.Errorf("%s = %q, want the role image's definition", name, got)
		}
	}
	pins, err := storage.ReadPins(filepath.Join(i.StateDir, "storage", "disks.json"))
	if err != nil || pins.Disks["system"].Identity.Serial != "chalk-target" {
		t.Errorf("system disk pinned as %+v, %v; want the target", pins.Disks["system"], err)
	}
	if !Installed(i.StateDir) {
		t.Error("the installed marker is missing")
	}
}

func TestFromMediaRejectsCorruptImage(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(req *MediaRequest, image []byte)
		want   string
	}{
		{"checksum", func(req *MediaRequest, image []byte) {
			req.ImageSHA256 = make([]byte, sha256.Size)
		}, "SHA-256"},
		{"short", func(req *MediaRequest, image []byte) {
			req.Image = bytes.NewReader(image[:len(image)-1])
		}, "had 3146239 bytes"},
		{"long", func(req *MediaRequest, image []byte) {
			req.Image = bytes.NewReader(append(append([]byte{}, image...), 0))
		}, "had 3146241 bytes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeRunner{}
			i := newTestInstaller(t, r, vda, target)
			r.add(mediaRules(i)...)
			path, reread := diskFile(t, i, int64(target.size))
			image := testImage(t)
			req := mediaRequest(t, image)
			c.change(&req, image)

			err := i.FromMedia(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			written, _ := os.ReadFile(path)
			if !bytes.Equal(written[:mib], make([]byte, mib)) {
				t.Error("the first MiB of the target was not cleared")
			}
			if *reread != 0 {
				t.Error("reread the partition table of a bad image")
			}
			if len(r.calls) != 1 {
				t.Errorf("went on after a bad image: %v", r.calls)
			}
			if Installed(i.StateDir) {
				t.Error("installed a bad image")
			}
		})
	}
}

func TestFromMediaChecksTarget(t *testing.T) {
	gpt := &rule{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nPTUUID=c5a8b6e2\nPTTYPE=gpt\n"}
	foreign := part{2, "0FC63DAF-8483-4772-8E79-3D69D8477DE4", "home"}
	for _, c := range []struct {
		name  string
		rules []*rule
		wipe  bool
		// refusal is part of the error, or empty when the install goes ahead.
		refusal string
	}{
		{"empty", nil, false, ""},
		{"file system", []*rule{{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nTYPE=ext4\n"}}, false, "carries data (ext4)"},
		{"file system, wiped", []*rule{{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nTYPE=ext4\n"}}, true, ""},
		{"chalkos node", []*rule{gpt, {prefix: "sfdisk --json /dev/vdb", out: sfdisk("/dev/vdb", espPart, usrPart, statePart, varPart), times: 1}}, false, ""},
		{"foreign partition", []*rule{gpt, {prefix: "sfdisk --json /dev/vdb", out: sfdisk("/dev/vdb", espPart, foreign), times: 1}}, false, `/dev/vdb2 (type 0fc63daf-8483-4772-8e79-3d69d8477de4, label "home")`},
		{"probe fails", []*rule{{prefix: "blkid -p -o export /dev/vdb", err: &node.ToolError{Command: "blkid", Code: 2, Stderr: "error: /dev/vdb: Input/output error"}}}, false, "Input/output error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeRunner{}
			i := newTestInstaller(t, r, vda, target)
			r.add(c.rules...)
			r.add(mediaRules(i)...)
			opened := false
			path, _ := diskFile(t, i, int64(target.size))
			open := i.OpenDisk
			i.OpenDisk = func(dev string) (Disk, error) { opened = true; return open(dev) }
			req := mediaRequest(t, testImage(t))
			req.WipeDisk = c.wipe

			err := i.FromMedia(context.Background(), req)
			if c.refusal == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.refusal) {
				t.Fatalf("err = %v, want %q", err, c.refusal)
			}
			if opened {
				t.Error("opened a disk that must not be overwritten")
			}
			if data, _ := os.ReadFile(path); data[0] != 0xa5 {
				t.Error("changed the refused disk")
			}
		})
	}
}

func TestFromMediaRefusesInstallerDisk(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, target)
	req := mediaRequest(t, testImage(t))
	req.Target = storage.Ref{Path: "/dev/vda"}
	req.WipeDisk = true
	err := i.FromMedia(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "the installer runs from") {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
	}
}

func TestFromMediaRefusesDiskInUse(t *testing.T) {
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, target)
	r.add(mediaRules(i)...)
	i.OpenDisk = func(string) (Disk, error) {
		return nil, &os.PathError{Op: "open", Path: "/dev/vdb", Err: syscall.EBUSY}
	}
	err := i.FromMedia(context.Background(), mediaRequest(t, testImage(t)))
	if err == nil || !strings.Contains(err.Error(), "is in use") {
		t.Fatalf("err = %v", err)
	}
}

func TestFromMediaRefusesImageLargerThanDisk(t *testing.T) {
	r := &fakeRunner{}
	small := target
	small.size = 2 * mib
	i := newTestInstaller(t, r, vda, small)
	err := i.FromMedia(context.Background(), mediaRequest(t, testImage(t)))
	if err == nil || !strings.Contains(err.Error(), "more than the target disk") {
		t.Fatalf("err = %v", err)
	}
}
