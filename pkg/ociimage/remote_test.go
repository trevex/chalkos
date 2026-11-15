package ociimage_test

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
)

// TestPushAndRead pushes an image directory's layout and reads it back by tag: pinned to the
// manifest's digest, with its header, contract and metadata.
func TestPushAndRead(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	root := writeImage(t, in)
	out := filepath.Join(dir, "out")
	d := writeLayout(t, in, out)
	img, err := ociimage.ReadLayout(out)
	if err != nil {
		t.Fatal(err)
	}
	host := ocitest.Registry(t)
	pinned, err := ociimage.Push(ctx, img, host+"/chalkos/node-metal:v1")
	if want := host + "/chalkos/node-metal@" + d.String(); err != nil || pinned != want {
		t.Fatalf("Push = %s, %v; want %s", pinned, err, want)
	}
	a, err := ociimage.Read(ctx, host+"/chalkos/node-metal:v1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Reference != pinned || a.Header.RootHash != hex.EncodeToString(root) || a.Metadata.Variant != "node" || a.Metadata.Tag != "v1" {
		t.Errorf("Read = %+v", a)
	}
	if want, _ := ociimage.ReadContractDir(in); !reflect.DeepEqual(a.Contract, want) {
		t.Errorf("contract = %+v, want %+v", a.Contract, want)
	}
	storeLayer, err := ociimage.Layer(img, ociimage.StoreType)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := storeLayer.Digest()
	if err != nil {
		t.Fatal(err)
	}
	wantSize, err := storeLayer.Size()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := a.Descriptor(ociimage.StoreType)
	if err != nil || desc.Digest != wantDigest || desc.Size != wantSize {
		t.Errorf("Descriptor(StoreType) = %+v, %v; want digest %s, size %d", desc, err, wantDigest, wantSize)
	}
}

// TestReadRefuses refuses a reference without a tag, a container image, an index, an unknown
// repository, and an image whose contract's metadata.json names another root hash than its
// header.
func TestReadRefuses(t *testing.T) {
	ctx := context.Background()
	host := ocitest.Registry(t)
	ctr, _ := random.Image(64, 1)
	ctrRef, err := name.ParseReference(host + "/other/ctr:v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ctrRef, ctr); err != nil {
		t.Fatal(err)
	}
	idx, _ := random.Index(64, 1, 1)
	idxRef, err := name.ParseReference(host + "/other/idx:v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(idxRef, idx); err != nil {
		t.Fatal(err)
	}
	c := ocitest.Contract()
	c.Metadata = metadataOf(strings.Repeat("ab", 32))
	other := ocitest.Header()
	other.RootHash = strings.Repeat("cd", 32)
	ocitest.Push(t, ocitest.Image(t, other, c), host+"/chalkos/other:v1")
	for ref, want := range map[string]string{
		host + "/other/ctr":        "no image reference with a registry and a tag or digest",
		host + "/other/ctr:v1":     "it is no chalkos image",
		host + "/other/idx:v1":     "not an image manifest",
		host + "/other/none:v1":    "NAME_UNKNOWN",
		host + "/chalkos/other:v1": `the contract layer's metadata.json names the root hash "abab`,
	} {
		if _, err := ociimage.Read(ctx, ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Read(%s) = %v, want %q", ref, err, want)
		}
	}
}

// TestReadRefusesASwappedManifest refuses a manifest whose bytes do not match the pinned digest.
func TestReadRefusesASwappedManifest(t *testing.T) {
	c := ocitest.Contract()
	other := ocitest.Header()
	other.RootHash = strings.Repeat("cd", 32)
	host, digestA := swappingRegistry(t, ocitest.Image(t, ocitest.Header(), c), ocitest.Image(t, other, c))
	pinnedRef := host + "/chalkos/x@" + digestA.String()
	if _, err := ociimage.Read(context.Background(), pinnedRef); err == nil || !strings.Contains(err.Error(), "does not match requested digest") {
		t.Errorf("Read(%s) = %v, want a digest mismatch", pinnedRef, err)
	}
}
