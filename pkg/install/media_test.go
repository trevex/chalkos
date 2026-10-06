package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// target is the installer's target disk; small, as tests read it back whole.
var target = testDisk{"vdb", "253:16", 64 * mib, map[string]string{"ID_PATH": "pci-0000:00:05.0", "ID_SERIAL": "chalk-target"}}

const (
	// staleUUID is the ESP of an earlier attempt, which is on no disk any more.
	staleUUID = "5ca1ab1e-0000-4000-8000-000000000000"
	// otherUUID is the ESP of a chalkos node on another disk.
	otherUUID = "0717e400-0000-4000-8000-000000000000"
)

// newMediaInstaller is an installer booted from vda with the target vdb, neither of whose
// partitions is mounted or open. Its firmware has an entry of its own, one left by an earlier
// attempt and one of a chalkos node on another disk.
func newMediaInstaller(t *testing.T, r *fakeRunner) *Installer {
	t.Helper()
	i := newTestInstaller(t, r, vda, target)
	write(t, i.MountInfo, "30 1 0:27 / / rw - tmpfs tmpfs rw\n")
	if err := os.Remove(filepath.Join(i.Host.DevRoot, "mapper", "state")); err != nil {
		t.Fatal(err)
	}
	links := filepath.Join(i.Host.DevRoot, "disk", "by-partuuid")
	if err := os.MkdirAll(links, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../vda", filepath.Join(links, otherUUID)); err != nil {
		t.Fatal(err)
	}
	r.efi = &fakeEFI{
		entries: []efiEntry{
			{"0001", "UEFI Misc Device", ""},
			{"0003", "chalkos", staleUUID},
			{"0005", "chalkos", otherUUID},
		},
		next: 6,
	}
	return i
}

// diskLog records what happened to the target disk.
type diskLog struct {
	reread int
	writes []diskWrite
}

type diskWrite struct {
	off  int64
	n    int
	zero bool
}

// wroteHeader reports whether anything but zeros was written to the disk's first MiB.
func (l *diskLog) wroteHeader() bool {
	for _, w := range l.writes {
		if w.off < mib && !w.zero {
			return true
		}
	}
	return false
}

// diskFile stands for the target disk: a file of the disk's size. Like the kernel, it refuses
// to open the disk while one of its partitions is mounted or open.
func diskFile(t *testing.T, i *Installer, size int64) (string, *diskLog) {
	t.Helper()
	log := &diskLog{}
	path := filepath.Join(t.TempDir(), "vdb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Old data at both ends, as a used disk has.
	f.WriteAt(bytes.Repeat([]byte{0xa5}, mib), 0)
	f.WriteAt(bytes.Repeat([]byte{0xa5}, mib), size-mib)
	f.Close()
	r := i.Run.(*fakeRunner)
	i.OpenDisk = func(_ context.Context, dev string) (Disk, error) {
		if dev != "/dev/vdb" {
			t.Errorf("opened %s, want /dev/vdb", dev)
		}
		if r.holds(dev) {
			return nil, &os.PathError{Op: "open", Path: dev, Err: syscall.EBUSY}
		}
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		return fileDisk{File: f, log: log}, err
	}
	return path, log
}

// fileDisk is a file standing for a disk; it records writes and partition table rereads.
type fileDisk struct {
	*os.File
	log *diskLog
}

func (d fileDisk) WriteAt(p []byte, off int64) (int, error) {
	d.log.writes = append(d.log.writes, diskWrite{off, len(p), !bytes.ContainsFunc(p, func(r rune) bool { return r != 0 })})
	return d.File.WriteAt(p, off)
}

func (d fileDisk) RereadPartitions() error { d.log.reread++; return nil }

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
	}
}

