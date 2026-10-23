package ociimage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
)

// direct is the one source of a pull from the registry its reference names.
func direct(auth authn.Authenticator, insecure bool) []ociimage.Source {
	return []ociimage.Source{{Auth: auth, Insecure: insecure}}
}

// elsewhere is ref with its registry host replaced by host.
func elsewhere(ref, host string) string {
	_, rest, _ := strings.Cut(ref, "/")
	return host + "/" + rest
}

// prefixedRegistry serves the registry at host below the path prefix alone, as a mirror whose
// endpoint has a path does, and records whether a request came below it.
func prefixedRegistry(t *testing.T, host, prefix string) (string, *atomic.Bool) {
	t.Helper()
	var used atomic.Bool
	target, _ := url.Parse("http://" + host)
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		used.Store(true)
		r.URL.Path = rest
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &used
}

// testParts is the parts an upgrade pulls of an image ocitest.Image builds: store, hash tree and
// UKI, each a few bytes.
func testParts() []ociimage.Part {
	var parts []ociimage.Part
	for _, p := range []struct {
		mediaType types.MediaType
		data      string
	}{{ociimage.StoreType, "store"}, {ociimage.HashTreeType, "hash tree"}, {ociimage.UKIType, "uki"}} {
		sum := sha256.Sum256([]byte(p.data))
		parts = append(parts, ociimage.Part{MediaType: p.mediaType, Size: int64(len(p.data)), SHA256: sum[:]})
	}
	return parts
}

// pushSmall pushes a small chalkos image to the registry at host and returns its reference by
// digest, its header, and the parts an upgrade pulls of it: store, hash tree and UKI.
func pushSmall(t *testing.T, host string) (string, ociimage.Header, []ociimage.Part) {
	t.Helper()
	h := ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("ab", 32), Architecture: "x86-64", Platform: "metal"}
	img := ocitest.Image(t, h, ociimage.Contract{Metadata: []byte("{}"), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}})
	ref := ocitest.Push(t, img, host+"/lab/node-metal:v1")
	return ref, h, testParts()
}

// servedBlobs is a registry from memory that records the digests of the blobs it serves.
func servedBlobs(t *testing.T) (string, *sync.Map) {
	t.Helper()
	var served sync.Map
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, digest, ok := strings.Cut(r.URL.Path, "/blobs/"); ok && r.Method == http.MethodGet {
			served.Store(digest, true)
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &served
}

// TestPull reads the parts of an image by digest, one after the other.
func TestPull(t *testing.T) {
	ref, h, parts := pushSmall(t, ocitest.Registry(t))
	p, err := ociimage.NewPull(context.Background(), ref, direct(authn.Anonymous, true), h, parts)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	got, err := io.ReadAll(p)
	if err != nil || string(got) != "storehash treeuki" {
		t.Errorf("read %q, %v", got, err)
	}
}

// TestPullRefuses refuses a reference by tag, an image whose header or layers are not those
// wanted, and a manifest the registry does not hold, naming the reference.
func TestPullRefuses(t *testing.T) {
	host := ocitest.Registry(t)
	ref, h, parts := pushSmall(t, host)
	if _, err := ociimage.NewPull(context.Background(), host+"/lab/node-metal:v1", direct(authn.Anonymous, true), h, parts); err == nil || !strings.Contains(err.Error(), "is no image reference by digest") {
		t.Errorf("by tag: %v", err)
	}
	other := sha256.Sum256([]byte("other"))
	kvm := h
	kvm.Platform = "kvm"
	for _, tc := range []struct {
		name  string
		ref   string
		h     ociimage.Header
		parts []ociimage.Part
		want  string
	}{
		{"another header", ref, kvm, parts, "its header describes"},
		{"another hash tree", ref, h, []ociimage.Part{parts[0], {MediaType: ociimage.HashTreeType, Size: 9, SHA256: other[:]}, parts[2]}, "its application/vnd.chalkos.image.hash-tree.v1 layer is sha256:"},
		{"an unknown digest", host + "/lab/node-metal@sha256:" + strings.Repeat("0", 64), h, parts, "MANIFEST_UNKNOWN"},
	} {
		p, err := ociimage.NewPull(context.Background(), tc.ref, direct(authn.Anonymous, true), tc.h, tc.parts)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(p)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.ref) {
			t.Errorf("%s: %v, want %q and the reference", tc.name, err, tc.want)
		}
	}
}

