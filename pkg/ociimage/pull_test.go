package ociimage_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/imagepart"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
)

// direct is the one source of a pull from the registry its reference names.
func direct(auth authn.Authenticator, insecure bool) []ociimage.Source {
	return []ociimage.Source{{Auth: auth, Insecure: insecure}}
}

// loopback is the one source of a pull from a test registry.
var loopback = direct(authn.Anonymous, true)

// viaMirror is the endpoint as a mirror, followed by the test registry the reference names.
func viaMirror(endpoint string) []ociimage.Source {
	return []ociimage.Source{{Endpoint: endpoint, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous, Insecure: true}}
}

// newPull is ociimage.NewPull that fails the test on an error and closes the pull at its end.
func newPull(t *testing.T, ref string, src []ociimage.Source, h ociimage.Header, parts []ociimage.Part) *ociimage.Pull {
	t.Helper()
	p, err := ociimage.NewPull(context.Background(), ref, src, h, parts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// allParts is what a pull of the parts testParts names reads.
const allParts = "storehash treeuki"

// wantRead reads p to its end and fails the test unless it reads want.
func wantRead(t *testing.T, p *ociimage.Pull, want string) {
	t.Helper()
	if got, err := io.ReadAll(p); err != nil || string(got) != want {
		t.Errorf("read %q, %v; want %q", got, err, want)
	}
}

// storeDigest is the digest of the store layer of ocitest.Image.
var storeDigest = func() string {
	sum := sha256.Sum256([]byte("store"))
	return "sha256:" + hex.EncodeToString(sum[:])
}()

// hang answers no request until it ends.
func hang(_ http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(10 * time.Second):
	}
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
		parts = append(parts, ociimage.Part{MediaType: p.mediaType, Part: imagepart.Part{Size: int64(len(p.data)), SHA256: sum[:]}})
	}
	return parts
}

// pushSmall pushes a small chalkos image to the registry at host and returns its reference by
// digest, its header, and the parts an upgrade pulls of it: store, hash tree and UKI.
func pushSmall(t *testing.T, host string) (string, ociimage.Header, []ociimage.Part) {
	t.Helper()
	h := ocitest.Header()
	img := ocitest.Image(t, h, ocitest.Contract())
	ref := ocitest.Push(t, img, host+"/lab/node-metal:v1")
	return ref, h, testParts()
}

// TestPull reads the parts of an image by digest, one after the other.
func TestPull(t *testing.T) {
	ref, h, parts := pushSmall(t, ocitest.Registry(t))
	wantRead(t, newPull(t, ref, loopback, h, parts), allParts)
}

// TestPullRefuses refuses a reference by tag, an image whose header or layers are not those
// wanted, and a manifest the registry does not hold, naming the reference.
func TestPullRefuses(t *testing.T) {
	host := ocitest.Registry(t)
	ref, h, parts := pushSmall(t, host)
	if _, err := ociimage.NewPull(context.Background(), host+"/lab/node-metal:v1", loopback, h, parts); err == nil || !strings.Contains(err.Error(), "is no image reference by digest") {
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
		{"another hash tree", ref, h, []ociimage.Part{parts[0], {MediaType: ociimage.HashTreeType, Part: imagepart.Part{Size: 9, SHA256: other[:]}}, parts[2]}, "its application/vnd.chalkos.image.hash-tree.v1 layer is sha256:"},
		{"an unknown digest", host + "/lab/node-metal@sha256:" + strings.Repeat("0", 64), h, parts, "MANIFEST_UNKNOWN"},
	} {
		_, err := io.ReadAll(newPull(t, tc.ref, loopback, tc.h, tc.parts))
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.ref) {
			t.Errorf("%s: %v, want %q and the reference", tc.name, err, tc.want)
		}
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
	host := ocitest.RegistryWith(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/manifests/"+digestA.String()) {
			w.Header().Set("Content-Type", string(types.OCIManifestSchema1))
			w.Write(manifestB)
			return true
		}
		return false
	})
	tagRef, err := name.ParseReference(host + "/chalkos/x:v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(tagRef, a); err != nil {
		t.Fatal(err)
	}
	return host, digestA
}

// TestPullWithCredentials pulls from a registry that asks for credentials with the user and
// password given, and is refused anonymously.
func TestPullWithCredentials(t *testing.T) {
	ref, h, parts := pushSmall(t, ocitest.AuthRegistry(t, "ci-bot", "s3cret"))
	if _, err := io.ReadAll(newPull(t, ref, loopback, h, parts)); err == nil || !strings.Contains(err.Error(), "UNAUTHORIZED") {
		t.Errorf("anonymously: %v", err)
	}
	wantRead(t, newPull(t, ref, direct(&authn.Basic{Username: "ci-bot", Password: "s3cret"}, true), h, parts), allParts)
}

