package verity

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// veritysetup formats a hash device for the data as systemd-repart does and returns the device
// and the root hash.
func veritysetup(t *testing.T, data []byte, blockSize string, salt []byte) ([]byte, []byte) {
	t.Helper()
	if _, err := exec.LookPath("veritysetup"); err != nil {
		t.Skip("veritysetup not in PATH")
	}
	dir := t.TempDir()
	dataPath, hashPath := filepath.Join(dir, "data"), filepath.Join(dir, "hash")
	if err := os.WriteFile(dataPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("veritysetup", "format", "--data-block-size", blockSize, "--hash-block-size", blockSize,
		"--salt", hex.EncodeToString(salt), dataPath, hashPath).CombinedOutput()
	if err != nil {
		t.Fatalf("veritysetup: %v: %s", err, out)
	}
	m := regexp.MustCompile(`Root hash:\s+([0-9a-f]{64})`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("no root hash in %s", out)
	}
	root, _ := hex.DecodeString(string(m[1]))
	hash, err := os.ReadFile(hashPath)
	if err != nil {
		t.Fatal(err)
	}
	return hash, root
}

func randomData(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// TestAgreesWithVeritysetup checks trees of one to three levels and a single data block, of
// 4 KiB blocks as systemd-repart writes them and of 512-byte blocks as images built before that
// carry them, against veritysetup.
func TestAgreesWithVeritysetup(t *testing.T) {
	salt := randomData(1, 32)
	for _, tc := range []struct {
		name      string
		blockSize uint32
		blocks    int
	}{
		{"one block", 512, 1},
		{"one level", 512, 16},
		{"two levels", 512, 17},
		{"three levels, partial blocks", 512, 3000},
		{"4 KiB blocks", 4096, 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := randomData(uint64(tc.blocks), tc.blocks*int(tc.blockSize))
			want, root := veritysetup(t, data, strconv.Itoa(int(tc.blockSize)), salt)
			sb, err := ParseSuperblock(want)
			if err != nil {
				t.Fatal(err)
			}
			if sb.DataBlocks != uint64(tc.blocks) || sb.DataBlockSize != tc.blockSize || sb.HashBlockSize != tc.blockSize || !bytes.Equal(sb.Salt, salt) {
				t.Fatalf("superblock %+v", sb)
			}
			if sb.DataSize() != int64(len(data)) {
				t.Errorf("DataSize = %d, want %d", sb.DataSize(), len(data))
			}
			if sb.HashSize() > int64(len(want)) || !bytes.Equal(want[sb.HashSize():], make([]byte, int64(len(want))-sb.HashSize())) {
				t.Errorf("HashSize = %d leaves out part of the %d-byte device", sb.HashSize(), len(want))
			}
			if !bytes.Equal(sb.Marshal(), want[:SuperblockSize]) {
				t.Error("Marshal does not reproduce the superblock")
			}
			dev, got, err := Tree(bytes.NewReader(data), sb)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, root) {
				t.Errorf("root hash %x, want %x", got, root)
			}
			if !bytes.Equal(dev, want[:sb.HashSize()]) {
				t.Error("the tree differs from veritysetup's")
			}
			if _, err := Verify(bytes.NewReader(data), bytes.NewReader(want), root); err != nil {
				t.Errorf("Verify: %v", err)
			}
		})
	}
}