// TestPullRefusesASwappedManifest serves a chalkos image through a proxy that answers a fetch of
// its pinned digest with a different chalkos image's manifest, bytes an attacker who controls (or
// sits in front of) a registry might substitute once a tag has already resolved to a digest.
// go-containerregistry hashes the manifest bytes it receives and refuses them when they do not
// match the digest the caller asked for, so a Pull of the pinned reference must fail rather than
// silently stream the swapped image's layers.
func TestPullRefusesASwappedManifest(t *testing.T) {
	ctx := context.Background()
	h := ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("ab", 32), Architecture: "x86-64", Platform: "metal"}
	other := ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("cd", 32), Architecture: "x86-64", Platform: "metal"}
	c := ociimage.Contract{Metadata: []byte("{}"), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}}
	host, digestA := swappingRegistry(t, ocitest.Image(t, h, c), ocitest.Image(t, other, c))
	pinnedRef := host + "/chalkos/x@" + digestA.String()
	p, err := ociimage.NewPull(ctx, pinnedRef, direct(authn.Anonymous, true), h, testParts())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(p); err == nil || !strings.Contains(err.Error(), "does not match requested digest") {
		t.Errorf("swapped manifest: %v, want a digest mismatch", err)
	}
}

// swappingRegistry is a registry that holds a, as chalkos/x:v1, and answers a GET of a's manifest
// with b's bytes. It returns its host and a's digest.
func swappingRegistry(t *testing.T, a, b v1.Image) (string, v1.Hash) {
	t.Helper()
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
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	tagRef, err := name.ParseReference(host + "/chalkos/x:v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(tagRef, a); err != nil {
		t.Fatal(err)
	}
	return host, digestA
}

// TestPullRefusesATamperedLayer serves a chalkos image through a proxy that answers a fetch of
// its store layer's blob with bytes other than those its digest names, truncated or tampered,
// bytes an attacker who controls (or sits in front of) a registry might substitute after the
// manifest has already been checked against the header and parts wanted. go-containerregistry
// hashes every byte of a blob it streams and fails once it has read to the blob's end if the hash
// does not match the digest asked for, so Read must surface that error rather than hand the
// caller bytes that do not match the pulled image.
func TestPullRefusesATamperedLayer(t *testing.T) {
	store := sha256.Sum256([]byte("store"))
	blobPath := "/blobs/sha256:" + hex.EncodeToString(store[:])
	for _, tc := range []struct {
		name  string
		serve string
	}{
		{"tampered", "xtore"}, // same length as "store", different bytes
		{"truncated", "sto"},  // shorter than "store"
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, blobPath) {
					io.WriteString(w, tc.serve)
					return
				}
				reg.ServeHTTP(w, r)
			}))
			defer srv.Close()
			host := strings.TrimPrefix(srv.URL, "http://")
			ref, h, parts := pushSmall(t, host)
			p, err := ociimage.NewPull(context.Background(), ref, direct(authn.Anonymous, true), h, parts)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(p)
			if err == nil {
				t.Fatalf("read %q, want an error before the layer's end", got)
			}
			if !strings.Contains(err.Error(), "checksum") {
				t.Errorf("%s layer: %v, want a checksum mismatch", tc.name, err)
			}
		})
	}
}

