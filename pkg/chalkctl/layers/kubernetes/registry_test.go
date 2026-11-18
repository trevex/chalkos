package kubernetes

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"io/fs"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/trevex/chalkos/pkg/kubernetes/clusterregistry"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
	"github.com/trevex/chalkos/pkg/registry/registrytest"
)

func init() {
	// No line for every request a test makes that a registry refuses.
	registry.QuietLogs()
}

// testEndpoints are registries serving the cluster registry's endpoints on loopback.
type testEndpoints struct {
	endpoints
	// regs are the registries by endpoint.
	regs map[netip.Addr]*registrytest.Registry
	osCA pki.CertKey
}

// startEndpoints serves a registry for each endpoint of addrs, mirroring docker.io and ghcr.io on
// port 5000, with a node certificate that names the endpoint. wraps, by endpoint, wrap a
// registry's handler. The endpoints reach them through connect, with an admin's certificate.
func startEndpoints(t *testing.T, wraps map[string]func(http.Handler) http.Handler, addrs ...string) *testEndpoints {
	t.Helper()
	now := time.Now()
	osCA, err := pki.NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	r, err := clusterregistry.New(clusterregistry.Spec{Mirrors: []string{"docker.io", "ghcr.io"}, Endpoints: addrs})
	if err != nil {
		t.Fatal(err)
	}
	te := &testEndpoints{endpoints: endpoints{Registry: r, connect: map[netip.Addr]string{}}, regs: map[netip.Addr]*registrytest.Registry{}, osCA: osCA}
	for _, e := range r.Endpoints {
		reg := registrytest.Start(t, osCA, e.Is6(), wraps[e.String()], e.AsSlice())
		te.regs[e] = reg
		te.connect[e] = reg.Host
	}
	te.tls = te.clientTLS(t, pki.RoleAdmin)
	return te
}

// clientTLS is the TLS config of a client of the role.
func (te *testEndpoints) clientTLS(t *testing.T, role pki.Role) *tls.Config {
	t.Helper()
	c, err := pki.IssueClient(te.osCA, "client-"+string(role), role, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(c.Certificate), []byte(c.Key))
	if err != nil {
		t.Fatal(err)
	}
	roots, err := pki.BundlePool(te.osCA.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: roots}
}

// digestAt is the digest the endpoint's registry serves for path, read anonymously; ok is false
// when it serves none.
func (te *testEndpoints) digestAt(t *testing.T, e, path string) (d v1.Hash, ok bool) {
	t.Helper()
	reg := te.regs[netip.MustParseAddr(e)]
	desc, err := remote.Head(ref(t, reg.Host+"/"+path), remote.WithTransport(reg.Transport(t, nil)))
	if err != nil {
		return v1.Hash{}, false
	}
	return desc.Digest, true
}

// pushTo pushes img as path below the endpoints es directly, as the admin.
func (te *testEndpoints) pushTo(t *testing.T, img v1.Image, path string, es ...string) {
	t.Helper()
	admin, err := pki.IssueClient(te.osCA, "admin", pki.RoleAdmin, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		reg := te.regs[netip.MustParseAddr(e)]
		if err := remote.Write(ref(t, reg.Host+"/"+path), img, remote.WithTransport(reg.Transport(t, &admin))); err != nil {
			t.Fatal(err)
		}
	}
}