// TestPullSkips passes over the store and hash tree without fetching them.
func TestPullSkips(t *testing.T) {
	host, served := ocitest.ServedBlobs(t)
	ref, h, parts := pushSmall(t, host)
	p := newPull(t, ref, loopback, h, parts)
	if err := p.Skip(parts[0].Size + parts[1].Size); err != nil {
		t.Fatal(err)
	}
	wantRead(t, p, "uki")
	if _, ok := served.Load(storeDigest); ok {
		t.Error("the store was fetched")
	}
}

// TestPullContextCanceled refuses to fetch the manifest once its context is canceled.
func TestPullContextCanceled(t *testing.T) {
	ref, h, parts := pushSmall(t, ocitest.Registry(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err := ociimage.NewPull(ctx, ref, loopback, h, parts)
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
	_, err := io.ReadAll(newPull(t, ref, direct(authn.Anonymous, false), h, parts))
	if err == nil || !strings.Contains(err.Error(), "plain HTTP to 127.0.0.1") || !strings.Contains(err.Error(), "insecure: true") || !strings.Contains(err.Error(), ref) {
		t.Errorf("over plain HTTP: %v", err)
	}
}

// blobServer is a registry that answers a GET of the blob of digest with serve.
func blobServer(t *testing.T, digest string, serve http.HandlerFunc) string {
	t.Helper()
	return ocitest.RegistryWith(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+digest) {
			serve(w, r)
			return true
		}
		return false
	})
}

// TestPullCancelsAStalledLayer cancels the fetch of a layer whose registry stops sending data
// halfway, naming the layer and the reference.
func TestPullCancelsAStalledLayer(t *testing.T) {
	ociimage.SetStallTimeout(t, 200*time.Millisecond)
	host := blobServer(t, storeDigest, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5")
		io.WriteString(w, "st")
		w.(http.Flusher).Flush()
		hang(w, r)
	})
	ref, h, parts := pushSmall(t, host)
	start := time.Now()
	_, err := io.ReadAll(newPull(t, ref, loopback, h, parts))
	want := "no data from the registry " + host + " for 200ms while pulling the application/vnd.chalkos.image.store.v1 layer of " + ref
	if err == nil || err.Error() != want {
		t.Errorf("stalled layer: %v, want %q", err, want)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the stalled layer was canceled after %v", d)
	}
}

// TestPullRefusesALongerLayer refuses a last layer that goes on past its part's size, before it
// reads to its end.
func TestPullRefusesALongerLayer(t *testing.T) {
	sum := sha256.Sum256([]byte("uki"))
	host := blobServer(t, "sha256:"+hex.EncodeToString(sum[:]), func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "uki"+strings.Repeat("x", 1<<16))
	})
	ref, h, parts := pushSmall(t, host)
	p := newPull(t, ref, loopback, h, parts)
	// An upgrade reads the UKI by its size alone, with io.CopyN, which drops the error of the
	// read that returns its last bytes; it reads once more to find the stream ended.
	_, err := io.CopyN(io.Discard, p, parts[0].Size+parts[1].Size+parts[2].Size)
	if err == nil {
		_, err = p.Read(make([]byte, 1))
	}
	if err == nil || !strings.Contains(err.Error(), "layer is longer than 3 bytes") || !strings.Contains(err.Error(), ref) {
		t.Errorf("longer layer: %v", err)
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
	h := ocitest.Header()
	c := ocitest.Contract()
	c.Metadata = metadataOf(h.RootHash)
	c.Facts = map[string][]byte{"kubernetes": []byte(`{"version": "1.37.1"}`)}
	ref := ocitest.Push(t, ocitest.Image(t, h, c), host+"/lab/node-metal:v1")
	loader := sha256.Sum256([]byte("boot loader"))
	parts := append(testParts(), ociimage.Part{MediaType: ociimage.BootLoaderType, Part: imagepart.Part{Size: int64(len("boot loader")), SHA256: loader[:]}})
	before := manifests.Load()
	p := newPull(t, ref, loopback, h, parts)
	if got, err := p.Contract(); err != nil || !reflect.DeepEqual(got, c) {
		t.Errorf("Contract = %+v, %v; want %+v", got, err, c)
	}
	wantRead(t, p, allParts+"boot loader")
	if n := manifests.Load() - before; n != 1 {
		t.Errorf("fetched %d manifests, want 1", n)
	}
}

