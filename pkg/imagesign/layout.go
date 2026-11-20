package imagesign

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/secureboot"
)

// SignLayout copies the OCI layout in, as chalkbuild oci writes it, to the new directory out with
// its UKI and boot loader signed with the Secure Boot db key and checked against cert as firmware
// with cert in db would, and returns the new manifest's digest. It refuses before signing when
// out exists. in is not changed.
func SignLayout(ctx context.Context, in, out, key, cert string) (v1.Hash, error) {
	if err := atomicfile.CheckNew(out); err != nil {
		return v1.Hash{}, err
	}
	img, err := ociimage.ReadLayout(in)
	if err != nil {
		return v1.Hash{}, err
	}
	signer, err := secureboot.LoadSigner(key, cert)
	if err != nil {
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
		if err := signFile(path, signer); err != nil {
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

// extractLayer writes img's layer of the media type to a new file at path and fails unless it
// matches the layer's digest (see ociimage.VerifiedReader): a corrupted UKI or boot loader would
// otherwise come out of signFile validly signed.
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
