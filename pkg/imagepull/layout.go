package imagepull

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/verity"
)

// writeLayout writes the layout of the image a describes into tmp: its manifest and config as the
// registry serves them and its layers, each checked against its digest, then checks its store
// against its hash tree.
func writeLayout(ctx context.Context, tmp string, a *ociimage.Artifact) error {
	img, err := ociimage.Image(ctx, a.Reference)
	if err != nil {
		return err
	}
	if _, err := ociimage.FillLayout(ctx, img, tmp); err != nil {
		return err
	}
	blob := func(mediaType types.MediaType) (*os.File, int64, error) {
		d, err := a.Descriptor(mediaType)
		if err != nil {
			return nil, 0, err
		}
		f, err := os.Open(filepath.Join(tmp, "blobs", d.Digest.Algorithm, d.Digest.Hex))
		return f, d.Size, err
	}
	store, storeSize, err := blob(ociimage.StoreType)
	if err != nil {
		return err
	}
	defer store.Close()
	tree, treeSize, err := blob(ociimage.HashTreeType)
	if err != nil {
		return err
	}
	defer tree.Close()
	return checkStore(store, tree, storeSize, treeSize, a.Header.RootHash)
}

// checkStore checks the store of size bytes that data holds against the hash tree of treeSize
// bytes that tree holds and the root hash, in hex, every block as dm-verity reads it. The tree
// must cover exactly the store, as an image's build makes its parts.
func checkStore(data, tree io.ReaderAt, size, treeSize int64, rootHash string) error {
	root, err := hex.DecodeString(rootHash)
	if err != nil {
		return err
	}
	sb, err := verity.Verify(data, tree, root)
	if err != nil {
		return fmt.Errorf("its store does not match its hash tree: %w", err)
	}
	if sb.DataSize() != size || sb.HashSize() != treeSize {
		return fmt.Errorf("its hash tree of %d bytes covers %d bytes of store, but its hash tree layer is %d bytes and its store layer %d", sb.HashSize(), sb.DataSize(), treeSize, size)
	}
	return nil
}