// TestPullContractRefuses refuses an image whose header is not the one wanted, and one whose
// contract's metadata.json describes another image than its header, naming the reference. A read
// after a refused contract checks the manifest again.
func TestPullContractRefuses(t *testing.T) {
	host, manifests := ocitest.CountingRegistry(t)
	h := ocitest.Header()
	for name, tc := range map[string]struct{ metadataRoot, wantPlatform, want string }{
		"another-header":   {strings.Repeat("ab", 32), "kvm", "its header describes"},
		"another-metadata": {strings.Repeat("cd", 32), "metal", `the contract layer's metadata.json names the root hash "cdcd`},
	} {
		t.Run(name, func(t *testing.T) {
			c := ocitest.Contract()
			c.Metadata = metadataOf(tc.metadataRoot)
			ref := ocitest.Push(t, ocitest.Image(t, h, c), host+"/lab/"+name+":v1")
			want := h
			want.Platform = tc.wantPlatform
			p := newPull(t, ref, loopback, want, testParts())
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
	host, served := ocitest.ServedBlobs(t)
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
	wantRead(t, newPull(t, pinned, mirrors, h, parts), allParts)
	if _, ok := served.Load(storeDigest); !ok {
		t.Error("the store did not come from the mirror that holds it")
	}
	privRef := ocitest.Push(t, ocitest.Image(t, h, ocitest.Contract()), private+"/lab/node-metal:v1")
	wantRead(t, newPull(t, elsewhere(privRef, "registry.invalid"), []ociimage.Source{
		{Endpoint: "http://" + private, Auth: &authn.Basic{Username: "ci-bot", Password: "s3cret"}, Insecure: true},
		{Auth: authn.Anonymous},
	}, h, parts), allParts)
}

// TestPullThroughAPathPrefix reaches a mirror's registry API below the endpoint's path, with
// /v2 appended unless the path ends in it, as containerd does.
func TestPullThroughAPathPrefix(t *testing.T) {
	host := ocitest.Registry(t)
	ref, h, parts := pushSmall(t, host)
	for _, path := range []string{"/cache", "/cache/v2"} {
		mirror, used := prefixedRegistry(t, host, "/cache")
		wantRead(t, newPull(t, elsewhere(ref, "registry.invalid"), []ociimage.Source{
			{Endpoint: "http://" + mirror + path, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous}}, h, parts), allParts)
		if !used.Load() {
			t.Errorf("%s: the pull did not go below the path", path)
		}
	}
}

// TestPullFallsBackForALayer fetches a layer a mirror lacks from the next source, after the
// manifest came from the mirror.
func TestPullFallsBackForALayer(t *testing.T) {
	host, served := ocitest.ServedBlobs(t)
	ref, h, parts := pushSmall(t, host)
	mirror := blobServer(t, storeDigest, http.NotFound)
	pushSmall(t, mirror)
	wantRead(t, newPull(t, ref, viaMirror("http://"+mirror), h, parts), allParts)
	if _, ok := served.Load(storeDigest); !ok {
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
	_, err := io.ReadAll(newPull(t, ref, viaMirror("http://"+empty), h, parts))
	if err == nil || !strings.Contains(err.Error(), ref) || !strings.Contains(err.Error(), "MANIFEST_UNKNOWN") ||
		!strings.Contains(err.Error(), ", after the mirror http://"+empty+": ") {
		t.Errorf("pull = %v", err)
	}
}

// TestPullDoesNotPassOverAMismatch fails at a mirror that serves other bytes for the pinned
// digest, without asking the registry: the content is pinned.
func TestPullDoesNotPassOverAMismatch(t *testing.T) {
	h := ocitest.Header()
	other := h
	other.RootHash = strings.Repeat("cd", 32)
	c := ocitest.Contract()
	a := ocitest.Image(t, h, c)
	mirror, _ := swappingRegistry(t, a, ocitest.Image(t, other, c))
	host, manifests := ocitest.CountingRegistry(t)
	ref := ocitest.Push(t, a, host+"/chalkos/x:v1")
	before := manifests.Load()
	if _, err := io.ReadAll(newPull(t, ref, viaMirror("http://"+mirror), h, testParts())); err == nil || !strings.Contains(err.Error(), "does not match requested digest") {
		t.Errorf("swapped at the mirror: %v", err)
	}
	if manifests.Load() != before {
		t.Error("the registry was asked after the mirror's mismatch")
	}
}

// TestPullRefusesAPlainHTTPMirror refuses an http:// endpoint without Insecure, and
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
// is cut short or goes on past its size, without fetching the layer from the registry.
func TestPullDoesNotPassOverATamperedLayer(t *testing.T) {
	for name, tc := range map[string]struct{ serve, want string }{
		"tampered":  {"xtore", "checksum"},
		"truncated": {"sto", "checksum"},
		"longer":    {"store" + strings.Repeat("x", 1<<16), "layer is longer than 5 bytes"},
	} {
		host, served := ocitest.ServedBlobs(t)
		ref, h, parts := pushSmall(t, host)
		mirror := blobServer(t, storeDigest, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.serve) })
		pushSmall(t, mirror)
		if _, err := io.ReadAll(newPull(t, ref, viaMirror("http://"+mirror), h, parts)); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), ref) {
			t.Errorf("%s at the mirror: %v, want %q", name, err, tc.want)
		}
		if _, ok := served.Load(storeDigest); ok {
			t.Errorf("%s: the store was fetched from the registry after the mirror's", name)
		}
	}
}