func ref(t *testing.T, s string) name.Reference {
	t.Helper()
	r, err := name.ParseReference(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func randomImage(t *testing.T) v1.Image {
	t.Helper()
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func digestOf(t *testing.T, img interface{ Digest() (v1.Hash, error) }) v1.Hash {
	t.Helper()
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestPushToEndpoints pushes to every endpoint, IPv4 and IPv6, reports each, and names those
// that did not get the image.
func TestPushToEndpoints(t *testing.T) {
	te := startEndpoints(t, nil, "192.0.2.1", "fd00::2", "192.0.2.3")
	te.regs[netip.MustParseAddr("192.0.2.3")].Close()
	img := randomImage(t)
	want := digestOf(t, img)
	var out bytes.Buffer
	err := te.push(context.Background(), &out, img, ref(t, "docker.io/library/busybox:1.37.0"))
	if want := "the image is missing on 1 of 3 endpoints of the cluster registry: 192.0.2.3; nothing copies it between them, so push it again once they are reachable"; err == nil || err.Error() != want {
		t.Errorf("push = %v, want %q", err, want)
	}
	for _, line := range []string{
		"192.0.2.1: pushed 192.0.2.1:5000/docker.io/library/busybox:1.37.0 as " + want.String() + "\n",
		"fd00::2: pushed [fd00::2]:5000/docker.io/library/busybox:1.37.0 as " + want.String() + "\n",
		"192.0.2.3: failed: ",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output = %q, want %q", out.String(), line)
		}
	}
	for _, e := range []string{"192.0.2.1", "fd00::2"} {
		if d, ok := te.digestAt(t, e, "docker.io/library/busybox:1.37.0"); d != want {
			t.Errorf("%s serves %v (%v), want %s", e, d, ok, want)
		}
	}
	// A reader may not push; the registry says so for the endpoint.
	te.tls = te.clientTLS(t, pki.RoleReader)
	out.Reset()
	if err := te.push(context.Background(), &out, img, ref(t, "docker.io/library/busybox:1.37.1")); err == nil || !strings.Contains(out.String(), "192.0.2.1: failed: ") || !strings.Contains(out.String(), "admin role") {
		t.Errorf("push as a reader = %v, %q, want the registry's refusal", err, out.String())
	}
}

// otherDigest makes the registry claim another digest for every manifest a HEAD asks for.
func otherDigest(other v1.Hash) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/manifests/") {
				w = rewriteDigest{w, other.String()}
			}
			next.ServeHTTP(w, r)
		})
	}
}

type rewriteDigest struct {
	http.ResponseWriter
	digest string
}

func (w rewriteDigest) WriteHeader(code int) {
	if code == http.StatusOK {
		w.Header().Set("Docker-Content-Digest", w.digest)
	}
	w.ResponseWriter.WriteHeader(code)
}

// TestPushChecksTheDigest fails a push to an endpoint that serves another digest afterwards.
func TestPushChecksTheDigest(t *testing.T) {
	other := digestOf(t, randomImage(t))
	te := startEndpoints(t, map[string]func(http.Handler) http.Handler{"192.0.2.2": otherDigest(other)}, "192.0.2.1", "192.0.2.2")
	img := randomImage(t)
	var out bytes.Buffer
	err := te.push(context.Background(), &out, img, ref(t, "docker.io/library/busybox:1.37.0"))
	if err == nil || !strings.Contains(err.Error(), "missing on 1 of 2 endpoints of the cluster registry: 192.0.2.2;") {
		t.Errorf("push = %v, want 192.0.2.2 named", err)
	}
	if want := "192.0.2.2: failed: the endpoint serves " + other.String() + ", not " + digestOf(t, img).String() + "\n"; !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// TestPushIndex pushes the images of an index before the index, as the registry wants them, and
// checks the index's digest.
func TestPushIndex(t *testing.T) {
	var mu sync.Mutex
	var puts []string
	record := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") {
				mu.Lock()
				puts = append(puts, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
				mu.Unlock()
			}
			next.ServeHTTP(w, r)
		})
	}
	te := startEndpoints(t, map[string]func(http.Handler) http.Handler{"192.0.2.1": record}, "192.0.2.1", "fd00::2")
	idx, err := random.Index(512, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := te.push(context.Background(), &out, idx, ref(t, "ghcr.io/a/b:1")); err != nil {
		t.Fatalf("push = %v; %s", err, out.String())
	}
	want := digestOf(t, idx)
	for _, e := range []string{"192.0.2.1", "fd00::2"} {
		if d, _ := te.digestAt(t, e, "ghcr.io/a/b:1"); d != want {
			t.Errorf("%s serves %v, want the index %s", e, d, want)
		}
	}
	if len(puts) != 3 || puts[2] != "1" {
		t.Errorf("manifests put = %v, want the two images, then the tag 1", puts)
	}
}

