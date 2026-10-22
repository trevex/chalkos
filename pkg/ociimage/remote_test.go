package ociimage_test

import (
	"context"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

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
	md := `{"schemaVersion": 0, "id": "chalkos", "variant": "node", "platform": "metal", "rootHash": "` + strings.Repeat("ab", 32) + `", "kinds": []}`
	c := ociimage.Contract{Metadata: []byte(md), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}}
	ocitest.Push(t, ocitest.Image(t, ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("cd", 32), Architecture: "x86-64", Platform: "metal"}, c), host+"/chalkos/other:v1")
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

// TestReadRefusesASwappedManifest serves a chalkos image through a proxy that answers a fetch of
// its pinned digest with a different chalkos image's manifest, bytes an attacker who controls (or
// sits in front of) a registry might substitute once a tag has already resolved to a digest.
// go-containerregistry hashes the manifest bytes it receives and refuses them when they do not
// match the digest the caller asked for, so Read of the pinned reference must fail rather than
// silently accept the swapped image.
func TestReadRefusesASwappedManifest(t *testing.T) {
	ctx := context.Background()
	c := ociimage.Contract{Metadata: []byte("{}"), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}}
	a := ocitest.Image(t, ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("ab", 32), Architecture: "x86-64", Platform: "metal"}, c)
	b := ocitest.Image(t, ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("cd", 32), Architecture: "x86-64", Platform: "metal"}, c)
	digestA, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	manifestB, err := b.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/manifests/"+digestA.String()) {
			w.Header().Set("Content-Type", string(types.OCIManifestSchema1))
			w.Write(manifestB)
			return
		}
		reg.ServeHTTP(w, r)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	tagRef, err := name.ParseReference(host + "/chalkos/x:v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(tagRef, a); err != nil {
		t.Fatal(err)
	}
	pinnedRef := host + "/chalkos/x@" + digestA.String()
	if _, err := ociimage.Read(ctx, pinnedRef); err == nil || !strings.Contains(err.Error(), "does not match requested digest") {
		t.Errorf("Read(%s) = %v, want a digest mismatch", pinnedRef, err)
	}
}
