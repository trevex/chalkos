package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
	"github.com/trevex/chalkos/pkg/verity"
)

// image is a test image: a small store, its hash tree and a UKI booting it.
type image struct {
	version     string
	store, hash []byte
	root        []byte
	uki         []byte
	header      Header
}

// storeBlocks makes trees of two levels.
const storeBlocks = 300

func newImage(t *testing.T, version string, tries int) image {
	t.Helper()
	return newImageOfBlocks(t, version, tries, 512, storeBlocks)
}

// newImageOfBlocks makes an image whose store has the number of dm-verity blocks of the size.
func newImageOfBlocks(t *testing.T, version string, tries int, blockSize uint32, blocks uint64) image {
	t.Helper()
	seed := sha256.Sum256([]byte(version))
	r := rand.New(rand.NewPCG(binary.LittleEndian.Uint64(seed[:]), binary.LittleEndian.Uint64(seed[8:])))
	store := make([]byte, blocks*uint64(blockSize))
	for i := range store {
		store[i] = byte(r.Uint32())
	}
	salt := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(r.Uint32())
	}
	hash, root, err := verity.Tree(bytes.NewReader(store), verity.Superblock{DataBlockSize: blockSize, HashBlockSize: blockSize, DataBlocks: blocks, Salt: salt})
	if err != nil {
		t.Fatal(err)
	}
	img := image{version: version, store: store, hash: hash, root: root}
	img.uki = newUKI(img.osRelease(tries), root)
	img.header = img.headerFor(img.uki)
	return img
}

// newUKI builds a UKI of the os-release booting the store of the root hash.
func newUKI(osRelease map[string]string, root []byte) []byte {
	return ukitest.UKI(osRelease, fmt.Sprintf("init=/nix/store/x/init usrhash=%x", root))
}

func (img image) osRelease(tries int) map[string]string {
	return map[string]string{
		"IMAGE_ID":           "chalkos",
		"IMAGE_VERSION":      img.version,
		"CHALKOS_CLUSTER":    "lab",
		"CHALKOS_ROLE":       "worker",
		"CHALKOS_BOOT_TRIES": strconv.Itoa(tries),
	}
}

func sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// headerFor describes the image with the UKI given.
func (img image) headerFor(uki []byte) Header {
	return Header{
		ImageID: "chalkos", Version: img.version, Cluster: "lab", Role: "worker", RootHash: img.root,
		StoreSize: int64(len(img.store)), VeritySize: int64(len(img.hash)), UKISize: int64(len(uki)),
		StoreSHA256: sum(img.store), VeritySHA256: sum(img.hash), UKISHA256: sum(uki),
	}
}

func (img image) stream() io.Reader {
	return io.MultiReader(bytes.NewReader(img.store), bytes.NewReader(img.hash), bytes.NewReader(img.uki))
}

// GPT partition types of the lab's disk.
const (
	typeESP    = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"
	typeData   = "8484680C-9521-48C6-9C11-B0720656F69E"
	typeVerity = "77FF5F63-E7B6-4633-ACF4-1565B864C0E6"
	typeState  = "480B7842-1236-1ABB-0339-E5503B43122A"
)

// lab is a node booted from slot A of a disk image, with its ESP and EFI variables in
// directories.
type lab struct {
	t                     *testing.T
	disk, esp, efivars    string
	cmdline               string
	node                  *Node
	mu                    sync.Mutex
	writes                map[int]int
	running               image
	runningData, runningV int
}

func newLab(t *testing.T, running image) *lab {
	t.Helper()
	if _, err := exec.LookPath("sfdisk"); err != nil {
		t.Skip("sfdisk not in PATH")
	}
	dir := t.TempDir()
	l := &lab{t: t, disk: filepath.Join(dir, "disk.img"), esp: filepath.Join(dir, "esp"), efivars: filepath.Join(dir, "efivars"), cmdline: filepath.Join(dir, "cmdline"), writes: map[int]int{}}
	if err := os.WriteFile(l.disk, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(l.disk, 16<<20); err != nil {
		t.Fatal(err)
	}
	data, verityUUID := PartitionUUIDs(running.root)
	script := fmt.Sprintf(`label: gpt
size=2MiB, type=%s, name=esp
size=256KiB, type=%s, name=store-verity_%s, uuid=%s
size=1MiB, type=%s, name=store_%s, uuid=%s
size=256KiB, type=%s, name=_empty
size=1MiB, type=%s, name=_empty
size=1MiB, type=%s, name=state
`, typeESP, typeVerity, running.version, verityUUID, typeData, running.version, data, typeVerity, typeData, typeState)
	sfdisk := exec.Command("sfdisk", "--quiet", l.disk)
	sfdisk.Stdin = strings.NewReader(script)
	if out, err := sfdisk.CombinedOutput(); err != nil {
		t.Fatalf("sfdisk: %v: %s", err, out)
	}
	l.node = &Node{
		Run:           node.ExecRunner{},
		Disk:          l.disk,
		ESP:           l.esp,
		EFIVars:       l.efivars,
		Cmdline:       l.cmdline,
		OpenPartition: l.open,
	}
	for _, d := range []string{filepath.Join(l.esp, linuxDir), l.efivars} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	parts := l.table()
	l.writePartition(parts[2], running.hash)
	l.writePartition(parts[3], running.store)
	if err := os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_"+running.version+".efi"), running.uki, 0o644); err != nil {
		t.Fatal(err)
	}
	l.runFrom(running, 2, 3)
	return l
}

