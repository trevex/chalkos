package image

import (
	"bytes"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/verity"
)

func TestReadStore(t *testing.T) {
	for _, blockSize := range []uint32{512, 4096} {
		data := bytes.Repeat([]byte("store"), 300*int(blockSize)/5)
		tree, root, err := verity.Tree(bytes.NewReader(data), verity.Superblock{DataBlockSize: blockSize, HashBlockSize: blockSize, DataBlocks: 300, Salt: []byte("salt")})
		if err != nil {
			t.Fatal(err)
		}
		// The partitions are larger than what the tree covers, as repart's fixed sizes leave them.
		raw := make([]byte, 4<<20)
		copy(raw[4096:], tree)
		copy(raw[65536:], data)
		hexRoot := hex.EncodeToString(root)
		parts := []Partition{
			{Type: "esp", Offset: 0, RawSize: 4096},
			{Type: "usr-x86-64-verity", Offset: 4096, RawSize: 61440, RootHash: hexRoot},
			{Type: "usr-x86-64", Offset: 65536, RawSize: 2 << 20, RootHash: hexRoot},
		}
		s, err := ReadStore(bytes.NewReader(raw), parts)
		if err != nil {
			t.Fatalf("blocks of %d bytes: %v", blockSize, err)
		}
		gotData, _ := io.ReadAll(s.Data)
		gotTree, _ := io.ReadAll(s.HashTree)
		if !bytes.Equal(gotData, data) || !bytes.Equal(gotTree, tree) || !bytes.Equal(s.RootHash, root) {
			t.Errorf("blocks of %d bytes: read %d bytes of store and %d of hash tree, want %d and %d", blockSize, len(gotData), len(gotTree), len(data), len(tree))
		}

		other := strings.Repeat("ab", 32)
		for name, change := range map[string]func(p []Partition){
			"another root hash": func(p []Partition) { p[1].RootHash, p[2].RootHash = other, other },
			"two root hashes":   func(p []Partition) { p[2].RootHash = other },
			"no verity":         func(p []Partition) { p[1].Type = "linux-generic" },
			"a short partition": func(p []Partition) { p[2].RawSize = 4096 },
		} {
			p := append([]Partition(nil), parts...)
			change(p)
			if _, err := ReadStore(bytes.NewReader(raw), p); err == nil {
				t.Errorf("blocks of %d bytes, %s: accepted", blockSize, name)
			}
		}
	}
}