// TestPullPassesOverAStalledMirror fetches a layer from the next source when a mirror sends no
// response for it within the stall timeout, and fails once the last source stalls too.
func TestPullPassesOverAStalledMirror(t *testing.T) {
	ociimage.SetStallTimeout(t, 200*time.Millisecond)
	host, served := ocitest.ServedBlobs(t)
	ref, h, parts := pushSmall(t, host)
	mirror := blobServer(t, storeDigest, hang)
	pushSmall(t, mirror)
	wantRead(t, newPull(t, ref, viaMirror("http://"+mirror), h, parts), allParts)
	if _, ok := served.Load(storeDigest); !ok {
		t.Error("the store did not come from the registry")
	}
	stalled := blobServer(t, storeDigest, hang)
	ref, _, _ = pushSmall(t, stalled)
	want := "no data from the registry " + stalled + " for 200ms while pulling the application/vnd.chalkos.image.store.v1 layer of " + ref + ", after the mirror http://" + mirror + ": no data from the mirror http://" + mirror + " for 200ms"
	if _, err := io.ReadAll(newPull(t, ref, viaMirror("http://"+mirror), h, parts)); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("stalled everywhere: %v, want %q", err, want)
	}
}

// tcpServer is an endpoint host that hands each connection it accepts to serve.
func tcpServer(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	return l.Addr().String()
}

// TestPullPassesOverAFailingMirror passes over an endpoint that closes the connection, one that
// answers HTTPS with plain HTTP, one that ends the TLS handshake with an alert, one that answers
// 503 and one that answers 429, and pulls from the registry.
func TestPullPassesOverAFailingMirror(t *testing.T) {
	ociimage.SetFastRetries(t)
	status := func(code int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		t.Cleanup(srv.Close)
		return strings.TrimPrefix(srv.URL, "http://")
	}
	closing := tcpServer(t, func(c net.Conn) { c.Close() })
	// A fatal certificate_required alert, as a server that requires client certificates sends.
	alert := tcpServer(t, func(c net.Conn) {
		defer c.Close()
		c.Read(make([]byte, 4096))
		c.Write([]byte{0x15, 0x03, 0x03, 0x00, 0x02, 0x02, 116})
	})
	ref, h, parts := pushSmall(t, ocitest.Registry(t))
	for name, endpoint := range map[string]ociimage.Source{
		"closed":     {Endpoint: "https://" + closing},
		"plain HTTP": {Endpoint: "https://" + ocitest.Registry(t)},
		"alert":      {Endpoint: "https://" + alert},
		"503":        {Endpoint: "http://" + status(http.StatusServiceUnavailable), Insecure: true},
		"429":        {Endpoint: "http://" + status(http.StatusTooManyRequests), Insecure: true},
	} {
		t.Run(name, func(t *testing.T) {
			endpoint.Auth = authn.Anonymous
			wantRead(t, newPull(t, ref, append([]ociimage.Source{endpoint}, loopback...), h, parts), allParts)
		})
	}
}

// TestPullPassesOverAHangingManifest asks the registry once a mirror does not answer for the
// manifest within the source timeout.
func TestPullPassesOverAHangingManifest(t *testing.T) {
	ociimage.SetSourceTimeout(t, 200*time.Millisecond)
	ref, h, parts := pushSmall(t, ocitest.Registry(t))
	mirror := ocitest.RegistryWith(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "/manifests/") && r.Method == http.MethodGet {
			hang(w, r)
			return true
		}
		return false
	})
	pushSmall(t, mirror)
	start := time.Now()
	wantRead(t, newPull(t, ref, viaMirror("http://"+mirror), h, parts), allParts)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the hanging mirror was passed over after %v", d)
	}
}