// runFrom makes the node run the image from the slot of the partitions: the kernel's command
// line, the running image's os-release and the entry systemd-boot booted.
func (l *lab) runFrom(img image, verityPart, dataPart int) {
	l.t.Helper()
	l.running, l.runningV, l.runningData = img, verityPart, dataPart
	if err := os.WriteFile(l.cmdline, fmt.Appendf(nil, "init=/x usrhash=%x\n", img.root), 0o644); err != nil {
		l.t.Fatal(err)
	}
	l.node.OSRelease = img.osRelease(3)
	l.setVariable("LoaderEntrySelected", "chalkos_"+img.version+".efi")
}

func (l *lab) setVariable(name, value string) {
	l.t.Helper()
	if err := setLoaderString(l.efivars, name, value, nil); err != nil {
		l.t.Fatal(err)
	}
}

// setGlobal sets a variable of the global vendor, such as SecureBoot.
func (l *lab) setGlobal(name, vendor string, value []byte) {
	l.t.Helper()
	if err := os.WriteFile(filepath.Join(l.efivars, name+"-"+vendor), append([]byte{7, 0, 0, 0}, value...), 0o644); err != nil {
		l.t.Fatal(err)
	}
}

func (l *lab) table() map[int]Partition {
	l.t.Helper()
	parts, err := l.node.readTable(context.Background())
	if err != nil {
		l.t.Fatal(err)
	}
	m := map[int]Partition{}
	for _, p := range parts {
		m[p.Number] = p
	}
	return m
}

func (l *lab) writePartition(p Partition, data []byte) {
	l.t.Helper()
	f, err := os.OpenFile(l.disk, os.O_WRONLY, 0)
	if err != nil {
		l.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(data, p.Start*512); err != nil {
		l.t.Fatal(err)
	}
}

// partitionFile is a partition of the lab's disk image.
type partitionFile struct {
	f         *os.File
	off, size int64
}

func (p partitionFile) ReadAt(b []byte, off int64) (int, error) {
	if off >= p.size {
		return 0, io.EOF
	}
	n, err := p.f.ReadAt(b[:min(int64(len(b)), p.size-off)], p.off+off)
	if err == nil && n < len(b) {
		err = io.EOF
	}
	return n, err
}

func (p partitionFile) WriteAt(b []byte, off int64) (int, error) {
	if off+int64(len(b)) > p.size {
		return 0, errors.New("write beyond the partition")
	}
	return p.f.WriteAt(b, p.off+off)
}

func (p partitionFile) Sync() error  { return p.f.Sync() }
func (p partitionFile) Close() error { return p.f.Close() }

// open opens a partition of the disk image, refusing writes to the running slot's partitions as
// the kernel would, and counts writes.
func (l *lab) open(p Partition, write bool) (PartitionFile, error) {
	if write {
		l.mu.Lock()
		l.writes[p.Number]++
		l.mu.Unlock()
		if p.Number == l.runningData || p.Number == l.runningV {
			l.t.Errorf("partition %d of the running slot was opened for writing", p.Number)
			return nil, syscall.EBUSY
		}
	}
	flag := os.O_RDONLY
	if write {
		flag = os.O_RDWR
	}
	f, err := os.OpenFile(l.disk, flag, 0)
	if err != nil {
		return nil, err
	}
	return partitionFile{f: f, off: p.Start * 512, size: p.Bytes()}, nil
}

// boot is what systemd-boot would boot: the entry it selects, and the version of the image whose
// store that entry finds by its partition UUIDs and checks against its root hash. An empty
// version means the boot finds no store.
func (l *lab) boot() (Entry, string) {
	l.t.Helper()
	entries, err := Entries(l.esp)
	if err != nil {
		l.t.Fatal(err)
	}
	if len(entries) == 0 {
		l.t.Fatal("no UKI on the ESP")
	}
	// Bad entries last, then by the image ID their os-release names, then the newest version first,
	// then by ID.
	slices.SortStableFunc(entries, func(a, b Entry) int {
		if a.Bad() != b.Bad() {
			if a.Bad() {
				return 1
			}
			return -1
		}
		if c := strings.Compare(a.ImageID, b.ImageID); c != 0 {
			return c
		}
		if c := compareVersions(b.Version, a.Version); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	selected := entries[0]
	preferred, err := loaderString(l.efivars, "LoaderEntryPreferred")
	if err != nil {
		l.t.Fatal(err)
	}
	for _, e := range entries {
		if e.ID == preferred && !e.Bad() {
			selected = e
			break
		}
	}
	data, verityUUID := PartitionUUIDs(selected.RootHash)
	var store, hash *Partition
	for _, p := range l.table() {
		switch p.UUID {
		case data:
			store = &p
		case verityUUID:
			hash = &p
		}
	}
	if store == nil || hash == nil {
		return selected, ""
	}
	d, _ := l.open(*store, false)
	h, _ := l.open(*hash, false)
	defer d.Close()
	defer h.Close()
	if _, err := verity.Verify(d, h, selected.RootHash); err != nil {
		l.t.Errorf("%s boots a store that does not verify: %v", selected.File, err)
		return selected, ""
	}
	return selected, selected.Version
}

// compareVersions compares versions of dot-separated numbers.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(as), len(bs)) {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x - y
		}
	}
	return len(as) - len(bs)
}

