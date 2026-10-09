// Package verity reads and checks dm-verity hash trees as systemd-repart and veritysetup write
// them: a superblock, then the tree's levels, the top one first. A store's verity root hash
// covers its data blocks, which the superblock counts; the data partition may be larger.
package verity

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
)

// SuperblockSize is the size of the superblock at the start of a hash device.
const SuperblockSize = 512

var signature = []byte("verity\x00\x00")

// Superblock describes a hash tree. Only what systemd-repart writes is supported: version 1,
// hash type 1 (the salt before the block) and SHA-256.
type Superblock struct {
	UUID          [16]byte
	DataBlockSize uint32
	HashBlockSize uint32
	DataBlocks    uint64
	Salt          []byte
}

// ParseSuperblock reads the superblock at the start of a hash device.
func ParseSuperblock(b []byte) (Superblock, error) {
	if len(b) < SuperblockSize {
		return Superblock{}, errors.New("no verity superblock: too short")
	}
	if !bytes.Equal(b[:8], signature) {
		return Superblock{}, errors.New("no verity superblock")
	}
	le := binary.LittleEndian
	if v := le.Uint32(b[8:]); v != 1 {
		return Superblock{}, fmt.Errorf("verity superblock version %d is not supported", v)
	}
	if t := le.Uint32(b[12:]); t != 1 {
		return Superblock{}, fmt.Errorf("verity hash type %d is not supported", t)
	}
	if alg := string(bytes.TrimRight(b[32:64], "\x00")); alg != "sha256" {
		return Superblock{}, fmt.Errorf("verity hash algorithm %q is not supported", alg)
	}
	sb := Superblock{
		DataBlockSize: le.Uint32(b[64:]),
		HashBlockSize: le.Uint32(b[68:]),
		DataBlocks:    le.Uint64(b[72:]),
	}
	copy(sb.UUID[:], b[16:32])
	saltSize := int(le.Uint16(b[80:]))
	if saltSize > 256 {
		return Superblock{}, fmt.Errorf("verity salt of %d bytes is too long", saltSize)
	}
	sb.Salt = bytes.Clone(b[88 : 88+saltSize])
	return sb, sb.validate()
}

// maxBlockSize is the largest block the kernel's dm-verity takes: one no larger than a page.
const maxBlockSize = 4096

func (sb Superblock) validate() error {
	for _, size := range []uint32{sb.DataBlockSize, sb.HashBlockSize} {
		if size < 512 || size > maxBlockSize || size&(size-1) != 0 {
			return fmt.Errorf("verity block size %d is not a power of two from 512 to %d", size, maxBlockSize)
		}
	}
	if sb.DataBlocks == 0 || sb.DataBlocks > 1<<40 {
		return fmt.Errorf("verity tree of %d data blocks", sb.DataBlocks)
	}
	return nil
}

// Marshal encodes the superblock as it starts a hash device.
func (sb Superblock) Marshal() []byte {
	b := make([]byte, SuperblockSize)
	le := binary.LittleEndian
	copy(b, signature)
	le.PutUint32(b[8:], 1)
	le.PutUint32(b[12:], 1)
	copy(b[16:], sb.UUID[:])
	copy(b[32:], "sha256")
	le.PutUint32(b[64:], sb.DataBlockSize)
	le.PutUint32(b[68:], sb.HashBlockSize)
	le.PutUint64(b[72:], sb.DataBlocks)
	le.PutUint16(b[80:], uint16(len(sb.Salt)))
	copy(b[88:], sb.Salt)
	return b
}

// DataSize is how many bytes of the data device the tree covers.
func (sb Superblock) DataSize() int64 { return int64(sb.DataBlocks) * int64(sb.DataBlockSize) }

// HashSize is how many bytes of the hash device the superblock and the tree take.
func (sb Superblock) HashSize() int64 {
	levels := sb.levels()
	end := sb.hashStart()
	for _, l := range levels {
		end += l.blocks
	}
	return end * int64(sb.HashBlockSize)
}

// level is one level of the tree, in hash blocks: where it starts on the hash device and how
// many blocks it has. Level 0 holds the digests of the data blocks.
type level struct{ start, blocks int64 }

// hashStart is the first hash block after the superblock.
func (sb Superblock) hashStart() int64 {
	hbs := int64(sb.HashBlockSize)
	return (SuperblockSize + hbs - 1) / hbs
}

// perBlockBits is log2 of how many digests a hash block holds.
func (sb Superblock) perBlockBits() int {
	return bits.Len32(sb.HashBlockSize/sha256.Size) - 1
}

// levels lays the tree out as veritysetup does: enough levels for one block at the top, which
// comes first on the device.
func (sb Superblock) levels() []level {
	b := sb.perBlockBits()
	n := 0
	for b*n < 64 && (sb.DataBlocks-1)>>(b*n) != 0 {
		n++
	}
	levels := make([]level, n)
	pos := sb.hashStart()
	for i := n - 1; i >= 0; i-- {
		shift := uint((i + 1) * b)
		levels[i] = level{start: pos, blocks: int64((sb.DataBlocks + 1<<shift - 1) >> shift)}
		pos += levels[i].blocks
	}
	return levels
}

func (sb Superblock) digest(block []byte) [sha256.Size]byte {
	h := sha256.New()
	h.Write(sb.Salt)
	h.Write(block)
	var d [sha256.Size]byte
	h.Sum(d[:0])
	return d
}

// readChunk is how much one read takes from a device while hashing it.
const readChunk = 4 << 20

