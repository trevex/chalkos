package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	mrand "math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
	"github.com/trevex/chalkos/pkg/upgrade"
	"github.com/trevex/chalkos/pkg/verity"
)

// partsImage is a test role image as an install streams it.
type partsImage struct {
	version                  string
	store, hash, uki, loader []byte
	root                     []byte
	header                   upgrade.Header
}

// newPartsImage makes an image whose store is random data of the seed, with a UKI that boots it
// and systemd-boot standing in as a small PE binary.
func newPartsImage(t *testing.T, version, seed string) partsImage {
	t.Helper()
	s := sha256.Sum256([]byte(seed))
	r := mrand.New(mrand.NewPCG(binary.LittleEndian.Uint64(s[:]), binary.LittleEndian.Uint64(s[8:])))
	const blockSize, blocks = 4096, 300
	store := make([]byte, blocks*blockSize)
	for i := range store {
		store[i] = byte(r.Uint32())
	}
	hash, root, err := verity.Tree(bytes.NewReader(store), verity.Superblock{DataBlockSize: blockSize, HashBlockSize: blockSize, DataBlocks: blocks, Salt: s[:]})
	if err != nil {
		t.Fatal(err)
	}
	img := partsImage{version: version, store: store, hash: hash, root: root}
	img.uki = ukitest.UKI(map[string]string{
		"IMAGE_ID": "chalkos", "IMAGE_VERSION": version, "CHALKOS_CLUSTER": "lab", "CHALKOS_ROLE": "worker", "CHALKOS_BOOT_TRIES": "3",
	}, fmt.Sprintf("init=/nix/store/x/init usrhash=%x", root))
	img.loader = ukitest.Build(map[string][]byte{".text": []byte("systemd-boot " + seed)})
	img.header = img.headerFor()
	return img
}

// headerFor describes the image as it is.
func (img partsImage) headerFor() upgrade.Header {
	return upgrade.Header{
		ImageID: "chalkos", Version: img.version, Cluster: "lab", Role: "worker", RootHash: img.root,
		StoreSize: int64(len(img.store)), VeritySize: int64(len(img.hash)), UKISize: int64(len(img.uki)), BootLoaderSize: int64(len(img.loader)),
		StoreSHA256: sha(img.store), VeritySHA256: sha(img.hash), UKISHA256: sha(img.uki), BootLoaderSHA256: sha(img.loader),
	}
}

