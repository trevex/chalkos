// Package imagepull pulls a chalkos image from a registry, by tag or by digest, into a local
// directory: an image directory as nix build makes it, which everything that reads one accepts,
// or the OCI layout the registry serves, whose manifest keeps its digest. A pull checks the image
// as a node does: its header against its manifest and contract, every part against its digest
// and the store against its hash tree.
package imagepull

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/ociimage"
)

// Pulled is the image a pull resolved its reference to.
type Pulled struct {
	// Reference is the image's repository and manifest digest, repository@sha256:….
	Reference string
	Header    ociimage.Header
	// Present is set when the directory held the image already, which the pull left as it was.
	Present bool
}

// Directory pulls the image ref names, by tag or by digest, into dir as an image directory: the
// raw disk image with repart-output.json, metadata.json, schemas/, repart.d/ and facts/. The tag
// is resolved once, and every fetch after it names the digest.
//
// dir must not exist, be empty, or hold the same image already: a directory ociimage.FromDirectory
// reads as the image of the same manifest digest, which the pull leaves as it is. dir appears only
// once the whole image is written and checked.
//
// The image's ESP is written with mtools, whose absence Directory reports before it fetches
// anything.
func Directory(ctx context.Context, ref, dir string) (Pulled, error) {
	if missing := image.MissingESPTools(); len(missing) > 0 {
		return Pulled{}, fmt.Errorf("pull %s: an image directory's ESP is written and read with mtools, whose %s PATH lacks; install mtools, which the flake's chalkctl and chalklab bring, or pull the OCI layout instead", ref, strings.Join(missing, ", "))
	}
	return pull(ctx, ref, dir, directoryDigest, writeDirectory)
}

// Layout pulls the image ref names, by tag or by digest, into dir as the OCI image layout of the
// manifest the registry serves, byte for byte, so that pushing the layout keeps its digest. dir
// must not exist, be empty, or hold the layout of the same digest already, as Directory's dir.
func Layout(ctx context.Context, ref, dir string) (Pulled, error) {
	return pull(ctx, ref, dir, layoutDigest, writeLayout)
}

// pull reads ref's header and contract, refuses a dir that holds another image, as held reads
// its digest, and writes dir with write unless it holds the image already.
func pull(ctx context.Context, ref, dir string, held func(ctx context.Context, dir string) (v1.Hash, error), write func(ctx context.Context, tmp string, a *ociimage.Artifact) error) (Pulled, error) {
	a, err := ociimage.Read(ctx, ref)
	if err != nil {
		return Pulled{}, fmt.Errorf("pull %s: %w", ref, err)
	}
	p := Pulled{Reference: a.Reference, Header: a.Header}
	_, digest, _ := strings.Cut(a.Reference, "@")
	if p.Present, err = holds(ctx, dir, digest, held); err != nil || p.Present {
		return p, err
	}
	if err := atomicfile.NewDir(dir, func(tmp string) error { return write(ctx, tmp, a) }); err != nil {
		return p, fmt.Errorf("pull %s: %w", a.Reference, err)
	}
	return p, nil
}

// holds reports whether dir holds the image of the digest already. It removes an empty dir, for
// the pull to write it anew, and refuses a dir that holds anything else.
func holds(ctx context.Context, dir, digest string, held func(ctx context.Context, dir string) (v1.Hash, error)) (bool, error) {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case len(entries) == 0:
		return false, os.Remove(dir)
	}
	got, err := held(ctx, dir)
	if err != nil {
		return false, fmt.Errorf("%s is not empty and holds no image to compare with %s, so the pull leaves it alone: %w", dir, digest, err)
	}
	if got.String() != digest {
		return false, fmt.Errorf("%s holds the image %s, not %s; pull into a new or empty directory", dir, got, digest)
	}
	return true, nil
}

// layoutDigest is the manifest digest of the OCI layout at dir.
func layoutDigest(_ context.Context, dir string) (v1.Hash, error) {
	img, err := ociimage.ReadLayout(dir)
	if err != nil {
		return v1.Hash{}, err
	}
	return img.Digest()
}

// directoryDigest is the manifest digest of the image of the image directory dir, as chalkbuild
// oci would write it.
func directoryDigest(ctx context.Context, dir string) (v1.Hash, error) {
	img, closeImage, err := ociimage.FromDirectory(ctx, dir)
	if err != nil {
		return v1.Hash{}, err
	}
	defer closeImage()
	return img.Digest()
}
