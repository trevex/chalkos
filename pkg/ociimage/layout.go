package ociimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/types"
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
		rc, err := l.Compressed()
		if err != nil {
			return v1.Hash{}, err
		}
		if err := p.WriteBlob(d, ctxReader{ctx, rc}); err != nil {
			return v1.Hash{}, err
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
	if err := os.Chmod(tmp, 0o755); err != nil {
		return v1.Hash{}, err
	}
	if _, err := os.Lstat(out); err == nil {
		return v1.Hash{}, fmt.Errorf("%s appeared while writing the layout", out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return v1.Hash{}, err
	}
	if err := os.Rename(tmp, out); err != nil {
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

// ReadLayout reads the chalkos image of an OCI image layout, whose index.json names one
// manifest.
func ReadLayout(dir string) (v1.Image, error) {
	p, err := layout.FromPath(dir)
	if err != nil {
		return nil, err
	}
	index, err := p.ImageIndex()
	if err != nil {
		return nil, err
	}
	m, err := index.IndexManifest()
	if err != nil {
		return nil, err
	}
	if len(m.Manifests) != 1 {
		return nil, fmt.Errorf("%s names %d manifests in its index.json; a chalkos image's layout names one", dir, len(m.Manifests))
	}
	img, err := index.Image(m.Manifests[0].Digest)
	if err != nil {
		return nil, err
	}
	if _, err := ReadHeader(img); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return img, nil
}