// TestPullWithCredentials pulls from a registry that asks for credentials with the user and
// password given, and is refused anonymously.
func TestPullWithCredentials(t *testing.T) {
	ref, h, parts := pushSmall(t, ocitest.AuthRegistry(t, "ci-bot", "s3cret"))
	p, err := ociimage.NewPull(context.Background(), ref, direct(authn.Anonymous, true), h, parts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(p); err == nil || !strings.Contains(err.Error(), "UNAUTHORIZED") {
		t.Errorf("anonymously: %v", err)
	}
	p, err = ociimage.NewPull(context.Background(), ref, direct(&authn.Basic{Username: "ci-bot", Password: "s3cret"}, true), h, parts)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(p); err != nil || string(got) != "storehash treeuki" {
		t.Errorf("with credentials: %q, %v", got, err)
	}
}

// TestPullSkips passes over the store and hash tree without fetching them.
func TestPullSkips(t *testing.T) {
	host, served := servedBlobs(t)
	ref, h, parts := pushSmall(t, host)
	p, err := ociimage.NewPull(context.Background(), ref, direct(authn.Anonymous, true), h, parts)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Skip(parts[0].Size + parts[1].Size); err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(p); err != nil || string(got) != "uki" {
		t.Errorf("after the skip: %q, %v", got, err)
	}
	store := sha256.Sum256([]byte("store"))
	if _, ok := served.Load("sha256:" + hex.EncodeToString(store[:])); ok {
		t.Error("the store was fetched")
	}
}

// TestPullContextCanceled refuses to fetch the manifest once its context is canceled, rather than
// hang or silently succeed.
func TestPullContextCanceled(t *testing.T) {
	ref, h, parts := pushSmall(t, ocitest.Registry(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err := ociimage.NewPull(ctx, ref, direct(authn.Anonymous, true), h, parts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(p); err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context: %v, want context.Canceled", err)
	}
}

// TestPullOverHTTPSAlone refuses plain HTTP to a registry, even a loopback one, unless the pull
// allows it, and names the setting that does.
func TestPullOverHTTPSAlone(t *testing.T) {
	ref, h, parts := pushSmall(t, ocitest.Registry(t))
	p, err := ociimage.NewPull(context.Background(), ref, direct(authn.Anonymous, false), h, parts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(p); err == nil || !strings.Contains(err.Error(), "plain HTTP to 127.0.0.1") || !strings.Contains(err.Error(), "insecure: true") || !strings.Contains(err.Error(), ref) {
		t.Errorf("over plain HTTP: %v", err)
	}
}

// blobServer is a registry that answers a GET of the blob of data with serve.
func blobServer(t *testing.T, data string, serve http.HandlerFunc) string {
	t.Helper()
	sum := sha256.Sum256([]byte(data))
	blobPath := "/blobs/sha256:" + hex.EncodeToString(sum[:])
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, blobPath) {
			serve(w, r)
			return
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// TestPullCancelsAStalledLayer cancels the fetch of a layer whose registry stops sending data
// halfway, naming the layer and the reference.
func TestPullCancelsAStalledLayer(t *testing.T) {
	ociimage.SetStallTimeout(t, 200*time.Millisecond)
	host := blobServer(t, "store", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5")
		io.WriteString(w, "st")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	ref, h, parts := pushSmall(t, host)
	p, err := ociimage.NewPull(context.Background(), ref, direct(authn.Anonymous, true), h, parts)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	start := time.Now()
	_, err = io.ReadAll(p)
	want := "no data from the registry for 200ms while pulling the application/vnd.chalkos.image.store.v1 layer of " + ref
	if err == nil || err.Error() != want {
		t.Errorf("stalled layer: %v, want %q", err, want)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the stalled layer was canceled after %v", d)
	}
}

// TestPullRefusesALongerLayer refuses a layer that goes on past its part's size, before it reads
// to its end.
func TestPullRefusesALongerLayer(t *testing.T) {
	for _, data := range []string{"store", "uki"} {
		t.Run(data, func(t *testing.T) {
			host := blobServer(t, data, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, data+strings.Repeat("x", 1<<16))
			})
			ref, h, parts := pushSmall(t, host)
			p, err := ociimage.NewPull(context.Background(), ref, direct(authn.Anonymous, true), h, parts)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			// An upgrade reads the UKI by its size alone, with io.CopyN, which drops the error of
			// the read that returns its last bytes; it reads once more to find the stream ended.
			_, err = io.CopyN(io.Discard, p, parts[0].Size+parts[1].Size+parts[2].Size)
			if err == nil {
				_, err = p.Read(make([]byte, 1))
			}
			if err == nil || !strings.Contains(err.Error(), "layer is longer than "+fmt.Sprint(len(data))+" bytes") || !strings.Contains(err.Error(), ref) {
				t.Errorf("longer layer: %v", err)
			}
		})
	}
}

// metadataOf is the metadata.json of a node image on metal of the root hash.
func metadataOf(rootHash string) []byte {
	return []byte(`{"schemaVersion": 0, "id": "chalkos", "variant": "node", "platform": "metal", "rootHash": "` + rootHash + `", "kinds": []}`)
}

// TestPullContract fetches the manifest, the header and the contract layer before any part, and
// then reads the parts, the boot loader last as an install pulls it, without fetching the
// manifest again.
func TestPullContract(t *testing.T) {
	host, manifests := ocitest.CountingRegistry(t)
	h := ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("ab", 32), Architecture: "x86-64", Platform: "metal"}
	c := ociimage.Contract{
		Metadata: metadataOf(h.RootHash), Schemas: map[string][]byte{"a.json": []byte("{}")},
		Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}, ImageJSON: []byte(`{"version": "1.37.1"}`),
	}
	ref := ocitest.Push(t, ocitest.Image(t, h, c), host+"/lab/node-metal:v1")
	loader := sha256.Sum256([]byte("boot loader"))
	parts := append(testParts(), ociimage.Part{MediaType: ociimage.BootLoaderType, Size: int64(len("boot loader")), SHA256: loader[:]})
	before := manifests.Load()
	p, err := ociimage.NewPull(context.Background(), ref, direct(authn.Anonymous, true), h, parts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Contract()
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Errorf("Contract = %+v, %v; want %+v", got, err, c)
	}
	if all, err := io.ReadAll(p); err != nil || string(all) != "storehash treeukiboot loader" {
		t.Errorf("the parts = %q, %v", all, err)
	}
	if n := manifests.Load() - before; n != 1 {
		t.Errorf("fetched %d manifests, want 1", n)
	}
}

// TestPullContractRefuses refuses an image whose header is not the one wanted, and one whose
// contract's metadata.json describes another image than its header, naming the reference. A read
// after a refused contract checks the manifest again.
func TestPullContractRefuses(t *testing.T) {
	host, manifests := ocitest.CountingRegistry(t)
	h := ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("ab", 32), Architecture: "x86-64", Platform: "metal"}
	for name, tc := range map[string]struct{ metadataRoot, wantPlatform, want string }{
		"another-header":   {strings.Repeat("ab", 32), "kvm", "its header describes"},
		"another-metadata": {strings.Repeat("cd", 32), "metal", `the contract layer's metadata.json names the root hash "cdcd`},
	} {
		t.Run(name, func(t *testing.T) {
			c := ociimage.Contract{Metadata: metadataOf(tc.metadataRoot), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}}
			ref := ocitest.Push(t, ocitest.Image(t, h, c), host+"/lab/"+name+":v1")
			want := h
			want.Platform = tc.wantPlatform
			p, err := ociimage.NewPull(context.Background(), ref, direct(authn.Anonymous, true), want, testParts())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Contract(); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), ref) {
				t.Errorf("Contract = %v, want an error naming %s and %q", err, ref, tc.want)
			}
			before := manifests.Load()
			p.Read(make([]byte, 1))
			if n := manifests.Load() - before; n != 1 {
				t.Errorf("the read after the refused contract fetched %d manifests, want 1", n)
			}
		})
	}
}