// attempt boots as systemd-boot does: it counts a try of the entry it selects, and returns it.
func (l *lab) attempt() Entry {
	l.t.Helper()
	e, version := l.boot()
	if version == "" {
		l.t.Fatalf("%s finds no store", e.File)
	}
	if e.TriesLeft >= 0 {
		next := fmt.Sprintf("%s+%d-%d.efi", strings.TrimSuffix(e.ID, ".efi"), max(e.TriesLeft-1, 0), e.TriesDone+1)
		if err := os.Rename(filepath.Join(l.esp, linuxDir, e.File), filepath.Join(l.esp, linuxDir, next)); err != nil {
			l.t.Fatal(err)
		}
		e.File, e.TriesLeft, e.TriesDone = next, max(e.TriesLeft-1, 0), e.TriesDone+1
	}
	return e
}

// bless marks the entry good, as systemd-bless-boot does.
func (l *lab) bless(e Entry) {
	l.t.Helper()
	if err := os.Rename(filepath.Join(l.esp, linuxDir, e.File), filepath.Join(l.esp, linuxDir, e.ID)); err != nil {
		l.t.Fatal(err)
	}
}

// bootsRunningOr checks that the disk boots the running image, or img with all its tries.
func (l *lab) bootsRunningOr(img image, tries int) {
	l.t.Helper()
	e, version := l.boot()
	switch {
	case version == l.running.version && e.ID == "chalkos_"+l.running.version+".efi":
	case version == img.version && e.TriesLeft == tries && e.TriesDone <= 0:
	default:
		l.t.Errorf("the disk boots %q from %s (%d tries left), want %s or %s with %d tries", version, e.File, e.TriesLeft, l.running.version, img.version, tries)
	}
}

// boots checks that the disk boots img from the entry with the tries.
func (l *lab) boots(img image, tries int) {
	l.t.Helper()
	e, version := l.boot()
	if version != img.version || e.TriesLeft != tries {
		l.t.Errorf("the disk boots %q from %s (%d tries left), want %s with %d tries", version, e.File, e.TriesLeft, img.version, tries)
	}
}

func (l *lab) install(img image) (Result, error) {
	return l.node.Install(context.Background(), img.header, img.stream())
}

// file returns a UKI's content, or nil when it is gone.
func (l *lab) file(name string) []byte {
	b, err := os.ReadFile(filepath.Join(l.esp, linuxDir, name))
	if err != nil {
		return nil
	}
	return b
}

// setUUID gives a partition of the disk image a UUID, as another tool might.
func (l *lab) setUUID(part int, uuid string) {
	l.t.Helper()
	if out, err := exec.Command("sfdisk", "--part-uuid", l.disk, strconv.Itoa(part), uuid).CombinedOutput(); err != nil {
		l.t.Fatalf("sfdisk: %v: %s", err, out)
	}
}
