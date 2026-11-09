// Package upgradetest holds the fixtures of the tests that write an image onto a disk: a small
// image of random data that a UKI boots, and a partition of a disk image as a file. It does not
// import upgrade, so upgrade's own tests share it.
package upgradetest

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"

	"github.com/trevex/chalkos/pkg/imagepart"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
	"github.com/trevex/chalkos/pkg/verity"
)

// Options are what NewImage makes an image of.
type Options struct {
	// Name seeds the store and its hash tree's salt, so images of different names differ in their
	// root hashes.
	Name string
	// ImageID is the image ID of the UKI's os-release; empty means chalkos.
	ImageID string
	// BlockSize and Blocks are the size and the number of the store's dm-verity blocks.
	BlockSize uint32
	Blocks    uint64
	// Tries is the UKI's CHALKOS_BOOT_TRIES.
	Tries int
	// BootLoader gives the image a boot loader: a small PE binary stands in for systemd-boot.
	BootLoader bool
}

// Image is a test image: a store, its hash tree and root hash, a UKI booting it and, if asked
// for, a boot loader.
type Image struct {
	Name, ImageID   string
	Store, HashTree []byte
	Root            []byte
	UKI             []byte
	BootLoader      []byte
}

// NewImage makes the image of the options.
func NewImage(t testing.TB, o Options) Image {
	t.Helper()
	if o.ImageID == "" {
		o.ImageID = "chalkos"
	}
	seed := sha256.Sum256([]byte(o.Name))
	r := rand.New(rand.NewPCG(binary.LittleEndian.Uint64(seed[:]), binary.LittleEndian.Uint64(seed[8:])))
	store := make([]byte, o.Blocks*uint64(o.BlockSize))
	for i := range store {
		store[i] = byte(r.Uint32())
	}
	salt := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(r.Uint32())
	}
	hash, root, err := verity.Tree(bytes.NewReader(store), verity.Superblock{DataBlockSize: o.BlockSize, HashBlockSize: o.BlockSize, DataBlocks: o.Blocks, Salt: salt})
	if err != nil {
		t.Fatal(err)
	}
	img := Image{Name: o.Name, ImageID: o.ImageID, Store: store, HashTree: hash, Root: root}
	img.UKI = UKI(OSRelease(o.ImageID, o.Tries), root)
	if o.BootLoader {
		img.BootLoader = ukitest.Build(map[string][]byte{".text": []byte("systemd-boot " + o.Name)})
	}
	return img
}

// OSRelease is the os-release of a worker image of the image ID for metal whose boots get the
// number of tries.
func OSRelease(imageID string, tries int) map[string]string {
	return map[string]string{
		"IMAGE_ID":           imageID,
		"IMAGE_VERSION":      "",
		"CHALKOS_VARIANT":    "worker",
		"CHALKOS_PLATFORM":   "metal",
		"CHALKOS_BOOT_TRIES": strconv.Itoa(tries),
	}
}

// UKI builds a UKI of the os-release booting the store of the root hash.
func UKI(osRelease map[string]string, root []byte) []byte {
	return ukitest.UKI(osRelease, fmt.Sprintf("init=/nix/store/x/init usrhash=%x", root))
}

// Slot describes the image's store and hash tree as a slot holds them.
func (img Image) Slot() storeslot.Image {
	return storeslot.Image{RootHash: img.Root, Store: Part(img.Store), Verity: Part(img.HashTree)}
}

// Stream is the image's parts as an install streams them: the store, the hash tree, the UKI and
// the boot loader, if any.
func (img Image) Stream() io.Reader {
	return io.MultiReader(bytes.NewReader(img.Store), bytes.NewReader(img.HashTree), bytes.NewReader(img.UKI), bytes.NewReader(img.BootLoader))
}

// Sum is the SHA-256 of b.
func Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// Part describes data as a part of an image.
func Part(data []byte) imagepart.Part {
	return imagepart.Part{Size: int64(len(data)), SHA256: Sum(data)}
}

// PartitionFile is a partition of a disk image: Size bytes of F from Off.
type PartitionFile struct {
	F         *os.File
	Off, Size int64
}

func (p PartitionFile) ReadAt(b []byte, off int64) (int, error) {
	if off >= p.Size {
		return 0, io.EOF
	}
	n, err := p.F.ReadAt(b[:min(int64(len(b)), p.Size-off)], p.Off+off)
	if err == nil && n < len(b) {
		err = io.EOF
	}
	return n, err
}

func (p PartitionFile) WriteAt(b []byte, off int64) (int, error) {
	if off+int64(len(b)) > p.Size {
		return 0, errors.New("write beyond the partition")
	}
	return p.F.WriteAt(b, p.Off+off)
}

func (p PartitionFile) Sync() error  { return p.F.Sync() }
func (p PartitionFile) Close() error { return p.F.Close() }