// TestPullThroughMirrors pulls through the first endpoint that serves the image: past one that
// refuses connections, one without the image and one that refuses anonymous pulls, never
// reaching the registry the reference names, and with each endpoint's own credentials.
func TestPullThroughMirrors(t *testing.T) {
	host, served := servedBlobs(t)
	ref, h, parts := pushSmall(t, host)
	empty := ocitest.Registry(t)
	private := ocitest.AuthRegistry(t, "ci-bot", "s3cret")
	pinned := elsewhere(ref, "registry.invalid")
	mirrors := []ociimage.Source{
		{Endpoint: "http://127.0.0.1:1", Auth: authn.Anonymous, Insecure: true},
		{Endpoint: "http://" + empty, Auth: authn.Anonymous, Insecure: true},
		{Endpoint: "http://" + private, Auth: authn.Anonymous, Insecure: true},
		{Endpoint: "http://" + host, Auth: authn.Anonymous, Insecure: true},
		{Auth: authn.Anonymous},
	}
	p, err := ociimage.NewPull(context.Background(), pinned, mirrors, h, parts)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(p); err != nil || string(got) != "storehash treeuki" {
		t.Fatalf("read %q, %v", got, err)
	}
	store := sha256.Sum256([]byte("store"))
	if _, ok := served.Load("sha256:" + hex.EncodeToString(store[:])); !ok {
		t.Error("the store did not come from the mirror that holds it")
	}
	privRef := ocitest.Push(t, ocitest.Image(t, h, ociimage.Contract{Metadata: []byte("{}"), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}}), private+"/lab/node-metal:v1")
	p, err = ociimage.NewPull(context.Background(), elsewhere(privRef, "registry.invalid"), []ociimage.Source{
		{Endpoint: "http://" + private, Auth: &authn.Basic{Username: "ci-bot", Password: "s3cret"}, Insecure: true},
		{Auth: authn.Anonymous},
	}, h, parts)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(p); err != nil || string(got) != "storehash treeuki" {
		t.Errorf("with the endpoint's credentials: %q, %v", got, err)
	}
}