func sha(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

func (img partsImage) parts() io.Reader {
	return io.MultiReader(bytes.NewReader(img.store), bytes.NewReader(img.hash), bytes.NewReader(img.uki), bytes.NewReader(img.loader))
}

// labDefinitions are a role's definitions of the system region, as small as repart formats them:
// FAT32 takes 260 MiB, ext4 32 MiB. The build sandbox's file systems take no user extended
// attributes, which repart sets on what it populates an ext4 file system from for
// systemd-validatefs.
var labDefinitions = map[string]string{
	"00-esp.conf":            "[Partition]\nType=esp\nFormat=vfat\nSizeMinBytes=260M\nSizeMaxBytes=260M\n",
	"10-store-verity-a.conf": "[Partition]\nType=usr-x86-64-verity\nSizeMinBytes=256K\nSizeMaxBytes=256K\n",
	"20-store-a.conf":        "[Partition]\nType=usr-x86-64\nSizeMinBytes=2M\nSizeMaxBytes=2M\n",
	"30-store-verity-b.conf": "[Partition]\nType=usr-x86-64-verity\nLabel=_empty\nSizeMinBytes=256K\nSizeMaxBytes=256K\n",
	"40-store-b.conf":        "[Partition]\nType=usr-x86-64\nLabel=_empty\nSizeMinBytes=2M\nSizeMaxBytes=2M\n",
	"50-state.conf":          "[Partition]\nType=" + stateType + "\nLabel=state\nFormat=ext4\nAddValidateFS=no\nEncrypt=tpm2\nSizeMinBytes=32M\nSizeMaxBytes=32M\n",
}

// labTarget is the target disk, a disk image VAR fills the rest of.
var labTarget = testDisk{"vdb", "253:16", 400 * mib, map[string]string{"ID_PATH": "pci-0000:00:05.0", "ID_SERIAL": "chalk-target"}}

// partsLab is an installer booted from vda whose target vdb is a disk image. sfdisk,
// systemd-repart and blkid run on the image; mounts, LUKS, the TPM and the firmware are
// emulated. A mounted file system is a directory kept per partition UUID: it outlives an install
// that stops, as the file system on the disk would, and a disk laid out anew has new ones.
type partsLab struct {
	t        *testing.T
	i        *Installer
	r        *fakeRunner
	disk     string
	fs       string
	repart   string
	efivars  string
	diskLog  *diskLog
	writes   int
	tpmFails bool
}

const labDev = "/dev/vdb"

func newPartsLab(t *testing.T) *partsLab {
	t.Helper()
	for _, tool := range []string{"sfdisk", "blkid", "mkfs.vfat", "mkfs.ext4"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	repart := os.Getenv("CHALKOS_TEST_REPART")
	if repart == "" {
		t.Skip("CHALKOS_TEST_REPART not set")
	}
	r := &fakeRunner{}
	i := newTestInstaller(t, r, vda, labTarget)
	write(t, i.MountInfo, "30 1 0:27 / / rw - tmpfs tmpfs rw\n")
	if err := os.Remove(filepath.Join(i.Host.DevRoot, "mapper", "state")); err != nil {
		t.Fatal(err)
	}
	r.efi = &fakeEFI{entries: []efiEntry{{"0001", "UEFI Misc Device", ""}}, next: 2}
	dir := t.TempDir()
	l := &partsLab{t: t, i: i, r: r, disk: filepath.Join(dir, "vdb"), fs: filepath.Join(dir, "fs"), repart: repart, efivars: filepath.Join(dir, "efivars"), diskLog: &diskLog{}}
	if err := os.WriteFile(l.disk, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(l.disk, int64(labTarget.size)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.efivars, 0o755); err != nil {
		t.Fatal(err)
	}
	i.Run = l
	i.EFIVars = l.efivars
	i.OpenPartition = l.openPartition
	i.OpenDisk = func(_ context.Context, dev string) (Disk, error) {
		if dev != labDev {
			t.Errorf("opened %s, want %s", dev, labDev)
		}
		if r.holds(dev) {
			return nil, &os.PathError{Op: "open", Path: dev, Err: syscall.EBUSY}
		}
		f, err := os.OpenFile(l.disk, os.O_WRONLY, 0)
		return fileDisk{File: f, log: l.diskLog}, err
	}
	return l
}

func (l *partsLab) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return l.RunWithEnv(ctx, nil, name, args...)
}

func (l *partsLab) RunWithInput(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	return l.RunWithEnv(ctx, nil, name, args...)
}

// RunWithEnv runs the disk tools on the disk image and emulates the others.
func (l *partsLab) RunWithEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	switch {
	case name == "sfdisk" || name == "systemd-repart" || name == "blkid":
		l.r.calls = append(l.r.calls, strings.Join(append([]string{name}, args...), " "))
		return l.real(ctx, name, args)
	case name == "mount":
		l.r.calls = append(l.r.calls, strings.Join(append([]string{name}, args...), " "))
		return nil, l.mount(args[len(args)-2], args[len(args)-1])
	case name == "umount":
		l.r.calls = append(l.r.calls, strings.Join(append([]string{name}, args...), " "))
		return nil, l.umount(args[0])
	case name == "systemd-cryptsetup" && args[0] == "attach" && l.tpmFails:
		return nil, &node.ToolError{Command: name, Code: 1, Stderr: "Failed to unseal secret using TPM2: State not recoverable"}
	case name == "efibootmgr":
		// The emulated firmware finds the ESP's partition UUID on the disk.
		l.r.partUUIDs = map[string]string{}
		for _, p := range l.table() {
			l.r.partUUIDs[labDev+" "+strconv.Itoa(p.Number)] = p.UUID
		}
	}
	return l.r.RunWithEnv(ctx, env, name, args...)
}

// real runs a disk tool on the disk image, with blkid probing a partition at its offset.
func (l *partsLab) real(ctx context.Context, name string, args []string) ([]byte, error) {
	var argv []string
	for _, a := range args {
		switch {
		case a == labDev:
			argv = append(argv, l.disk)
		case strings.HasPrefix(a, labDev) && name == "blkid":
			n, _ := strconv.Atoi(strings.TrimPrefix(a, labDev))
			p, ok := l.table()[n]
			if !ok {
				return nil, &node.ToolError{Command: name, Code: 2, Stderr: a + ": no such partition"}
			}
			argv = append(argv, "-O", strconv.FormatInt(p.Start*512, 10), "-S", strconv.FormatInt(p.Size*512, 10), l.disk)
		case strings.HasPrefix(a, "/dev/"):
			l.t.Errorf("%s ran on %s", name, a)
			return nil, errors.New("not the target disk")
		default:
			argv = append(argv, a)
		}
	}
	if name == "systemd-repart" {
		name = l.repart
	}
	out, err := node.ExecRunner{}.RunQuiet(ctx, name, argv...)
	out = bytes.ReplaceAll(out, []byte(l.disk), []byte(labDev))
	var te *node.ToolError
	if errors.As(err, &te) {
		te.Stderr = strings.ReplaceAll(te.Stderr, l.disk, labDev)
	}
	return out, err
}

// table reads the disk image's partition table, by partition number.
func (l *partsLab) table() map[int]upgrade.Partition {
	l.t.Helper()
	out, err := exec.Command("sfdisk", "--json", l.disk).Output()
	if err != nil {
		return nil
	}
	var dump struct {
		Table struct {
			Partitions []upgrade.Partition `json:"partitions"`
		} `json:"partitiontable"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		l.t.Fatal(err)
	}
	parts := map[int]upgrade.Partition{}
	for _, p := range dump.Table.Partitions {
		p.Number, _ = strconv.Atoi(strings.TrimPrefix(p.Node, l.disk))
		p.Node = labDev + strconv.Itoa(p.Number)
		p.UUID, p.Type = strings.ToLower(p.UUID), strings.ToLower(p.Type)
		parts[p.Number] = p
	}
	return parts
}

// mount puts the file system of the partition, or of the LUKS device opened on it, at target.
func (l *partsLab) mount(source, target string) error {
	dev := source
	if name, ok := strings.CutPrefix(source, "/dev/mapper/"); ok {
		backing, err := os.ReadFile(filepath.Join(l.i.Host.DevRoot, "mapper", name))
		if err != nil {
			return &node.ToolError{Command: "mount", Code: 32, Stderr: source + " does not exist"}
		}
		dev = string(backing)
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(dev, labDev))
	p, ok := l.table()[n]
	if !ok {
		return &node.ToolError{Command: "mount", Code: 32, Stderr: dev + " does not exist"}
	}
	dir := filepath.Join(l.fs, p.UUID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(dir, target); err != nil {
		return err
	}
	data, err := os.ReadFile(l.i.MountInfo)
	if err != nil {
		return err
	}
	return os.WriteFile(l.i.MountInfo, fmt.Appendf(data, "50 30 253:9 / %s rw,relatime - fs %s rw\n", target, source), 0o644)
}

func (l *partsLab) umount(target string) error {
	data, err := os.ReadFile(l.i.MountInfo)
	if err != nil {
		return err
	}
	var kept []string
	found := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 4 && fields[4] == target {
			found = true
			continue
		}
		kept = append(kept, line)
	}
	if !found {
		return &node.ToolError{Command: "umount", Code: 32, Stderr: "umount: " + target + ": not mounted."}
	}
	if err := os.Remove(target); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		return err
	}
	return os.WriteFile(l.i.MountInfo, []byte(strings.Join(kept, "")), 0o644)
}

// labPartition is a partition of the disk image.
type labPartition struct {
	f         *os.File
	off, size int64
}

func (p labPartition) ReadAt(b []byte, off int64) (int, error) {
	if off >= p.size {
		return 0, io.EOF
	}
	n, err := p.f.ReadAt(b[:min(int64(len(b)), p.size-off)], p.off+off)
	if err == nil && n < len(b) {
		err = io.EOF
	}
	return n, err
}

func (p labPartition) WriteAt(b []byte, off int64) (int, error) {
	if off+int64(len(b)) > p.size {
		return 0, errors.New("write beyond the partition")
	}
	return p.f.WriteAt(b, p.off+off)
}

func (p labPartition) Sync() error  { return p.f.Sync() }
func (p labPartition) Close() error { return p.f.Close() }

func (l *partsLab) openPartition(p upgrade.Partition, write bool) (upgrade.PartitionFile, error) {
	if !strings.HasPrefix(p.Node, labDev) {
		l.t.Errorf("opened %s, a partition of another disk", p.Node)
		return nil, syscall.EBUSY
	}
	flag := os.O_RDONLY
	if write {
		l.writes++
		flag = os.O_RDWR
	}
	f, err := os.OpenFile(l.disk, flag, 0)
	if err != nil {
		return nil, err
	}
	return labPartition{f: f, off: p.Start * 512, size: p.Size * 512}, nil
}

// request installs img onto the lab's target, with STATE and VAR unencrypted: the image tools
// lay out encrypted partitions only with a TPM.
func (l *partsLab) request(img partsImage) PartsRequest {
	l.t.Helper()
	section := testSection(storage.EncryptionNone, storage.FallbackNone, labDev)
	section.Disks[storage.SystemDisk].Repart["50-var.conf"] += "AddValidateFS=no\n"
	return PartsRequest{
		Request:           testRequest(l.t, section),
		Target:            storage.Ref{Selector: storage.Selector{Serial: "chalk-target"}},
		Image:             img.header,
		Parts:             img.parts(),
		SystemDefinitions: labDefinitions,
	}
}

func (l *partsLab) install(img partsImage) error {
	return l.i.FromParts(context.Background(), l.request(img))
}

// esp returns the directory standing for the target's ESP, which is empty while none exists.
func (l *partsLab) esp() string {
	for _, p := range l.table() {
		if p.Type == typeESP {
			return filepath.Join(l.fs, p.UUID)
		}
	}
	return filepath.Join(l.fs, "none")
}

func (l *partsLab) file(path string) []byte {
	b, err := os.ReadFile(filepath.Join(l.esp(), path))
	if err != nil {
		return nil
	}
	return b
}

// bootable reports whether firmware could boot the target: its ESP holds a boot loader.
func (l *partsLab) bootable() bool {
	return l.file("EFI/BOOT/BOOTX64.EFI") != nil
}

func (l *partsLab) installed() bool {
	for _, p := range l.table() {
		if p.Name == stateLabel {
			_, err := os.Stat(filepath.Join(l.fs, p.UUID, installedMarker))
			return err == nil
		}
	}
	return false
}

// checkInstalled checks that the target holds the image as installed: the system region laid
// out, slot A holding the image's store under its UUIDs and labels, slot B empty, the UKI and
// the boot loader on the ESP, STATE installed, and firmware booting the target next.
func (l *partsLab) checkInstalled(img partsImage) {
	l.t.Helper()
	parts := l.table()
	data, verityUUID := upgrade.PartitionUUIDs(img.root)
	want := []struct{ typ, label, uuid string }{
		{typeESP, "", ""},
		{typeUsrX86Verity, "store-verity_" + img.version, verityUUID},
		{typeUsrX86, "store_" + img.version, data},
		{typeUsrX86Verity, "_empty", ""},
		{typeUsrX86, "_empty", ""},
		{stateType, "state", ""},
		{varType, "var", ""},
	}
	if len(parts) != len(want) {
		l.t.Fatalf("the target has %d partitions, want %d", len(parts), len(want))
	}
	for n, w := range want {
		p := parts[n+1]
		if p.Type != w.typ || w.label != "" && p.Name != w.label || w.uuid != "" && p.UUID != w.uuid {
			l.t.Errorf("partition %d is %s %q %s, want %s %q %s", n+1, p.Type, p.Name, p.UUID, w.typ, w.label, w.uuid)
		}
	}
	w := &upgrade.SlotWriter{OpenPartition: l.openPartition}
	if !w.Holds(upgrade.Slot{Verity: parts[2], Data: parts[3]}, img.header) {
		l.t.Error("slot A does not hold the image's store")
	}
	if !bytes.Equal(l.file("EFI/Linux/chalkos_"+img.version+".efi"), img.uki) {
		l.t.Error("the ESP does not hold the image's UKI")
	}
	if !bytes.Equal(l.file("EFI/BOOT/BOOTX64.EFI"), img.loader) {
		l.t.Error("the ESP does not hold the image's boot loader")
	}
	for _, dir := range []string{"EFI/Linux", "EFI/BOOT"} {
		files, _ := os.ReadDir(filepath.Join(l.esp(), dir))
		if len(files) != 1 {
			l.t.Errorf("%s holds %d files, want one", dir, len(files))
		}
	}
	if !l.installed() {
		l.t.Error("STATE holds no installed marker")
	}
	var entry string
	for _, e := range l.r.efi.entries {
		if e.label == bootLabel && e.partUUID == parts[1].UUID {
			if entry != "" {
				l.t.Errorf("two boot entries for the target's ESP: %s and %s", entry, e.num)
			}
			entry = e.num
		}
	}
	if entry == "" || l.r.efi.bootNext != entry {
		l.t.Errorf("firmware boots %q next, want the target's entry %q", l.r.efi.bootNext, entry)
	}
}

func TestFromPartsInstallsOntoEmptyDisk(t *testing.T) {
	l := newPartsLab(t)
	img := newPartsImage(t, "0.1.0", "a")
	if err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.checkInstalled(img)
	if !hasPrefix(l.r.calls, "systemd-repart --dry-run=no --empty=allow --seed=random --definitions="+filepath.Join(l.i.WorkDir, "system")+" --tpm2-pcrs=7 /dev/vdb") {
		t.Errorf("the target was not laid out with the role's definitions: %q", l.r.calls)
	}
	if l.writes != 2 {
		t.Errorf("slot A's partitions were opened for writing %d times, want twice", l.writes)
	}
	if mounted, _ := isMounted(l.i.MountInfo, filepath.Join(l.i.WorkDir, "esp")); mounted {
		t.Error("the ESP is still mounted")
	}
}

var errStop = errors.New("stop")

// TestFromPartsInterrupted stops an install before each change it makes: the target never boots
// until the boot loader is on it and is never installed half, and the install run again
// completes, without writing slot A again once it held the image.
func TestFromPartsInterrupted(t *testing.T) {
	img := newPartsImage(t, "0.1.0", "a")
	var changes []string
	l := newPartsLab(t)
	l.i.Change = func(what string) error {
		changes = append(changes, what)
		return nil
	}
	if err := l.install(img); err != nil {
		t.Fatal(err)
	}
	t.Logf("changes: %q", changes)
	loaderAt, activatedAt := -1, -1
	for n, c := range changes {
		switch {
		case c == "name the boot loader":
			loaderAt = n
		case strings.HasPrefix(c, "label partition 3"):
			activatedAt = n
		}
	}
	if loaderAt < 0 || activatedAt < 0 {
		t.Fatal("the install names no boot loader or activates no slot")
	}
	for n, change := range changes {
		t.Run(change, func(t *testing.T) {
			l := newPartsLab(t)
			calls := 0
			l.i.Change = func(string) error {
				calls++
				if calls == n+1 {
					return errStop
				}
				return nil
			}
			if err := l.install(img); !errors.Is(err, errStop) {
				t.Fatalf("install = %v, want the stop", err)
			}
			if l.bootable() != (n > loaderAt) {
				t.Errorf("the target boots: %v, after a stop at %q", l.bootable(), change)
			}
			if l.installed() {
				t.Error("the target is installed")
			}
			if l.r.holds(labDev) {
				t.Error("the stopped install left a partition of the target mounted or open")
			}
			l.i.Change = nil
			l.writes = 0
			if err := l.install(img); err != nil {
				t.Fatal(err)
			}
			if n > activatedAt && l.writes != 0 {
				t.Errorf("slot A was written again after a stop at %q", change)
			}
			l.checkInstalled(img)
		})
	}
}

// TestFromPartsInterruptedTransfer cuts the stream within each part of the image.
func TestFromPartsInterruptedTransfer(t *testing.T) {
	img := newPartsImage(t, "0.1.0", "a")
	store, hash, uki, loader := int64(len(img.store)), int64(len(img.hash)), int64(len(img.uki)), int64(len(img.loader))
	for _, cut := range []int64{0, store / 2, store + hash/2, store + hash + uki/2, store + hash + uki + loader/2} {
		l := newPartsLab(t)
		req := l.request(img)
		req.Parts = io.LimitReader(img.parts(), cut)
		if err := l.i.FromParts(context.Background(), req); err == nil || !strings.Contains(err.Error(), "receive") {
			t.Errorf("a stream cut after %d bytes: %v", cut, err)
		}
		if l.bootable() || l.installed() {
			t.Errorf("a stream cut after %d bytes left a bootable or installed target", cut)
		}
		if err := l.install(img); err != nil {
			t.Fatal(err)
		}
		l.checkInstalled(img)
	}
}

// foreignTable gives the target a GPT of another system.
func (l *partsLab) foreignTable() {
	l.t.Helper()
	sfdisk := exec.Command("sfdisk", "--quiet", l.disk)
	sfdisk.Stdin = strings.NewReader("label: gpt\nsize=16MiB, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=data\n")
	if out, err := sfdisk.CombinedOutput(); err != nil {
		l.t.Fatalf("sfdisk: %v: %s", err, out)
	}
}

// testSigner makes a db signer: its key and certificate files and the certificate's DER.
func testSigner(t *testing.T, dir, name string) (key, cert string, der []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	key, cert = filepath.Join(dir, name+".key"), filepath.Join(dir, name+".crt")
	write(t, key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})))
	write(t, cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	return key, cert, der
}

func sbsign(t *testing.T, binary []byte, key, cert string) []byte {
	t.Helper()
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.efi"), filepath.Join(dir, "out.efi")
	if err := os.WriteFile(in, binary, 0o644); err != nil {
		t.Fatal(err)
	}
	if o, err := exec.Command("sbsign", "--key", key, "--cert", cert, "--output", out, in).CombinedOutput(); err != nil {
		t.Fatalf("sbsign: %v: %s", err, o)
	}
	signed, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// enforceSecureBoot turns Secure Boot on with db and dbx holding the certificates.
func (l *partsLab) enforceSecureBoot(db, dbx []byte) {
	l.t.Helper()
	const global, security = "8be4df61-93ca-11d2-aa0d-00e098032b8c", "d719b2cb-3d3a-4596-a3bc-dad00e67656f"
	for _, v := range []struct {
		name, vendor string
		value        []byte
	}{
		{"SecureBoot", global, []byte{1}},
		{"SetupMode", global, []byte{0}},
		{"db", security, ukitest.SignatureList(db)},
		{"dbx", security, nil},
	} {
		if v.name == "dbx" && dbx != nil {
			v.value = ukitest.SignatureList(dbx)
		}
		write(l.t, filepath.Join(l.efivars, v.name+"-"+v.vendor), string(append([]byte{7, 0, 0, 0}, v.value...)))
	}
}

// TestFromPartsRefuses checks what an install refuses, leaving a target that does not boot and
// is not installed.
func TestFromPartsRefuses(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	img := newPartsImage(t, "0.1.0", "a")
	keys := t.TempDir()
	key, cert, der := testSigner(t, keys, "db")
	signed := img
	signed.uki, signed.loader = sbsign(t, img.uki, key, cert), sbsign(t, img.loader, key, cert)
	signed.header = signed.headerFor()
	unsignedLoader := signed
	unsignedLoader.loader = img.loader
	unsignedLoader.header = unsignedLoader.headerFor()
	otherStore := img
	otherStore.uki = ukitest.UKI(map[string]string{
		"IMAGE_ID": "chalkos", "IMAGE_VERSION": "0.1.0", "CHALKOS_CLUSTER": "lab", "CHALKOS_ROLE": "worker", "CHALKOS_BOOT_TRIES": "3",
	}, fmt.Sprintf("usrhash=%x", sha([]byte("another store"))))
	otherStore.header = otherStore.headerFor()
	for _, tc := range []struct {
		name  string
		setup func(l *partsLab)
		img   partsImage
		edit  func(r *PartsRequest)
		want  string
	}{
		{"a foreign partition table", func(l *partsLab) { l.foreignTable() }, img, nil, "is not one an install of this role left"},
		{"a file system on the disk", func(l *partsLab) {
			if out, err := exec.Command("mkfs.ext4", "-q", l.disk).CombinedOutput(); err != nil {
				t.Fatalf("mkfs.ext4: %v: %s", err, out)
			}
		}, img, nil, "carries data (ext4)"},
		{"an installed disk", func(l *partsLab) {
			if err := l.install(img); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(l.esp(), "EFI/BOOT/BOOTX64.EFI"))
			l.r.efi.bootNext = ""
		}, img, nil, "holds an installed node"},
		{"STATE this machine's TPM does not open", func(l *partsLab) {
			l.stopAt(img, "write the UKI")
			l.luksState()
			l.tpmFails = true
		}, img, nil, "does not open on this machine"},
		{"slot A holding another store", func(l *partsLab) { l.stopAt(img, "write the UKI") }, newPartsImage(t, "0.2.0", "b"), nil, "slot A of the target disk"},
		{"another cluster", nil, img, func(r *PartsRequest) { r.Image.Cluster = "prod" }, "the UKI's cluster is \"lab\""},
		{"another role", nil, img, func(r *PartsRequest) { r.Image.Role = "controlplane" }, "the UKI's role is \"worker\""},
		{"a corrupt store", nil, img, func(r *PartsRequest) {
			corrupt := bytes.Clone(img.store)
			corrupt[100]++
			r.Parts = io.MultiReader(bytes.NewReader(corrupt), bytes.NewReader(img.hash), bytes.NewReader(img.uki), bytes.NewReader(img.loader))
		}, "the store's SHA-256"},
		{"another root hash", nil, img, func(r *PartsRequest) { r.Image.RootHash = sha([]byte("other")) }, "the written store"},
		{"a UKI booting another store", nil, otherStore, nil, "the UKI boots another store"},
		{"a corrupt boot loader", nil, img, func(r *PartsRequest) { r.Image.BootLoaderSHA256 = sha(nil) }, "the boot loader's SHA-256"},
		{"a boot loader for another machine", func(l *partsLab) { l.i.Loader = `\EFI\BOOT\BOOTAA64.EFI` }, img, nil, "built for the machine type 0x8664"},
		{"an unsigned UKI", func(l *partsLab) { l.enforceSecureBoot(der, nil) }, img, nil, "Secure Boot would refuse the UKI"},
		{"an unsigned boot loader", func(l *partsLab) { l.enforceSecureBoot(der, nil) }, unsignedLoader, nil, "Secure Boot would refuse the boot loader"},
		{"a signer dbx lists", func(l *partsLab) { l.enforceSecureBoot(der, der) }, signed, nil, "Secure Boot would refuse the UKI"},
		{"a header without a boot loader", nil, img, func(r *PartsRequest) { r.Image.BootLoaderSize, r.Image.BootLoaderSHA256 = 0, nil }, "brings no boot loader"},
		{"more than the header names", nil, img, func(r *PartsRequest) { r.Parts = io.MultiReader(img.parts(), strings.NewReader("x")) }, "longer than its header says"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newPartsLab(t)
			if tc.setup != nil {
				tc.setup(l)
			}
			before := l.table()
			req := l.request(tc.img)
			if tc.edit != nil {
				tc.edit(&req)
			}
			err := l.i.FromParts(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install = %v, want %q", err, tc.want)
			}
			if l.bootable() {
				t.Error("the target boots")
			}
			if strings.Contains(tc.want, "--wipe-disk") || strings.Contains(err.Error(), "--wipe-disk") {
				if after := l.table(); fmt.Sprint(after) != fmt.Sprint(before) {
					t.Errorf("the refused target's partitions changed:\n%v\n%v", before, after)
				}
			}
			if tc.name != "an installed disk" && l.installed() {
				t.Error("the target is installed")
			}
			if l.r.efi.bootNext != "" {
				t.Errorf("firmware boots %s next", l.r.efi.bootNext)
			}
		})
	}
}

// stopAt runs an install of img that stops before the change.
func (l *partsLab) stopAt(img partsImage, change string) {
	l.t.Helper()
	l.i.Change = func(what string) error {
		if what == change {
			return errStop
		}
		return nil
	}
	if err := l.install(img); !errors.Is(err, errStop) {
		l.t.Fatalf("install = %v, want the stop at %q", err, change)
	}
	l.i.Change = nil
}

// luksState puts a LUKS header on STATE, as a node whose STATE is encrypted has.
func (l *partsLab) luksState() {
	l.t.Helper()
	if _, err := exec.LookPath("cryptsetup"); err != nil {
		l.t.Skip("cryptsetup not in PATH")
	}
	header := filepath.Join(l.t.TempDir(), "luks")
	if err := os.WriteFile(header, nil, 0o644); err != nil {
		l.t.Fatal(err)
	}
	if err := os.Truncate(header, 16*mib); err != nil {
		l.t.Fatal(err)
	}
	luks := exec.Command("cryptsetup", "luksFormat", "--batch-mode", "--type", "luks2", "--pbkdf", "pbkdf2", "--pbkdf-force-iterations", "1000", "--key-file", "-", header)
	luks.Stdin = strings.NewReader("key")
	if out, err := luks.CombinedOutput(); err != nil {
		l.t.Fatalf("cryptsetup: %v: %s", err, out)
	}
	data, err := os.ReadFile(header)
	if err != nil {
		l.t.Fatal(err)
	}
	for _, p := range l.table() {
		if p.Name == stateLabel {
			f, err := os.OpenFile(l.disk, os.O_WRONLY, 0)
			if err != nil {
				l.t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteAt(data, p.Start*512); err != nil {
				l.t.Fatal(err)
			}
			return
		}
	}
	l.t.Fatal("no STATE")
}

// TestFromPartsWipesAForeignDisk lays out a disk of another system with --wipe-disk.
func TestFromPartsWipesAForeignDisk(t *testing.T) {
	img := newPartsImage(t, "0.1.0", "a")
	for _, setup := range []func(l *partsLab){
		func(l *partsLab) { l.foreignTable() },
		func(l *partsLab) {
			if err := l.install(newPartsImage(t, "0.2.0", "b")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		l := newPartsLab(t)
		setup(l)
		l.diskLog.reread = 0
		req := l.request(img)
		req.WipeDisk = true
		if err := l.i.FromParts(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		l.checkInstalled(img)
		if l.diskLog.reread != 1 {
			t.Errorf("the partition table was reread %d times after the wipe, want once", l.diskLog.reread)
		}
	}
}

// TestFromPartsUnderSecureBoot installs an image whose UKI and boot loader a db certificate
// signed.
func TestFromPartsUnderSecureBoot(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	img := newPartsImage(t, "0.1.0", "a")
	key, cert, der := testSigner(t, t.TempDir(), "db")
	img.uki, img.loader = sbsign(t, img.uki, key, cert), sbsign(t, img.loader, key, cert)
	img.header = img.headerFor()
	l := newPartsLab(t)
	l.enforceSecureBoot(der, nil)
	if err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.checkInstalled(img)
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

const (
	// staleUUID is the ESP of an earlier attempt, which is on no disk any more.
	staleUUID = "5ca1ab1e-0000-4000-8000-000000000000"
	// otherUUID is the ESP of a chalkos node on another disk.
	otherUUID = "0717e400-0000-4000-8000-000000000000"
)

// TestFromPartsBootsTheNewEntry makes the target's entry the next boot and deletes the entries of
// earlier attempts whose ESP is on no disk, keeping those of other disks.
func TestFromPartsBootsTheNewEntry(t *testing.T) {
	l := newPartsLab(t)
	links := filepath.Join(l.i.Host.DevRoot, "disk", "by-partuuid")
	if err := os.MkdirAll(links, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../vda", filepath.Join(links, otherUUID)); err != nil {
		t.Fatal(err)
	}
	l.r.efi = &fakeEFI{
		entries: []efiEntry{{"0001", "UEFI Misc Device", ""}, {"0003", "chalkos", staleUUID}, {"0005", "chalkos", otherUUID}},
		next:    6,
	}
	img := newPartsImage(t, "0.1.0", "a")
	if err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.checkInstalled(img)
	want := []efiEntry{{"0001", "UEFI Misc Device", ""}, {"0005", "chalkos", otherUUID}, {"0006", "chalkos", l.table()[1].UUID}}
	if !reflect.DeepEqual(l.r.efi.entries, want) || l.r.efi.bootNext != "0006" {
		t.Errorf("boot entries = %+v, next %s; want %+v, next 0006", l.r.efi.entries, l.r.efi.bootNext, want)
	}
	if !reflect.DeepEqual(l.r.efi.deleted, []string{"0003"}) {
		t.Errorf("deleted boot entries %v, want only the stale 0003", l.r.efi.deleted)
	}
}

// TestFromPartsRefusesBeforeTouchingTheDisk refuses requests that cannot install before any tool
// runs.
func TestFromPartsRefusesBeforeTouchingTheDisk(t *testing.T) {
	img := newPartsImage(t, "0.1.0", "a")
	for _, tc := range []struct {
		name  string
		setup func(l *partsLab, r *PartsRequest)
		want  string
	}{
		{"the installer's disk", func(_ *partsLab, r *PartsRequest) { r.Target = storage.Ref{Path: "/dev/vda"} }, "the installer runs from"},
		{"a dangling boot disk link", func(l *partsLab, _ *PartsRequest) { l.linkBootDisk("../vdz") }, "find the disk the installer runs from"},
		{"a boot disk link to a partition", func(l *partsLab, _ *PartsRequest) { l.linkBootDisk("../vda1") }, "not a whole disk"},
		{"an invalid storage section", func(_ *partsLab, r *PartsRequest) {
			r.Section.Volumes["data"] = storage.Volume{Disk: storage.SystemDisk, Label: "state", Format: "ext4"}
		}, `label "state"`},
		{"no boot loader path", func(l *partsLab, _ *PartsRequest) { l.i.Loader = "" }, "no UEFI boot loader path"},
		{"definitions without STATE", func(_ *partsLab, r *PartsRequest) {
			r.SystemDefinitions = maps.Clone(labDefinitions)
			delete(r.SystemDefinitions, "50-state.conf")
		}, "an ESP, two store slots and STATE"},
		{"definitions that do not format the ESP", func(_ *partsLab, r *PartsRequest) {
			r.SystemDefinitions = maps.Clone(labDefinitions)
			r.SystemDefinitions["00-esp.conf"] = "[Partition]\nType=esp\n"
		}, "does not format the ESP"},
		{"an invalid version", func(_ *partsLab, r *PartsRequest) { r.Image.Version = "0.1.0+1" }, "the image: the version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newPartsLab(t)
			req := l.request(img)
			req.WipeDisk = true
			tc.setup(l, &req)
			err := l.i.FromParts(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(l.r.calls) != 0 || len(l.diskLog.writes) != 0 {
				t.Errorf("ran %v and wrote %v", l.r.calls, l.diskLog.writes)
			}
		})
	}
}

// linkBootDisk points the boot disk's link elsewhere.
func (l *partsLab) linkBootDisk(target string) {
	l.t.Helper()
	link := filepath.Join(l.i.Host.DevRoot, "disk", "chalk-boot-disk")
	if err := os.Remove(link); err != nil {
		l.t.Fatal(err)
	}
	write(l.t, filepath.Join(l.i.Host.DevRoot, "vda1"), "")
	if err := os.Symlink(target, link); err != nil {
		l.t.Fatal(err)
	}
}

// TestFromPartsWithoutABootDiskLink installs when the installer's disk is unknown because udev
// made no link to it.
func TestFromPartsWithoutABootDiskLink(t *testing.T) {
	l := newPartsLab(t)
	if err := os.Remove(filepath.Join(l.i.Host.DevRoot, "disk", "chalk-boot-disk")); err != nil {
		t.Fatal(err)
	}
	img := newPartsImage(t, "0.1.0", "a")
	if err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.checkInstalled(img)
}

func TestFromPartsRefusesDiskInUse(t *testing.T) {
	l := newPartsLab(t)
	l.i.OpenDisk = func(context.Context, string) (Disk, error) {
		return nil, &os.PathError{Op: "open", Path: labDev, Err: syscall.EBUSY}
	}
	if err := l.install(newPartsImage(t, "0.1.0", "a")); err == nil || !strings.Contains(err.Error(), "is in use") {
		t.Fatalf("err = %v", err)
	}
}

// TestFromPartsReleasesTheTarget frees a target whose STATE a crashed chalkd left open before it
// installs, and frees it again when the install fails.
func TestFromPartsReleasesTheTarget(t *testing.T) {
	img := newPartsImage(t, "0.1.0", "a")
	l := newPartsLab(t)
	l.stopAt(img, "write the identity")
	if l.r.holds(labDev) {
		t.Fatal("the failed install left the target's STATE mounted or open")
	}
	for _, p := range l.table() {
		if p.Name == stateLabel {
			write(t, filepath.Join(l.i.Host.DevRoot, "mapper", "state"), p.Node)
			if err := l.mount("/dev/mapper/state", l.i.StateDir); err != nil {
				t.Fatal(err)
			}
		}
	}
	l.r.calls = nil
	if err := l.install(img); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, l.r.calls[:2], []string{"umount " + l.i.StateDir, "systemd-cryptsetup detach state"})
	l.checkInstalled(img)
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
