package image

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/trevex/chalkos/pkg/verity"
)

// Store is an image's store as an upgrade sends it: the data its root hash covers, which may be
// less than its partition holds, and its hash tree.
type Store struct {
	Data, HashTree *io.SectionReader
	RootHash       []byte
}

// ReadStore finds the store of a raw image whose partitions repart-output.json lists, and checks
// that its hash tree has the root hash repart reported.
func ReadStore(raw io.ReaderAt, parts []Partition) (Store, error) {
	var data, hash []Partition
	for _, p := range parts {
		switch {
		case strings.HasPrefix(p.Type, "usr-") && strings.HasSuffix(p.Type, "-verity"):
			hash = append(hash, p)
		case strings.HasPrefix(p.Type, "usr-"):
			data = append(data, p)
		}
	}
	if len(data) != 1 || len(hash) != 1 {
		return Store{}, fmt.Errorf("the image has %d store and %d verity partitions, want one of each", len(data), len(hash))
	}
	root, err := hex.DecodeString(hash[0].RootHash)
	if err != nil || len(root) != 32 || hash[0].RootHash != data[0].RootHash {
		return Store{}, fmt.Errorf("the image's store partitions name no single SHA-256 root hash")
	}
	sb, got, err := verity.Root(io.NewSectionReader(raw, hash[0].Offset, hash[0].RawSize))
	if err != nil {
		return Store{}, fmt.Errorf("the image's hash tree: %w", err)
	}
	if !bytes.Equal(got, root) {
		return Store{}, fmt.Errorf("the image's hash tree has the root hash %x, not the %x repart reported", got, root)
	}
	if sb.DataSize() > data[0].RawSize || sb.HashSize() > hash[0].RawSize {
		return Store{}, fmt.Errorf("the image's hash tree covers more than its partitions hold")
	}
	return Store{
		Data:     io.NewSectionReader(raw, data[0].Offset, sb.DataSize()),
		HashTree: io.NewSectionReader(raw, hash[0].Offset, sb.HashSize()),
		RootHash: root,
	}, nil
}
