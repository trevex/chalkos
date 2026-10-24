package ociimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"golang.org/x/sys/unix"
)

// IsLayout tells whether dir is an OCI image layout: whether it holds an oci-layout file.
func IsLayout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "oci-layout"))
	return err == nil
}

// CheckNew refuses an output directory that exists, or whose parent directory does not.
func CheckNew(out string) error {
	if _, err := os.Lstat(out); err == nil {
		return fmt.Errorf("%s exists; the output is a new directory", out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(out)
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is no directory", parent)
	}
	return nil
}

// RenameNew renames the directory tmp to out, which CheckNew found missing, and fails if out
// exists by now. It renames with RENAME_NOREPLACE, so out cannot appear between the check and the
// rename; on a file system without RENAME_NOREPLACE it checks out just before renaming.
func RenameNew(tmp, out string) error {
	err := unix.Renameat2(unix.AT_FDCWD, tmp, unix.AT_FDCWD, out, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		if _, err := os.Lstat(out); err == nil {
			return fmt.Errorf("%s appeared while it was being written", out)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return os.Rename(tmp, out)
	}
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s appeared while it was being written", out)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: tmp, New: out, Err: err}
	}
	return nil
}

// WriteLayout writes a chalkos image as an OCI image layout into the new directory out, whose
// index.json names its one manifest with the artifact type, and returns the manifest's digest.
// out must not exist and its parent directory must. The layout is built in a directory next to
// out and renamed to out once complete; on any failure, including ctx ending, which stops the
// copy of a layer, the partial layout is removed.
func WriteLayout(ctx context.Context, img v1.Image, out string) (_ v1.Hash, err error) {
	if err := CheckNew(out); err != nil {
		return v1.Hash{}, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(out), "."+filepath.Base(out)+"-")
	if err != nil {
		return v1.Hash{}, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()
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
	if err := RenameNew(tmp, out); err != nil {
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
// checks that it read exactly the bytes the layer's Digest and Size name.
//
// A layout's blob store does not check a blob against the digest its file name is, so a local
// layer needs this check as much as a remote one. WriteBlob and io.Copy close their reader without
// checking the error of Close. A caller therefore calls Verify once its copy, and closing the
// copy's destination, both succeeded.
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

// Verify fails unless the VerifiedReader read exactly the bytes its layer's own Digest and Size
// say it is.
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

// ReadLayout reads the chalkos image of an OCI image layout, whose index.json names one
// manifest. A layout's blob store does not check a blob's content against the digest its file
// name is, so ReadLayout checks the manifest bytes against index.json's descriptor and the
// config bytes against the manifest's, before trusting either: a layout corrupted since
// WriteLayout wrote it is refused here, rather than carried into a sign or a push. Its layers are
// checked the same way as they are read, by NewVerifiedReader.
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
