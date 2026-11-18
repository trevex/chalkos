package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
	"github.com/trevex/chalkos/pkg/registry/registrytest"
)

func init() {
	// No line for every request a test makes that the registry refuses.
	registry.QuietLogs()
}

// testRegistry is a registry and a client of each kind: "anyone" presents no certificate,
// "admin" and "reader" client certificates of those roles, "node" the registry's own node
// certificate, and "node admin" a certificate of the node CA whose Organization says admin.
// "e2e" is anyone with Basic credentials, as containerd sends a host's Registry's.
type testRegistry struct {
	*registrytest.Registry
	osCA    pki.CertKey
	clients map[string]http.RoundTripper
}

func startRegistry(t *testing.T) *testRegistry { return startWrapped(t, nil) }

// startWrapped is startRegistry with the handler wrapped by wrap.
func startWrapped(t *testing.T, wrap func(http.Handler) http.Handler) *testRegistry {
	t.Helper()
	now := time.Now()
	osCA, err := pki.NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	reg := registrytest.Start(t, osCA, false, wrap)
	r := &testRegistry{Registry: reg, osCA: osCA, clients: map[string]http.RoundTripper{"anyone": reg.Transport(t, nil)}}
	for _, role := range []pki.Role{pki.RoleAdmin, pki.RoleReader} {
		c, err := pki.IssueClient(osCA, "client-"+string(role), role, time.Hour, now)
		if err != nil {
			t.Fatal(err)
		}
		r.clients[string(role)] = reg.Transport(t, &c)
	}
	node, err := pki.IssueNode(reg.NodeCA, pki.NodeNames{CommonName: "n1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	r.clients["node"] = reg.Transport(t, &node)
	nodeAdmin, err := pki.IssueLeaf(reg.NodeCA, pki.Leaf{CommonName: "n2", Organization: []string{string(pki.RoleAdmin)}, Client: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	nodeAdmin.Certificate += reg.NodeCA.Certificate
	r.clients["node admin"] = reg.Transport(t, &nodeAdmin)
	return r
}

// ref is the reference of path in the registry.
func (r *testRegistry) ref(t *testing.T, path string) name.Reference {
	t.Helper()
	ref, err := name.ParseReference(r.Host + "/" + path)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// status sends one request as who and returns the answer's status.
func (r *testRegistry) status(t *testing.T, method, path, who string) int {
	t.Helper()
	return r.do(t, method, path, who, nil)
}

// do sends one request as who, decodes a JSON answer into out unless it is nil, and returns the
// answer's status.
func (r *testRegistry) do(t *testing.T, method, path, who string, out any) int {
	t.Helper()
	req, err := http.NewRequest(method, "https://"+r.Host+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
	client := r.clients[who]
	if who == "e2e" {
		client = r.clients["anyone"]
		req.SetBasicAuth("e2e", "s3cret")
	}
	resp, err := client.RoundTrip(req)
	if err != nil {
		t.Fatalf("%s %s as %s: %v", method, path, who, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s as %s: %v", method, path, who, err)
		}
	}
	return resp.StatusCode
}

// TestRegistry serves anyone's reads, also with Basic credentials, which containerd sends for a
// host's Registry, and the admin's writes and deletes; a reader, a node's certificate, one of the
// node CA that claims the admin role, and a client without one cannot write.
func TestRegistry(t *testing.T) {
	r := startRegistry(t)
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	ref := r.ref(t, "docker.io/library/busybox:1.37.0")
	for _, who := range []string{"anyone", "reader", "node", "node admin"} {
		err := remote.Write(ref, img, remote.WithTransport(r.clients[who]))
		if err == nil || !strings.Contains(err.Error(), "admin role") {
			t.Errorf("%s pushed, or was refused for another reason: %v", who, err)
		}
	}
	for who, want := range map[string]int{"anyone": http.StatusUnauthorized, "reader": http.StatusForbidden, "node": http.StatusForbidden, "node admin": http.StatusForbidden, "e2e": http.StatusUnauthorized} {
		if got := r.status(t, http.MethodPost, "/v2/docker.io/library/busybox/blobs/uploads/", who); got != want {
			t.Errorf("an upload as %s: %d, want %d", who, got, want)
		}
	}
	if err := remote.Write(ref, img, remote.WithTransport(r.clients["admin"])); err != nil {
		t.Fatalf("the admin's push: %v", err)
	}
	want, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range []authn.Authenticator{authn.Anonymous, &authn.Basic{Username: "e2e", Password: "s3cret"}} {
		got, err := remote.Image(ref, remote.WithTransport(r.clients["anyone"]), remote.WithAuth(auth))
		if err != nil {
			t.Fatalf("a pull with %T: %v", auth, err)
		}
		if d, _ := got.Digest(); d != want {
			t.Errorf("a pull with %T: %s, want %s", auth, d, want)
		}
	}
	// A request with credentials up front, as containerd sends them once it has them.
	if got := r.status(t, http.MethodGet, "/v2/docker.io/library/busybox/manifests/1.37.0", "e2e"); got != http.StatusOK {
		t.Errorf("a read with Basic credentials: %d, want 200", got)
	}
	repos, err := remote.Catalog(context.Background(), ref.Context().Registry, remote.WithTransport(r.clients["anyone"]))
	if err != nil || !slices.Equal(repos, []string{"docker.io/library/busybox"}) {
		t.Errorf("the catalog: %v, %v", repos, err)
	}
	if err := remote.Delete(ref, remote.WithTransport(r.clients["reader"])); err == nil {
		t.Error("a reader deleted")
	}
	if err := remote.Delete(ref, remote.WithTransport(r.clients["admin"])); err != nil {
		t.Fatalf("the admin's delete of the tag: %v", err)
	}
	if _, err := remote.Head(ref, remote.WithTransport(r.clients["anyone"])); err == nil {
		t.Error("the deleted tag still resolves")
	}
	byDigest := r.ref(t, "docker.io/library/busybox@"+want.String())
	if _, err := remote.Head(byDigest, remote.WithTransport(r.clients["anyone"])); err != nil {
		t.Errorf("the manifest of the deleted tag: %v", err)
	}
	if err := remote.Delete(byDigest, remote.WithTransport(r.clients["admin"])); err != nil {
		t.Errorf("the admin's delete by digest: %v", err)
	}
	if _, err := remote.Head(byDigest, remote.WithTransport(r.clients["anyone"])); err == nil {
		t.Error("the deleted manifest still resolves")
	}
}

// TestCollect frees the blobs of a deleted image, keeps an untagged manifest that was never
// deleted, refuses writes while it runs and a second collection, and a push after it uploads
// the freed blobs again.
func TestCollect(t *testing.T) {
	r := startRegistry(t)
	admin, anyone := remote.WithTransport(r.clients["admin"]), remote.WithTransport(r.clients["anyone"])
	kept, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := random.Image(4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	keptDigest, _ := kept.Digest()
	untagged := r.ref(t, "docker.io/library/kept@"+keptDigest.String())
	if err := remote.Write(untagged, kept, admin); err != nil {
		t.Fatal(err)
	}
	ref := r.ref(t, "docker.io/library/gone:1")
	if err := remote.Write(ref, gone, admin); err != nil {
		t.Fatal(err)
	}
	goneDigest, _ := gone.Digest()
	if err := remote.Delete(r.ref(t, "docker.io/library/gone@"+goneDigest.String()), admin); err != nil {
		t.Fatal(err)
	}
	freed, err := r.Collect(context.Background())
	if err != nil || freed < 4096*2 {
		t.Fatalf("Collect = %d, %v; want the deleted image's layers freed", freed, err)
	}
	if _, err := remote.Image(untagged, anyone); err != nil {
		t.Errorf("an untagged manifest is gone: %v", err)
	}
	if freed, err := r.Collect(context.Background()); err != nil || freed != 0 {
		t.Errorf("a collection with nothing to collect: %d, %v", freed, err)
	}
	// While a collection holds the lock, writes are refused and reads go on.
	release := r.HoldCollection()
	if got := r.status(t, http.MethodPost, "/v2/docker.io/library/gone/blobs/uploads/", "admin"); got != http.StatusServiceUnavailable {
		t.Errorf("a write during a collection: %d, want 503", got)
	}
	if _, err := remote.Image(untagged, anyone); err != nil {
		t.Errorf("a read during a collection: %v", err)
	}
	if _, err := r.Collect(context.Background()); !errors.Is(err, registry.ErrCollecting) {
		t.Errorf("a second collection: %v, want ErrCollecting", err)
	}
	release()
	if err := remote.Write(ref, gone, admin); err != nil {
		t.Fatalf("a push after the collection: %v", err)
	}
	pulled, err := remote.Image(ref, anyone)
	if err != nil {
		t.Fatalf("the image pushed again: %v", err)
	}
	layers, err := pulled.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range layers {
		rc, err := l.Compressed()
		if err != nil {
			t.Fatalf("a layer of the image pushed again: %v", err)
		}
		rc.Close()
	}
}

// TestRegistryOnIPv6 serves on ::1 with a node certificate for it.
func TestRegistryOnIPv6(t *testing.T) {
	osCA, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reg := registrytest.Start(t, osCA, true, nil)
	if !strings.HasPrefix(reg.Host, "[::1]:") {
		t.Errorf("Host = %s, want [::1]:<port>", reg.Host)
	}
	registryName, err := name.NewRegistry(reg.Host)
	if err != nil {
		t.Fatal(err)
	}
	if repos, err := remote.Catalog(context.Background(), registryName, remote.WithTransport(reg.Transport(t, nil))); err != nil || len(repos) != 0 {
		t.Errorf("the catalog: %v, %v", repos, err)
	}
}

// TestCollectOverHTTP runs a collection for the admin alone, one at a time, and reports the
// bytes freed.
func TestCollectOverHTTP(t *testing.T) {
	r := startRegistry(t)
	for who, want := range map[string]int{"anyone": http.StatusUnauthorized, "reader": http.StatusForbidden, "node admin": http.StatusForbidden} {
		if got := r.status(t, http.MethodPost, registry.GCPath, who); got != want {
			t.Errorf("a collection as %s: %d, want %d", who, got, want)
		}
	}
	if got := r.status(t, http.MethodGet, registry.GCPath, "admin"); got != http.StatusMethodNotAllowed {
		t.Errorf("GET %s: %d, want 405", registry.GCPath, got)
	}
	release := r.HoldCollection()
	if got := r.status(t, http.MethodPost, registry.GCPath, "admin"); got != http.StatusConflict {
		t.Errorf("a collection during another: %d, want 409", got)
	}
	release()
	img, err := random.Image(2048, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r.ref(t, "ghcr.io/x/y:1"), img, remote.WithTransport(r.clients["admin"])); err != nil {
		t.Fatal(err)
	}
	// Collections keep untagged manifests, so the digest goes, not the tag alone.
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Delete(r.ref(t, "ghcr.io/x/y@"+digest.String()), remote.WithTransport(r.clients["admin"])); err != nil {
		t.Fatal(err)
	}
	var res registry.GCResult
	if code := r.do(t, http.MethodPost, registry.GCPath, "admin", &res); code != http.StatusOK || res.FreedBytes < 2048 {
		t.Errorf("the admin's collection: %d, %+v", code, res)
	}
}