// TestPullThroughAPathPrefix reaches a mirror's registry API below the endpoint's path, with
// /v2 appended unless the path ends in it, as containerd does.
func TestPullThroughAPathPrefix(t *testing.T) {
	host := ocitest.Registry(t)
	ref, h, parts := pushSmall(t, host)
	for _, path := range []string{"/cache", "/cache/v2"} {
		mirror, used := prefixedRegistry(t, host, "/cache")
		p, err := ociimage.NewPull(context.Background(), elsewhere(ref, "registry.invalid"), []ociimage.Source{
			{Endpoint: "http://" + mirror + path, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous}}, h, parts)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := io.ReadAll(p); err != nil || string(got) != "storehash treeuki" || !used.Load() {
			t.Errorf("%s: read %q, %v, below the path: %v", path, got, err, used.Load())
		}
	}
}

// TestPullFallsBackForALayer fetches a layer a mirror lacks from the next source, after the
// manifest came from the mirror.
func TestPullFallsBackForALayer(t *testing.T) {
	host, served := servedBlobs(t)
	ref, h, parts := pushSmall(t, host)
	store := sha256.Sum256([]byte("store"))
	mirror := blobServer(t, "store", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	ocitest.Push(t, ocitest.Image(t, h, ociimage.Contract{Metadata: []byte("{}"), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}}), mirror+"/lab/node-metal:v1")
	p, err := ociimage.NewPull(context.Background(), ref, []ociimage.Source{
		{Endpoint: "http://" + mirror, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous, Insecure: true}}, h, parts)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(p); err != nil || string(got) != "storehash treeuki" {
		t.Fatalf("read %q, %v", got, err)
	}
	if _, ok := served.Load("sha256:" + hex.EncodeToString(store[:])); !ok {
		t.Error("the store did not come from the registry")
	}
}

// TestPullFailsAtTheLastSource names the error of the registry and, after it, why each mirror
// was passed over.
func TestPullFailsAtTheLastSource(t *testing.T) {
	host := ocitest.Registry(t)
	_, h, parts := pushSmall(t, host)
	ref := host + "/lab/node-metal@sha256:" + strings.Repeat("0", 64)
	empty := ocitest.Registry(t)
	p, err := ociimage.NewPull(context.Background(), ref, []ociimage.Source{
		{Endpoint: "http://" + empty, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous, Insecure: true}}, h, parts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(p)
	if err == nil || !strings.Contains(err.Error(), ref) || !strings.Contains(err.Error(), "MANIFEST_UNKNOWN") ||
		!strings.Contains(err.Error(), ", after the mirror http://"+empty+": ") {
		t.Errorf("pull = %v", err)
	}
}

// TestPullDoesNotPassOverAMismatch fails at a mirror that serves other bytes for the pinned
// digest, without asking the registry: the content is pinned.
func TestPullDoesNotPassOverAMismatch(t *testing.T) {
	h := ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("ab", 32), Architecture: "x86-64", Platform: "metal"}
	other := h
	other.RootHash = strings.Repeat("cd", 32)
	c := ociimage.Contract{Metadata: []byte("{}"), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}}
	a := ocitest.Image(t, h, c)
	mirror, _ := swappingRegistry(t, a, ocitest.Image(t, other, c))
	host, manifests := ocitest.CountingRegistry(t)
	ref := ocitest.Push(t, a, host+"/chalkos/x:v1")
	before := manifests.Load()
	p, err := ociimage.NewPull(context.Background(), ref, []ociimage.Source{
		{Endpoint: "http://" + mirror, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous, Insecure: true}}, h, testParts())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(p); err == nil || !strings.Contains(err.Error(), "does not match requested digest") {
		t.Errorf("swapped at the mirror: %v", err)
	}
	if manifests.Load() != before {
		t.Error("the registry was asked after the mirror's mismatch")
	}
}