// TestPullPassesOverAMissingHeaderOrContract asks the registry for the image when a mirror serves
// its manifest but not its header, or, for Contract, not its contract layer.
func TestPullPassesOverAMissingHeaderOrContract(t *testing.T) {
	h := ocitest.Header()
	c := ocitest.Contract()
	c.Metadata = metadataOf(h.RootHash)
	img := ocitest.Image(t, h, c)
	config, err := img.ConfigName()
	if err != nil {
		t.Fatal(err)
	}
	m, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	var contract v1.Hash
	for _, l := range m.Layers {
		if l.MediaType == ociimage.ContractType {
			contract = l.Digest
		}
	}
	host, manifests := ocitest.CountingRegistry(t)
	ref := ocitest.Push(t, img, host+"/lab/node-metal:v1")
	for name, digest := range map[string]v1.Hash{"header": config, "contract": contract} {
		mirror := blobServer(t, digest.String(), http.NotFound)
		ocitest.Push(t, img, mirror+"/lab/node-metal:v1")
		before := manifests.Load()
		p := newPull(t, ref, viaMirror("http://"+mirror), h, testParts())
		if got, err := p.Contract(); err != nil || !reflect.DeepEqual(got, c) {
			t.Errorf("%s: Contract = %v", name, err)
		}
		wantRead(t, p, allParts)
		if manifests.Load() == before {
			t.Errorf("%s: the registry was not asked", name)
		}
	}
}

// TestPullCutsAPassedOverError keeps an error a mirror answers with to a few hundred bytes.
func TestPullCutsAPassedOverError(t *testing.T) {
	long := strings.Repeat("x", 10000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"errors": [{"code": "NAME_UNKNOWN", "message": "`+long+`"}]}`)
	}))
	t.Cleanup(srv.Close)
	host := ocitest.Registry(t)
	_, h, parts := pushSmall(t, host)
	ref := host + "/lab/node-metal@sha256:" + strings.Repeat("0", 64)
	_, err := io.ReadAll(newPull(t, ref, viaMirror(srv.URL), h, parts))
	if err == nil || !strings.Contains(err.Error(), "after the mirror "+srv.URL+": ") || !strings.HasSuffix(err.Error(), "…") || len(err.Error()) > 1024 {
		t.Errorf("pull = %.2000v (%d bytes)", err, len(fmt.Sprint(err)))
	}
}

// TestPullOverHTTPSToAnHTTPSMirror reaches an https:// endpoint over HTTPS alone, even on
// loopback and with Insecure set.
func TestPullOverHTTPSToAnHTTPSMirror(t *testing.T) {
	host, manifests := ocitest.CountingRegistry(t)
	ref, h, parts := pushSmall(t, host)
	before := manifests.Load()
	_, err := io.ReadAll(newPull(t, elsewhere(ref, "registry.invalid"), []ociimage.Source{
		{Endpoint: "https://" + host, Auth: authn.Anonymous, Insecure: true}, {Auth: authn.Anonymous}}, h, parts))
	if err == nil || !strings.Contains(err.Error(), "after the mirror https://"+host+": ") {
		t.Errorf("pull = %v", err)
	}
	if manifests.Load() != before {
		t.Error("the mirror was asked over plain HTTP")
	}
}

// timeoutError is an error that says it timed out, as the HTTP client's TLS handshake timeout
// does.
type timeoutError struct{}

func (timeoutError) Error() string { return "net/http: TLS handshake timeout" }
func (timeoutError) Timeout() bool { return true }

// TestPassable passes over a source for errors of reaching it and for statuses that say it
// cannot serve the image now, never for bytes that do not match or a body cut short.
func TestPassable(t *testing.T) {
	get := func(err error) error { return &url.Error{Op: "Get", URL: "https://mirror.example/v2/", Err: err} }
	for err, want := range map[error]bool{
		get(io.EOF):                 true,
		get(io.ErrUnexpectedEOF):    true,
		get(timeoutError{}):         true,
		get(http.ErrSchemeMismatch): true,
		get(&net.OpError{Op: "remote error", Err: tls.AlertError(116)}): true,
		get(tls.AlertError(116)):                      true,
		&transport.Error{StatusCode: 404}:             true,
		&transport.Error{StatusCode: 429}:             true,
		&transport.Error{StatusCode: 502}:             true,
		&transport.Error{StatusCode: 400}:             false,
		io.ErrUnexpectedEOF:                           false,
		errors.New("error verifying sha256 checksum"): false,
		fmt.Errorf("the contract layer: %w", io.EOF):  false,
	} {
		if got := ociimage.Passable(err); got != want {
			t.Errorf("passable(%v) = %v, want %v", err, got, want)
		}
	}
}
