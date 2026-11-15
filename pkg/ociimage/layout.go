package ociimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/atomicfile"
)

// IsLayout tells whether dir is an OCI image layout: whether it holds an oci-layout file.
func IsLayout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "oci-layout"))
	return err == nil
}

// WriteLayout writes a chalkos image as an OCI image layout into the new directory out, whose
// index.json names its one manifest with the artifact type, and returns the manifest's digest.
// It writes out as atomicfile.NewDir does; ctx ending stops the copy of a layer.
func WriteLayout(ctx context.Context, img v1.Image, out string) (v1.Hash, error) {
	var digest v1.Hash
	err := atomicfile.NewDir(out, func(tmp string) (err error) {
		digest, err = writeLayout(ctx, img, tmp)
		return err
	})
	return digest, err
}

func writeLayout(ctx context.Context, img v1.Image, tmp string) (v1.Hash, error) {
	p, err := layout.Write(tmp, empty.Index)
	if err != nil {
		return v1.Hash{}, err
	}
	layers, err := img.Layers()
	if err != nil {
		return v1.Hash{}, err
	}
	for _, l := range layers {
		d, err := l.Digest()
		if err != nil {
			return v1.Hash{}, err
		}
		vr, err := NewVerifiedReader(l)
		if err != nil {
			return v1.Hash{}, err
		}
		if err := p.WriteBlob(d, ctxReader{ctx, vr}); err != nil {
			return v1.Hash{}, err
		}
		if err := vr.Verify(); err != nil {
			return v1.Hash{}, fmt.Errorf("the layer %s: %w", d, err)
		}
	}
	configName, err := img.ConfigName()
	if err != nil {
		return v1.Hash{}, err
	}
	config, err := img.RawConfigFile()
	if err != nil {
		return v1.Hash{}, err
	}
	if err := p.WriteBlob(configName, io.NopCloser(bytes.NewReader(config))); err != nil {
		return v1.Hash{}, err
	}
	digest, err := img.Digest()
	if err != nil {
		return v1.Hash{}, err
	}
	manifest, err := img.RawManifest()
	if err != nil {
		return v1.Hash{}, err
	}
	if err := p.WriteBlob(digest, io.NopCloser(bytes.NewReader(manifest))); err != nil {
		return v1.Hash{}, err
	}
	if err := p.AppendDescriptor(v1.Descriptor{MediaType: types.OCIManifestSchema1, Size: int64(len(manifest)), Digest: digest, ArtifactType: ArtifactType}); err != nil {
		return v1.Hash{}, err
	}
	// layout.Write and AppendDescriptor write index.json and oci-layout world-writable; a layout
	// is otherwise read-only once written, so narrow them before the layout becomes visible as out.
	for _, name := range []string{"oci-layout", "index.json"} {
		if err := os.Chmod(filepath.Join(tmp, name), 0o644); err != nil {
			return v1.Hash{}, err
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return v1.Hash{}, err
	}
	return digest, nil
}

// ctxReader fails once ctx ends, so that Ctrl-C stops the copy of a store of gigabytes.
type ctxReader struct {
	ctx context.Context
	io.ReadCloser
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}

// VerifiedReader reads a layer's content and hashes every byte read through it. Verify then
// checks that it read exactly the bytes the layer's Digest and Size name. A layout's blob store
// does not check a blob against the digest its file name is, so a local layer needs this as much
// as a remote one. WriteBlob and io.Copy ignore the error of Close, so call Verify once the copy
// and closing its destination succeeded.
type VerifiedReader struct {
	io.ReadCloser
	hash hash.Hash
	n    int64
	want v1.Hash
	size int64
}

// NewVerifiedReader is a VerifiedReader of the layer l's content.
func NewVerifiedReader(l v1.Layer) (*VerifiedReader, error) {
	want, err := l.Digest()
	if err != nil {
		return nil, err
	}
	size, err := l.Size()
	if err != nil {
		return nil, err
	}
	rc, err := l.Compressed()
	if err != nil {
		return nil, err
	}
	return &VerifiedReader{ReadCloser: rc, hash: sha256.New(), want: want, size: size}, nil
}

func (v *VerifiedReader) Read(p []byte) (int, error) {
	n, err := v.ReadCloser.Read(p)
	v.hash.Write(p[:n])
	v.n += int64(n)
	return n, err
}

// Verify fails unless the VerifiedReader read exactly the bytes of its layer's Digest and Size.
func (v *VerifiedReader) Verify() error {
	if v.n != v.size {
		return fmt.Errorf("read %d bytes, not the %d its descriptor names", v.n, v.size)
	}
	got := v1.Hash{Algorithm: v.want.Algorithm, Hex: hex.EncodeToString(v.hash.Sum(nil))}
	if got != v.want {
		return fmt.Errorf("read a blob of digest %s, not %s", got, v.want)
	}
	return nil
}

// ReadLayout reads the chalkos image of the layout at dir, whose index.json names one manifest.
// It checks the manifest and config against their digests; VerifiedReader checks the layers.
func ReadLayout(dir string) (v1.Image, error) {
	p, err := layout.FromPath(dir)
	if err != nil {
		return nil, err
	}
	index, err := p.ImageIndex()
	if err != nil {
		return nil, err
	}
	im, err := index.IndexManifest()
	if err != nil {
		return nil, err
	}
	if len(im.Manifests) != 1 {
		return nil, fmt.Errorf("%s names %d manifests in its index.json; a chalkos image's layout names one", dir, len(im.Manifests))
	}
	desc := im.Manifests[0]
	img, err := index.Image(desc.Digest)
	if err != nil {
		return nil, err
	}
	rawManifest, err := img.RawManifest()
	if err != nil {
		return nil, err
	}
	if err := checkBlobDigest(rawManifest, desc.Digest, desc.Size); err != nil {
		return nil, fmt.Errorf("%s: its manifest %w", dir, err)
	}
	m, err := img.Manifest()
	if err != nil {
		return nil, err
	}
	config, err := img.RawConfigFile()
	if err != nil {
		return nil, err
	}
	if err := checkBlobDigest(config, m.Config.Digest, m.Config.Size); err != nil {
		return nil, fmt.Errorf("%s: its config %w", dir, err)
	}
	if _, err := ReadHeader(img); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return img, nil
}

// checkBlobDigest fails unless data is exactly size bytes whose digest is want.
func checkBlobDigest(data []byte, want v1.Hash, size int64) error {
	got, n, err := v1.SHA256(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("is %d bytes, not the %d its descriptor names", n, size)
	}
	if got != want {
		return fmt.Errorf("hashes to %s, not %s", got, want)
	}
	return nil
}