// TestPushSources reads an OCI layout, an OCI archive, a docker archive and a layout of an index,
// and refuses a layout of two images, an archive that leaves its directory, and a path that
// neither exists nor names an image.
func TestPushSources(t *testing.T) {
	dir := t.TempDir()
	// Where an OCI archive is unpacked.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	img := randomImage(t)
	idx, err := random.Index(512, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	ociLayout := filepath.Join(dir, "layout")
	writeLayout(t, ociLayout, img)
	ociArchive := filepath.Join(dir, "oci.tar")
	tarDir(t, ociLayout, ociArchive)
	dockerArchive := filepath.Join(dir, "docker.tar")
	tag, _ := name.NewTag("busybox:1.37.0")
	if err := tarball.WriteToFile(dockerArchive, tag, img); err != nil {
		t.Fatal(err)
	}
	indexLayout := filepath.Join(dir, "index")
	p, err := layout.Write(indexLayout, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AppendIndex(idx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		source string
		want   v1.Hash
	}{
		{ociLayout, digestOf(t, img)},
		{ociArchive, digestOf(t, img)},
		{dockerArchive, digestOf(t, img)},
		{indexLayout, digestOf(t, idx)},
	} {
		got, cleanup, err := openSource(context.Background(), tc.source)
		if err != nil {
			t.Errorf("openSource(%s) = %v", filepath.Base(tc.source), err)
			continue
		}
		if d := digestOf(t, got); d != tc.want {
			t.Errorf("openSource(%s) = %s, want %s", filepath.Base(tc.source), d, tc.want)
		}
		cleanup()
	}
	// The OCI archive's unpacked layout is gone once cleaned up.
	if left, _ := os.ReadDir(tmp); len(left) > 0 {
		t.Errorf("unpacked layouts left: %v", left)
	}

	two := filepath.Join(dir, "two")
	writeLayout(t, two, img, randomImage(t))
	escaping := filepath.Join(dir, "escaping.tar")
	writeTar(t, escaping, map[string]string{"oci-layout": `{"imageLayoutVersion": "1.0.0"}`, "../x": "x"})
	for source, want := range map[string]string{
		two:                               "names 2 images; push a layout of one image",
		escaping:                          "the entry ../x leaves the archive's directory",
		filepath.Join(dir, "Missing.tar"): "is no file, no directory and no image reference",
	} {
		if _, _, err := openSource(context.Background(), source); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("openSource(%s) = %v, want %q", filepath.Base(source), err, want)
		}
	}
}

func writeLayout(t *testing.T, dir string, imgs ...v1.Image) {
	t.Helper()
	p, err := layout.Write(dir, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range imgs {
		if err := p.AppendImage(img); err != nil {
			t.Fatal(err)
		}
	}
}

// tarDir writes the files below dir into the tar archive out, by their paths below dir.
func tarDir(t *testing.T, dir, out string) {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	writeTar(t, out, files)
}

func writeTar(t *testing.T, out string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range slices.Sorted(maps.Keys(files)) {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTargetOf names an image below an endpoint by the prefix of its host, and refuses a host
// the ClusterRegistry does not mirror.
func TestTargetOf(t *testing.T) {
	r, err := clusterregistry.New(clusterregistry.Spec{Mirrors: []string{"docker.io", "ghcr.io"}, Endpoints: []string{"fd00::11", "192.0.2.1"}})
	if err != nil {
		t.Fatal(err)
	}
	s := endpoints{Registry: r}
	digest := "sha256:" + strings.Repeat("ab", 32)
	for _, tc := range []struct{ ref, endpoint, want string }{
		{"nginx:1.27", "fd00::11", "[fd00::11]:5000/docker.io/library/nginx:1.27"},
		{"index.docker.io/library/nginx:1.27", "192.0.2.1", "192.0.2.1:5000/docker.io/library/nginx:1.27"},
		{"ghcr.io/a/b@" + digest, "192.0.2.1", "192.0.2.1:5000/ghcr.io/a/b@" + digest},
	} {
		got, err := s.targetOf(netip.MustParseAddr(tc.endpoint), ref(t, tc.ref))
		if err != nil || got.String() != tc.want {
			t.Errorf("targetOf(%s, %s) = %v, %v, want %s", tc.endpoint, tc.ref, got, err, tc.want)
		}
	}
	want := "the ClusterRegistry mirrors docker.io and ghcr.io, not quay.io, so containerd never pulls quay.io/a/b:1 from it; add quay.io to its mirrors"
	if _, err := s.targetOf(r.Endpoints[0], ref(t, "quay.io/a/b:1")); err == nil || err.Error() != want {
		t.Errorf("targetOf(quay.io/a/b:1) = %v, want %q", err, want)
	}
}