// hashLevel digests n blocks of size bs that src holds from offset off and hands each finished
// hash block of the next level to emit, in order.
func (sb Superblock) hashLevel(src io.ReaderAt, off, n int64, bs int, emit func(j int64, block []byte) error) error {
	perBlock := int64(1) << sb.perBlockBits()
	hashBlock := make([]byte, sb.HashBlockSize)
	groups := max(readChunk/(int64(bs)*perBlock), 1)
	// A level smaller than a read takes a buffer of its own size.
	buf := make([]byte, min(n, groups*perBlock)*int64(bs))
	for first := int64(0); first < n; first += groups * perBlock {
		count := min(groups*perBlock, n-first)
		chunk := buf[:count*int64(bs)]
		if _, err := src.ReadAt(chunk, off+first*int64(bs)); err != nil {
			return fmt.Errorf("read block %d: %w", first, err)
		}
		for g := int64(0); g*perBlock < count; g++ {
			clear(hashBlock)
			for k := int64(0); k < perBlock && g*perBlock+k < count; k++ {
				i := (g*perBlock + k) * int64(bs)
				d := sb.digest(chunk[i : i+int64(bs)])
				copy(hashBlock[k*sha256.Size:], d[:])
			}
			if err := emit((first/perBlock)+g, hashBlock); err != nil {
				return err
			}
		}
	}
	return nil
}

// Tree computes the hash device for the data: the superblock and the tree. It returns the
// device's content and the root hash.
func Tree(data io.ReaderAt, sb Superblock) ([]byte, []byte, error) {
	if err := sb.validate(); err != nil {
		return nil, nil, err
	}
	dev := make([]byte, sb.HashSize())
	copy(dev, sb.Marshal())
	root, err := sb.walk(data, bytesAt(dev), func(l level, j int64, block []byte) error {
		copy(dev[(l.start+j)*int64(sb.HashBlockSize):], block)
		return nil
	})
	return dev, root, err
}

// Verify checks the data and hash devices against the root hash: every data block the tree
// covers and every block of the tree. It returns the hash device's superblock.
func Verify(data, hash io.ReaderAt, root []byte) (Superblock, error) {
	head := make([]byte, SuperblockSize)
	if _, err := hash.ReadAt(head, 0); err != nil {
		return Superblock{}, fmt.Errorf("read the verity superblock: %w", err)
	}
	sb, err := ParseSuperblock(head)
	if err != nil {
		return Superblock{}, err
	}
	stored := make([]byte, sb.HashBlockSize)
	got, err := sb.walk(data, hash, func(l level, j int64, block []byte) error {
		if _, err := hash.ReadAt(stored, (l.start+j)*int64(sb.HashBlockSize)); err != nil {
			return fmt.Errorf("read hash block %d: %w", l.start+j, err)
		}
		if !bytes.Equal(stored, block) {
			return fmt.Errorf("hash block %d does not match the blocks it covers", l.start+j)
		}
		return nil
	})
	if err != nil {
		return Superblock{}, err
	}
	if len(root) != len(got) || subtle.ConstantTimeCompare(root, got) != 1 {
		return Superblock{}, fmt.Errorf("the verity root hash is %x, want %x", got, root)
	}
	return sb, nil
}

// walk computes the tree level by level, handing each hash block to emit, and returns the root
// hash. Each level above the first is computed from the level below as tree holds it, which
// emit has checked or written by then.
func (sb Superblock) walk(data, tree io.ReaderAt, emit func(l level, j int64, block []byte) error) ([]byte, error) {
	levels := sb.levels()
	if len(levels) == 0 {
		block := make([]byte, sb.DataBlockSize)
		if _, err := data.ReadAt(block, 0); err != nil {
			return nil, fmt.Errorf("read data block 0: %w", err)
		}
		d := sb.digest(block)
		return d[:], nil
	}
	if err := sb.hashLevel(data, 0, int64(sb.DataBlocks), int(sb.DataBlockSize), func(j int64, block []byte) error {
		return emit(levels[0], j, block)
	}); err != nil {
		return nil, fmt.Errorf("data: %w", err)
	}
	hbs := int64(sb.HashBlockSize)
	for i := 1; i < len(levels); i++ {
		below := levels[i-1]
		if err := sb.hashLevel(tree, below.start*hbs, below.blocks, int(hbs), func(j int64, block []byte) error {
			return emit(levels[i], j, block)
		}); err != nil {
			return nil, fmt.Errorf("hash level %d: %w", i, err)
		}
	}
	top := make([]byte, hbs)
	if _, err := tree.ReadAt(top, levels[len(levels)-1].start*hbs); err != nil {
		return nil, fmt.Errorf("read the top hash block: %w", err)
	}
	d := sb.digest(top)
	return d[:], nil
}

// bytesAt reads a byte slice as a device.
type bytesAt []byte

func (b bytesAt) ReadAt(p []byte, off int64) (int, error) {
	return bytes.NewReader(b).ReadAt(p, off)
}

// Root reads the hash device's superblock and computes the root hash from its top hash block,
// without checking the blocks below: a quick look at which tree the device holds, which Verify
// then confirms.
func Root(hash io.ReaderAt) (Superblock, []byte, error) {
	head := make([]byte, SuperblockSize)
	if _, err := hash.ReadAt(head, 0); err != nil {
		return Superblock{}, nil, fmt.Errorf("read the verity superblock: %w", err)
	}
	sb, err := ParseSuperblock(head)
	if err != nil {
		return Superblock{}, nil, err
	}
	levels := sb.levels()
	if len(levels) == 0 {
		return sb, nil, errors.New("a tree of one data block has no hash block")
	}
	top := make([]byte, sb.HashBlockSize)
	if _, err := hash.ReadAt(top, levels[len(levels)-1].start*int64(sb.HashBlockSize)); err != nil {
		return Superblock{}, nil, fmt.Errorf("read the top hash block: %w", err)
	}
	d := sb.digest(top)
	return sb, d[:], nil
}