// TestPullRefusesAPlainHTTPMirror refuses an http:// endpoint without leave for plain HTTP, and
// an endpoint that is no URL.
func TestPullRefusesAPlainHTTPMirror(t *testing.T) {
	ref, h, parts := pushSmall(t, ocitest.Registry(t))
	for endpoint, want := range map[string]string{
		"http://10.0.0.5:5000": "the mirror http://10.0.0.5:5000 is plain HTTP; set insecure: true in the Registry of 10.0.0.5:5000",
		"mirror.lan":           `the mirror "mirror.lan" is no http:// or https:// URL`,
	} {
		if _, err := ociimage.NewPull(context.Background(), ref, []ociimage.Source{{Endpoint: endpoint, Auth: authn.Anonymous}, {Auth: authn.Anonymous}}, h, parts); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", endpoint, err, want)
		}
	}
}

// TestPullDoesNotPassOverATamperedLayer fails at a mirror whose layer does not match its digest,
// or goes on past its size, without fetching the layer from the registry.
func TestPullDoesNotPassOverATamperedLayer(t *testing.T) {
	store := sha256.Sum256([]byte("store"))
	for want, serve := range map[string]string{"checksum": "xtore", "layer is longer than 5 bytes": "store" + strings.Repeat("x", 1<<16)} {
		host, served := servedBlobs(t)
		ref, h, parts := pushSmall(t, host)
		mirror := blobServer(t, "store", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, serve) })
		pushSmall(t, mirror)
		p, err := ociimage.NewPull(context.Background(), ref, []ociimage.Source{
			{Endpoint: "http://" + mirror, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous, Insecure: true}}, h, parts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(p); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), ref) {
			t.Errorf("tampered at the mirror: %v, want %q", err, want)
		}
		if _, ok := served.Load("sha256:" + hex.EncodeToString(store[:])); ok {
			t.Error("the store was fetched from the registry after the mirror's tampered one")
		}
	}
}

// TestPullPassesOverAStalledMirror fetches a layer from the next source when a mirror sends no
// response for it within the stall timeout, and fails once the last source stalls too.
func TestPullPassesOverAStalledMirror(t *testing.T) {
	ociimage.SetStallTimeout(t, 200*time.Millisecond)
	hang := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}
	host, served := servedBlobs(t)
	ref, h, parts := pushSmall(t, host)
	mirror := blobServer(t, "store", hang)
	pushSmall(t, mirror)
	p, err := ociimage.NewPull(context.Background(), ref, []ociimage.Source{
		{Endpoint: "http://" + mirror, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous, Insecure: true}}, h, parts)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(p); err != nil || string(got) != "storehash treeuki" {
		t.Fatalf("read %q, %v", got, err)
	}
	store := sha256.Sum256([]byte("store"))
	if _, ok := served.Load("sha256:" + hex.EncodeToString(store[:])); !ok {
		t.Error("the store did not come from the registry")
	}
	stalled := blobServer(t, "store", hang)
	ref, _, _ = pushSmall(t, stalled)
	p, err = ociimage.NewPull(context.Background(), ref, []ociimage.Source{
		{Endpoint: "http://" + mirror, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous, Insecure: true}}, h, parts)
	if err != nil {
		t.Fatal(err)
	}
	want := "no data from the registry for 200ms while pulling the application/vnd.chalkos.image.store.v1 layer of " + ref + ", after the mirror http://" + mirror + ": no data"
	if _, err := io.ReadAll(p); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("stalled everywhere: %v, want %q", err, want)
	}
}
