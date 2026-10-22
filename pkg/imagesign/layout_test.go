package imagesign

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/uki"
)

// unsignedLayout writes the layout of a test image directory into dir/unsigned and returns its
// path and its manifest's digest.
func unsignedLayout(t *testing.T, dir string) (string, string) {
	t.Helper()
	in := filepath.Join(dir, "in")
	img := imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"}
	root := imagetest.Write(t, in, img)
	imagetest.WriteContract(t, in, img, root)
	oci, closeImage, err := ociimage.FromDirectory(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	defer closeImage()
	out := filepath.Join(dir, "unsigned")
	d, err := ociimage.WriteLayout(context.Background(), oci, out)
	if err != nil {
		t.Fatal(err)
	}
	return out, d.String()
}

// TestSignLayout signs the UKI and the boot loader of a layout's image into a new layout whose
// header, store, hash tree and contract stay as they are, and leaves the input as it is.
func TestSignLayout(t *testing.T) {
	needSigningTools(t)
	dir := t.TempDir()
	key, cert := imagetest.Signer(t, dir, "db")
	in, before := unsignedLayout(t, dir)
	out := filepath.Join(dir, "signed")
	digest, err := SignLayout(context.Background(), in, out, key, cert)
	if err != nil {
		t.Fatal(err)
	}
	if digest.String() == before {
		t.Error("the signed layout has the unsigned manifest")
	}
	signed, err := ociimage.ReadLayout(out)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := signed.Digest(); d != digest {
		t.Errorf("the signed layout names %s, SignLayout returned %s", d, digest)
	}
	certs, err := readCerts(cert)
	if err != nil {
		t.Fatal(err)
	}
	for _, mt := range []types.MediaType{ociimage.UKIType, ociimage.BootLoaderType} {
		l, err := ociimage.Layer(signed, mt)
		if err != nil {
			t.Fatal(err)
		}
		rc, _ := l.Compressed()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if err := uki.VerifySignature(bytes.NewReader(data), int64(len(data)), uki.Database{Certificates: certs}, uki.Database{}); err != nil {
			t.Errorf("%s: %v", mt, err)
		}
	}
	original, err := ociimage.ReadLayout(in)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := original.Digest(); d.String() != before {
		t.Errorf("the input layout changed to %s", d)
	}
	for _, mt := range []types.MediaType{ociimage.StoreType, ociimage.HashTreeType, ociimage.ContractType} {
		a, _ := ociimage.Layer(original, mt)
		b, _ := ociimage.Layer(signed, mt)
		da, _ := a.Digest()
		db, _ := b.Digest()
		if da != db {
			t.Errorf("%s changed from %s to %s", mt, da, db)
		}
	}
	ha, _ := ociimage.ReadHeader(original)
	hb, _ := ociimage.ReadHeader(signed)
	if ha != hb {
		t.Errorf("header changed from %+v to %+v", ha, hb)
	}
}

// TestSignLayoutRefusesACorruptUKI fails, and leaves out unwritten, when the UKI blob of the
// layout it signs does not hash to the digest its manifest names: a layout's blob store does not
// check a blob's content against the digest its file name is, so a corrupted UKI would otherwise
// come out of SignFile validly signed and validly addressed.
func TestSignLayoutRefusesACorruptUKI(t *testing.T) {
	dir := t.TempDir()
	key, cert := imagetest.Signer(t, dir, "db")
	in, _ := unsignedLayout(t, dir)
	img, err := ociimage.ReadLayout(in)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ociimage.Layer(img, ociimage.UKIType)
	if err != nil {
		t.Fatal(err)
	}
	d, err := l.Digest()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(in, "blobs", d.Algorithm, d.Hex)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "signed")
	if _, err := SignLayout(context.Background(), in, out, key, cert); err == nil {
		t.Error("SignLayout over a corrupt UKI = nil error")
	}
	if _, err := os.Stat(out); err == nil || !os.IsNotExist(err) {
		t.Errorf("out = %v, want it to stay absent", err)
	}
}

// TestSignLayoutNeedsANewDirectory refuses an output that exists before it signs anything.
func TestSignLayoutNeedsANewDirectory(t *testing.T) {
	needSigningTools(t)
	dir := t.TempDir()
	key, cert := imagetest.Signer(t, dir, "db")
	in, _ := unsignedLayout(t, dir)
	out := filepath.Join(dir, "signed")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := SignLayout(context.Background(), in, out, key, cert); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("SignLayout = %v", err)
	}
}
