package imagesign

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/ociimage"
)

// SignLayout copies the OCI image layout in, as chalkbuild oci writes it, to out with the UKI and
// the boot loader of its chalkos image signed with the Secure Boot db key and its certificate,
// and returns the new manifest's digest. It signs copies of the two layers in a temporary
// directory and checks them against cert, as firmware with cert in db would, before it writes
// out; the new layout holds them in place of the old layers, its other blobs as they are. out
// must not exist and its parent directory must, or SignLayout refuses before signing anything;
// in stays as it is.
func SignLayout(ctx context.Context, in, out, key, cert string) (v1.Hash, error) {
	if err := ociimage.CheckNew(out); err != nil {
		return v1.Hash{}, err
	}
	img, err := ociimage.ReadLayout(in)
	if err != nil {
		return v1.Hash{}, err
	}
	if _, err := os.Stat(key); err != nil {
		return v1.Hash{}, err
	}
	certs, err := readCerts(cert)
	if err != nil {
		return v1.Hash{}, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(out), ".imagesign-")
	if err != nil {
		return v1.Hash{}, err
	}
	defer os.RemoveAll(dir)
	var signed []ociimage.Blob
	for _, part := range []struct {
		mediaType  types.MediaType
		what, file string
	}{
		{ociimage.UKIType, "UKI", "uki.efi"},
		{ociimage.BootLoaderType, "boot loader", "boot-loader.efi"},
	} {
		path := filepath.Join(dir, part.file)
		if err := extractLayer(img, part.mediaType, path); err != nil {
			return v1.Hash{}, fmt.Errorf("the %s of %s: %w", part.what, in, err)
		}
		if err := SignFile(ctx, path, key, cert); err != nil {
			return v1.Hash{}, err
		}
		if err := verifyFile(path, certs); err != nil {
			return v1.Hash{}, fmt.Errorf("Secure Boot would refuse the %s: %w", part.what, err)
		}
		b, err := ociimage.FileBlob(part.mediaType, path)
		if err != nil {
			return v1.Hash{}, err
		}
		signed = append(signed, b)
	}
	signedImg, err := ociimage.Replace(img, signed...)
	if err != nil {
		return v1.Hash{}, err
	}
	return ociimage.WriteLayout(ctx, signedImg, out)
}

// extractLayer writes the layer of img of the media type to a new file at path, and fails unless
// what it wrote hashes to the layer's own digest: ReadLayout already checked the layout's
// manifest and config, but go-containerregistry's layout reader does not check a layer's blob
// against the digest its file name is, and a corrupted UKI or boot loader would otherwise come
// out of SignFile validly signed and validly addressed.
func extractLayer(img v1.Image, mediaType types.MediaType, path string) error {
	l, err := ociimage.Layer(img, mediaType)
	if err != nil {
		return err
	}
	vr, err := ociimage.NewVerifiedReader(l)
	if err != nil {
		return err
	}
	defer vr.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, vr); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return vr.Verify()
}
