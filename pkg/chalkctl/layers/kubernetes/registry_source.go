package kubernetes

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// openSource reads the image of source: an OCI image layout directory, an OCI archive (a tar of
// one) or a docker archive as docker save writes it, each of one image or index, or else a
// reference to an image in a registry, read with the docker config's credentials and opts. A
// local source is read through before it is returned, so that a missing blob fails before the
// first endpoint. cleanup removes what reading it unpacked.
func openSource(ctx context.Context, source string, opts ...remote.Option) (img pushable, cleanup func(), err error) {
	cleanup = func() {}
	fi, err := os.Stat(source)
	switch {
	case err == nil && fi.IsDir():
		if img, err = fromLayout(source); err == nil {
			err = checkSource(img)
		}
		if err != nil {
			return nil, cleanup, fmt.Errorf("%s: %w", source, err)
		}
		return img, cleanup, nil
	case err == nil:
		return fromArchive(source, fi.Size())
	case !errors.Is(err, fs.ErrNotExist):
		return nil, cleanup, err
	}
	ref, err := name.ParseReference(source)
	if err != nil {
		return nil, cleanup, fmt.Errorf("%s is no file, no directory and no image reference: %w", source, err)
	}
	opts = append([]remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain)}, opts...)
	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return nil, cleanup, fmt.Errorf("%s is no file and no directory, and as an image reference: %w", source, err)
	}
	if desc.MediaType.IsIndex() {
		img, err = desc.ImageIndex()
	} else {
		img, err = desc.Image()
	}
	return img, cleanup, err
}

// fromLayout reads the one image or index the OCI image layout dir names.
func fromLayout(dir string) (pushable, error) {
	idx, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		return nil, err
	}
	m, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	if len(m.Manifests) != 1 {
		return nil, fmt.Errorf("the layout names %d images; push a layout of one image", len(m.Manifests))
	}
	d := m.Manifests[0]
	if d.MediaType.IsIndex() {
		return idx.ImageIndex(d.Digest)
	}
	return idx.Image(d.Digest)
}

// checkSource reads the manifests and configs of img and of every image an index of it names,
// and opens each layer, which fails on a blob the source lacks.
func checkSource(img pushable) error {
	switch img := img.(type) {
	case v1.ImageIndex:
		m, err := img.IndexManifest()
		if err != nil {
			return err
		}
		for _, d := range m.Manifests {
			var child pushable
			switch {
			case d.MediaType.IsIndex():
				child, err = img.ImageIndex(d.Digest)
			case d.MediaType.IsImage():
				child, err = img.Image(d.Digest)
			default:
				continue
			}
			if err == nil {
				err = checkSource(child)
			}
			if err != nil {
				return fmt.Errorf("the image %s of the index: %w", d.Digest, err)
			}
		}
	case v1.Image:
		if _, err := img.RawConfigFile(); err != nil {
			return err
		}
		layers, err := img.Layers()
		if err != nil {
			return err
		}
		for _, l := range layers {
			rc, err := l.Compressed()
			if err != nil {
				d, _ := l.Digest()
				return fmt.Errorf("the layer %s: %w", d, err)
			}
			rc.Close()
		}
	}
	return nil
}

// fromArchive reads an OCI archive of size bytes, unpacked into a directory that cleanup
// removes, or a docker archive of one image.
func fromArchive(path string, size int64) (pushable, func(), error) {
	none := func() {}
	oci, err := holds(path, "oci-layout")
	if err != nil {
		return nil, none, err
	}
	if !oci {
		img, err := tarball.ImageFromPath(path, nil)
		if err != nil {
			return nil, none, fmt.Errorf("%s is neither an OCI archive nor a docker archive of one image: %w", path, err)
		}
		if err := checkSource(img); err != nil {
			return nil, none, fmt.Errorf("%s: %w", path, err)
		}
		return img, none, nil
	}
	dir, err := os.MkdirTemp("", "chalkctl-oci-")
	if err != nil {
		return nil, none, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	var img pushable
	err = unpack(path, dir, size)
	if err == nil {
		img, err = fromLayout(dir)
	}
	if err == nil {
		err = checkSource(img)
	}
	if err != nil {
		cleanup()
		return nil, none, fmt.Errorf("%s: %w", path, err)
	}
	return img, cleanup, nil
}

// holds reports whether the tar archive at path holds the file name at its top.
func holds(path, file string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
		if filepath.Clean(h.Name) == file {
			return true, nil
		}
	}
}

// unpack writes the directories and regular files of the tar archive at path, of size bytes,
// into dir. It refuses an entry that leaves dir, and files of more bytes in all than the archive
// has, which only sparse entries give.
func unpack(path, dir string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	left := size
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !filepath.IsLocal(h.Name) {
			return fmt.Errorf("the entry %s leaves the archive's directory", h.Name)
		}
		target := filepath.Join(dir, h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			var n int64
			n, err = writeEntry(target, tr, left)
			left -= n
		}
		if err != nil {
			return err
		}
	}
}

// writeEntry writes the file target from r and fails once it took more than max bytes.
func writeEntry(target string, r io.Reader, max int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(target)
	if err != nil {
		return 0, err
	}
	n, err := io.CopyN(f, r, max+1)
	switch {
	case err == nil:
		err = fmt.Errorf("the archive unpacks to more bytes than it holds, as sparse entries do; write it without them")
	case errors.Is(err, io.EOF):
		err = nil
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}