// TestVerifyRefusesChanges checks that a changed data block, a changed hash block beyond the
// superblock, a data device cut short and another root hash are refused.
func TestVerifyRefusesChanges(t *testing.T) {
	data := randomData(7, 3000*512)
	sb := Superblock{DataBlockSize: 512, HashBlockSize: 512, DataBlocks: 3000, Salt: randomData(8, 32)}
	hash, root, err := Tree(bytes.NewReader(data), sb)
	if err != nil {
		t.Fatal(err)
	}
	flip := func(b []byte, i int) []byte {
		c := bytes.Clone(b)
		c[i] ^= 1
		return c
	}
	for _, tc := range []struct {
		name       string
		data, hash []byte
		root       []byte
		want       string
	}{
		{"data block", flip(data, 1000*512+7), hash, root, "does not match"},
		{"last data block", flip(data, len(data)-1), hash, root, "does not match"},
		{"lowest hash level", data, flip(hash, len(hash)-1), root, "does not match"},
		{"top hash block", data, flip(hash, SuperblockSize+3), root, "does not match"},
		{"short data", data[:len(data)-512], hash, root, "read block"},
		{"root hash", data, hash, flip(root, 0), "root hash"},
		{"superblock", data, flip(hash, 0), root, "no verity superblock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Verify(bytes.NewReader(tc.data), bytes.NewReader(tc.hash), tc.root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Verify = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	if _, err := Verify(bytes.NewReader(data), bytes.NewReader(hash), root); err != nil {
		t.Errorf("Verify of the unchanged devices: %v", err)
	}
	if got, quick, err := Root(bytes.NewReader(hash)); err != nil || !bytes.Equal(quick, root) || got.DataBlocks != 3000 {
		t.Errorf("Root = %x, %v, want %x", quick, err, root)
	}
	if _, quick, _ := Root(bytes.NewReader(flip(hash, SuperblockSize+3))); bytes.Equal(quick, root) {
		t.Error("Root missed a changed top hash block")
	}
}

func TestParseSuperblockRefuses(t *testing.T) {
	good := Superblock{DataBlockSize: 512, HashBlockSize: 512, DataBlocks: 1}.Marshal()
	for _, tc := range []struct {
		name   string
		change func(b []byte)
	}{
		{"version 2", func(b []byte) { b[8] = 2 }},
		{"hash type 0", func(b []byte) { b[12] = 0 }},
		{"sha1", func(b []byte) { copy(b[32:], "sha1\x00\x00") }},
		{"block size 1000", func(b []byte) { b[64], b[65] = 0xe8, 0x03 }},
		{"data blocks of 8 KiB", func(b []byte) { binary.LittleEndian.PutUint32(b[64:], 8192) }},
		{"hash blocks of 1 MiB", func(b []byte) { binary.LittleEndian.PutUint32(b[68:], 1<<20) }},
		{"hash blocks of 256 bytes", func(b []byte) { binary.LittleEndian.PutUint32(b[68:], 256) }},
		{"no data blocks", func(b []byte) { b[72] = 0 }},
		{"long salt", func(b []byte) { b[81] = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := bytes.Clone(good)
			tc.change(b)
			if _, err := ParseSuperblock(b); err == nil {
				t.Error("accepted")
			}
		})
	}
	if _, err := ParseSuperblock(good); err != nil {
		t.Error(err)
	}
}

// allocated is how many bytes fn allocates.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestBoundedMemory checks that what a check allocates follows the devices' sizes, not what a
// superblock claims: a tree of large hash blocks is refused before anything is read, and a small
// store is checked with buffers its size.
func TestBoundedMemory(t *testing.T) {
	data := randomData(9, 300*512)
	sb := Superblock{DataBlockSize: 512, HashBlockSize: 512, DataBlocks: 300, Salt: randomData(10, 32)}
	hash, root, err := Tree(bytes.NewReader(data), sb)
	if err != nil {
		t.Fatal(err)
	}
	huge := bytes.Clone(hash)
	binary.LittleEndian.PutUint32(huge[68:], 1<<20)
	var verr error
	if n := allocated(func() { _, verr = Verify(bytes.NewReader(data), bytes.NewReader(huge), root) }); verr == nil || n > 64<<10 {
		t.Errorf("Verify of a tree of 1 MiB hash blocks: %v, after allocating %d bytes", verr, n)
	}
	if n := allocated(func() { _, verr = Verify(bytes.NewReader(data), bytes.NewReader(hash), root) }); verr != nil || n > 256<<10 {
		t.Errorf("Verify of a %d-byte store: %v, after allocating %d bytes", len(data), verr, n)
	}
}