func TestFromMediaInstallsOnTarget(t *testing.T) {
	r := &fakeRunner{}
	i := newMediaInstaller(t, r)
	r.add(mediaRules(i)...)
	path, disk := diskFile(t, i, int64(target.size))
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
	if disk.reread != 1 {
		t.Errorf("the partition table was reread %d times, want once after writing the image", disk.reread)
	}
	var last diskWrite
	for _, w := range disk.writes {
		if !w.zero {
			last = w
		}
	}
	if last.off != 0 || last.n != mib {
		t.Errorf("the last write of image data was %+v, want the image's first MiB", last)
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
		"efibootmgr --bootnext 0006",
	} {
		if !hasPrefix(r.calls, want) {
			t.Errorf("missing %q in\n%s", want, strings.Join(r.calls, "\n"))
		}
	}
	for _, c := range r.calls {
		if strings.Contains(c, "/dev/vda") {
			t.Errorf("touched the installer's own disk: %s", c)
		}
		if strings.Contains(c, secret) {
			t.Errorf("the fallback secret is on a command line: %s", c)
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
	if !installed(t, i.StateDir) {
		t.Error("the installed marker is missing")
	}
}

func TestFromMediaBootsTheNewEntry(t *testing.T) {
	r := &fakeRunner{}
	i := newMediaInstaller(t, r)
	r.add(mediaRules(i)...)
	diskFile(t, i, int64(target.size))
	if err := i.FromMedia(context.Background(), mediaRequest(t, testImage(t))); err != nil {
		t.Fatal(err)
	}
	espUUID := strings.ToLower(r.partUUIDs["/dev/vdb 1"])
	if espUUID == "" {
		t.Fatal("the ESP's partition UUID was not set")
	}
	if r.efi.bootNext != "0006" {
		t.Errorf("BootNext = %q, want the new entry 0006", r.efi.bootNext)
	}
	want := []efiEntry{
		{"0001", "UEFI Misc Device", ""},
		{"0005", "chalkos", otherUUID},
		{"0006", "chalkos", espUUID},
	}
	if !reflect.DeepEqual(r.efi.entries, want) {
		t.Errorf("boot entries = %+v, want %+v", r.efi.entries, want)
	}
	if !reflect.DeepEqual(r.efi.deleted, []string{"0003"}) {
		t.Errorf("deleted boot entries %v, want only the stale 0003", r.efi.deleted)
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
		{"shorter than a MiB", func(req *MediaRequest, image []byte) {
			req.Image = bytes.NewReader(image[:mib/2])
		}, "had 524288 bytes"},
		{"fails halfway", func(req *MediaRequest, image []byte) {
			req.Image = io.MultiReader(bytes.NewReader(image[:2*mib]), iotest.ErrReader(errors.New("connection reset")))
		}, "connection reset"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeRunner{}
			i := newMediaInstaller(t, r)
			r.add(mediaRules(i)...)
			path, disk := diskFile(t, i, int64(target.size))
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
			if disk.wroteHeader() {
				t.Error("wrote the image's first MiB before the image was verified")
			}
			if disk.reread != 0 {
				t.Error("reread the partition table of a bad image")
			}
			if len(r.calls) != 1 {
				t.Errorf("went on after a bad image: %v", r.calls)
			}
			if installed(t, i.StateDir) {
				t.Error("installed a bad image")
			}
		})
	}
}

func TestFromMediaChecksTarget(t *testing.T) {
	gpt := &rule{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nPTUUID=c5a8b6e2\nPTTYPE=gpt\n"}
	table := func(parts ...part) *rule {
		return &rule{prefix: "sfdisk --json /dev/vdb", out: sfdisk("/dev/vdb", parts...), times: 1}
	}
	foreign := part{2, "0FC63DAF-8483-4772-8E79-3D69D8477DE4", "home"}
	volume := part{8, strings.ToUpper(storage.PartitionType("data")), "data"}
	usrVerity := part{2, strings.ToUpper(typeUsrX86Verity), "usr-x86-64-verity"}
	for _, c := range []struct {
		name  string
		rules []*rule
		// refusal is part of the error without --wipe-disk, or empty when the install goes ahead.
		refusal string
	}{
		{"empty", nil, ""},
		{"file system", []*rule{{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nTYPE=ext4\n"}}, "carries data (ext4)"},
		{"partition table and file system", []*rule{{prefix: "blkid -p -o export /dev/vdb", out: "DEVNAME=/dev/vdb\nPTTYPE=gpt\nTYPE=iso9660\n"}}, "carries data (gpt iso9660)"},
		{"image only", []*rule{gpt, table(espPart, usrVerity, usrPart)}, ""},
		{"STATE", []*rule{gpt, table(espPart, usrPart, statePart)}, "partition 6 (chalkos STATE); pass --wipe-disk to replace it"},
		{"VAR", []*rule{gpt, table(espPart, usrPart, varPart)}, "partition 7 (chalkos VAR); pass --wipe-disk"},
		{"installed node", []*rule{gpt, table(espPart, usrPart, statePart, varPart)}, "partition 6 (chalkos STATE), partition 7 (chalkos VAR)"},
		{"volume", []*rule{gpt, table(espPart, usrPart, volume)}, "partition 8 (chalkos volume data)"},
		{"foreign partition", []*rule{gpt, table(espPart, foreign)}, `partition 2 (type 0fc63daf-8483-4772-8e79-3d69d8477de4, label "home")`},
		{"probe fails", []*rule{{prefix: "blkid -p -o export /dev/vdb", err: &node.ToolError{Command: "blkid", Code: 2, Stderr: "error: /dev/vdb: Input/output error"}}}, "Input/output error"},
	} {
		for _, wipe := range []bool{false, true} {
			name := c.name
			if wipe {
				name += ", wiped"
			}
			t.Run(name, func(t *testing.T) {
				r := &fakeRunner{}
				i := newMediaInstaller(t, r)
				if !wipe {
					r.add(c.rules...)
				}
				r.add(mediaRules(i)...)
				opened := false
				path, _ := diskFile(t, i, int64(target.size))
				open := i.OpenDisk
				i.OpenDisk = func(ctx context.Context, dev string) (Disk, error) { opened = true; return open(ctx, dev) }
				req := mediaRequest(t, testImage(t))
				req.WipeDisk = wipe

				err := i.FromMedia(context.Background(), req)
				if wipe || c.refusal == "" {
					if err != nil {
						t.Fatal(err)
					}
					if wipe && hasPrefix(r.calls, "blkid -p -o export") {
						t.Error("probed a disk the user asked to wipe")
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
}

func TestFromMediaRefusesInstallerDisk(t *testing.T) {
	r := &fakeRunner{}
	i := newMediaInstaller(t, r)
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

func TestFromMediaWithUnresolvableBootDisk(t *testing.T) {
	for _, c := range []struct {
		name string
		// link is where the boot-disk link points, or empty when there is no link.
		link string
		// refusal is part of the error, or empty when the install goes ahead.
		refusal string
	}{
		{"no link", "", ""},
		{"dangling link", "../vdz", "find the disk the installer runs from"},
		{"link to a partition", "../vda1", "not a whole disk"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeRunner{}
			i := newMediaInstaller(t, r)
			r.add(mediaRules(i)...)
			path, _ := diskFile(t, i, int64(target.size))
			link := filepath.Join(i.Host.DevRoot, "disk", "chalk-boot-disk")
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(i.Host.DevRoot, "vda1"), "")
			if c.link != "" {
				if err := os.Symlink(c.link, link); err != nil {
					t.Fatal(err)
				}
			}
			req := mediaRequest(t, testImage(t))
			req.WipeDisk = true

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
			if len(r.calls) != 0 {
				t.Errorf("ran %v", r.calls)
			}
			if data, _ := os.ReadFile(path); data[0] != 0xa5 {
				t.Error("changed the target although the installer's disk is unknown")
			}
		})
	}
}

func TestFromMediaRefusesDiskInUse(t *testing.T) {
	r := &fakeRunner{}
	i := newMediaInstaller(t, r)
	r.add(mediaRules(i)...)
	i.OpenDisk = func(context.Context, string) (Disk, error) {
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

func TestFromMediaCanRunAgain(t *testing.T) {
	r := &fakeRunner{}
	i := newMediaInstaller(t, r)
	r.add(&rule{prefix: "systemd-cryptenroll", err: &node.ToolError{Command: "systemd-cryptenroll", Code: 1, Stderr: "Failed to unseal secret using TPM2"}, times: 1})
	r.add(mediaRules(i)...)
	diskFile(t, i, int64(target.size))
	image := testImage(t)

	err := i.FromMedia(context.Background(), mediaRequest(t, image))
	if err == nil || !strings.Contains(err.Error(), "enroll the fallback key on state") {
		t.Fatalf("err = %v, want the enrollment failure", err)
	}
	if installed(t, i.StateDir) {
		t.Fatal("an interrupted install wrote the installed marker")
	}
	if r.holds("/dev/vdb") {
		t.Fatal("the failed install left the target's STATE mounted or open")
	}
	for _, want := range []string{"umount " + i.StateDir, "systemd-cryptsetup detach state"} {
		if !hasPrefix(r.calls, want) {
			t.Errorf("missing %q in\n%s", want, strings.Join(r.calls, "\n"))
		}
	}

	// The first attempt created STATE on the target, which only --wipe-disk replaces.
	r.calls = nil
	req := mediaRequest(t, image)
	req.WipeDisk = true
	if err := i.FromMedia(context.Background(), req); err != nil {
		t.Fatalf("the install did not run again: %v", err)
	}
	if !installed(t, i.StateDir) {
		t.Error("the second install did not finish")
	}
}

func TestFromMediaRecoversTargetLeftOpen(t *testing.T) {
	r := &fakeRunner{}
	i := newMediaInstaller(t, r)
	r.add(mediaRules(i)...)
	diskFile(t, i, int64(target.size))
	// A chalkd that crashed during an install left the target's STATE open and mounted.
	write(t, filepath.Join(i.Host.DevRoot, "mapper", "state"), "/dev/vdb6")
	write(t, i.MountInfo, "30 1 0:27 / / rw - tmpfs tmpfs rw\n41 30 253:1 / "+i.StateDir+" rw,relatime - ext4 /dev/mapper/state rw\n")
	req := mediaRequest(t, testImage(t))
	req.WipeDisk = true

	if err := i.FromMedia(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, r.calls[:2], []string{"umount " + i.StateDir, "systemd-cryptsetup detach state"})
}

func TestOpenExclusiveGivesUpWhenLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk")
	write(t, path, "")
	d, err := openExclusive(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = openExclusive(ctx, path)
	if err == nil || !strings.Contains(err.Error(), "the target disk is locked by another process") {
		t.Fatalf("err = %v, want the lock held by another process", err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("waited %s for the lock, past the context's deadline", waited)
	}
}
