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
	// out and errOut take what the commands print.
	out, errOut bytes.Buffer
}

// startEndpoints serves a registry for each endpoint of addrs, mirroring docker.io, ghcr.io and
// registry.k8s.io on port 5000, with a node certificate that names the endpoint. wraps, by
// endpoint, wrap a registry's handler. The endpoints reach them through connect, with an admin's
// certificate.
func startEndpoints(t *testing.T, wraps map[string]func(http.Handler) http.Handler, addrs ...string) *testEndpoints {
	t.Helper()
	osCA, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r, err := clusterregistry.New(clusterregistry.Spec{Mirrors: []string{"docker.io", "ghcr.io", "registry.k8s.io"}, Endpoints: addrs})
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

// reset empties what the commands printed.
func (te *testEndpoints) reset() {
	te.out.Reset()
	te.errOut.Reset()
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

// admin is a transport to the endpoint's registry with an admin's certificate.
func (te *testEndpoints) admin(t *testing.T, e string) remote.Option {
	t.Helper()
	c, err := pki.IssueClient(te.osCA, "admin", pki.RoleAdmin, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return remote.WithTransport(te.regs[netip.MustParseAddr(e)].Transport(t, &c))
}

// at is path in the endpoint's registry.
func (te *testEndpoints) at(t *testing.T, e, path string) name.Reference {
	t.Helper()
	return ref(t, te.regs[netip.MustParseAddr(e)].Host+"/"+path)
}

// digestAt is the digest the endpoint's registry serves for path, read anonymously; ok is false
// when it serves none.
func (te *testEndpoints) digestAt(t *testing.T, e, path string) (d v1.Hash, ok bool) {
	t.Helper()
	reg := te.regs[netip.MustParseAddr(e)]
	desc, err := remote.Head(te.at(t, e, path), remote.WithTransport(reg.Transport(t, nil)))
	if err != nil {
		return v1.Hash{}, false
	}
	return desc.Digest, true
}

// pushTo pushes img, an image or an index, as path below the endpoints es directly, as the admin.
func (te *testEndpoints) pushTo(t *testing.T, img pushable, path string, es ...string) {
	t.Helper()
	for _, e := range es {
		if err := write(te.at(t, e, path), img, te.admin(t, e)); err != nil {
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

func iref(t *testing.T, s string) imageRef {
	t.Helper()
	r, err := parseImageRef(s)
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

func randomIndex(t *testing.T) v1.ImageIndex {
	t.Helper()
	idx, err := random.Index(512, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func digestOf(t *testing.T, img pushable) v1.Hash {
	t.Helper()
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// childrenOfIndex are the digests of the images idx names.
func childrenOfIndex(t *testing.T, idx v1.ImageIndex) []v1.Hash {
	t.Helper()
	m, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	var out []v1.Hash
	for _, d := range m.Manifests {
		out = append(out, d.Digest)
	}
	return out
}

// TestParseImageRef reads a tag, a digest, both as the kinds write images, and neither.
func TestParseImageRef(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	for _, tc := range []struct{ in, tag, digest, want string }{
		{"nginx", "latest", "", "docker.io/library/nginx:latest"},
		{"nginx:1.27", "1.27", "", "docker.io/library/nginx:1.27"},
		{"ghcr.io/a/b@" + digest, "", digest, "ghcr.io/a/b@" + digest},
		{"registry.k8s.io/kube-apiserver:v1.37.2@" + digest, "v1.37.2", digest, "registry.k8s.io/kube-apiserver:v1.37.2@" + digest},
		{"localhost:5000/a@" + digest, "", digest, "localhost:5000/a@" + digest},
	} {
		r, err := parseImageRef(tc.in)
		if err != nil || r.tag != tc.tag || r.digest != tc.digest || r.String() != tc.want {
			t.Errorf("parseImageRef(%s) = %+v (%s), %v; want tag %q, digest %q, %s", tc.in, r, r, err, tc.tag, tc.digest, tc.want)
		}
	}
	if _, err := parseImageRef("a@sha256:12"); err == nil {
		t.Error("parseImageRef of a short digest succeeded")
	}
}

// TestPushToEndpoints pushes to every endpoint, IPv4 and IPv6, reports each, and names those
// that did not get the image.
func TestPushToEndpoints(t *testing.T) {
	te := startEndpoints(t, nil, "192.0.2.1", "fd00::2", "192.0.2.3")
	te.regs[netip.MustParseAddr("192.0.2.3")].Close()
	img := randomImage(t)
	want := digestOf(t, img)
	err := te.push(context.Background(), &te.out, &te.errOut, img, iref(t, "busybox:1.37.0"))
	if want := "the push failed on 1 of 3 endpoints of the cluster registry: 192.0.2.3; nothing copies images between them, so push again once they are reachable"; err == nil || err.Error() != want {
		t.Errorf("push = %v, want %q", err, want)
	}
	if want := "pushing docker.io/library/busybox:1.37.0 as docker.io/library/busybox@" + want.String() + "\n" +
		"192.0.2.1: pushed 192.0.2.1:5000/docker.io/library/busybox:1.37.0 as " + want.String() + "\n" +
		"fd00::2: pushed [fd00::2]:5000/docker.io/library/busybox:1.37.0 as " + want.String() + "\n"; te.out.String() != want {
		t.Errorf("output = %q, want %q", te.out.String(), want)
	}
	if !strings.HasPrefix(te.errOut.String(), "192.0.2.3: failed: ") {
		t.Errorf("errors = %q, want 192.0.2.3 failed", te.errOut.String())
	}
	for _, e := range []string{"192.0.2.1", "fd00::2"} {
		if d, ok := te.digestAt(t, e, "docker.io/library/busybox:1.37.0"); d != want {
			t.Errorf("%s serves %v (%v), want %s", e, d, ok, want)
		}
	}
	// A reader may not push; the registry says so for the endpoint.
	te.tls = te.clientTLS(t, pki.RoleReader)
	te.reset()
	if err := te.push(context.Background(), &te.out, &te.errOut, img, iref(t, "busybox:1.37.1")); err == nil || !strings.Contains(te.errOut.String(), "192.0.2.1: failed: ") || !strings.Contains(te.errOut.String(), "admin role") {
		t.Errorf("push as a reader = %v, %q, want the registry's refusal", err, te.errOut.String())
	}
}

// TestPushPinned pushes a reference written as the kinds write control-plane images under its
// tag, and refuses a source of another digest before any endpoint, by tag and digest or by
// digest alone.
func TestPushPinned(t *testing.T) {
	var mu sync.Mutex
	var writes int
	count := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				mu.Lock()
				writes++
				mu.Unlock()
			}
			next.ServeHTTP(w, r)
		})
	}
	te := startEndpoints(t, map[string]func(http.Handler) http.Handler{"192.0.2.1": count}, "192.0.2.1", "192.0.2.2")
	img, other := randomImage(t), randomImage(t)
	d := digestOf(t, img).String()
	for _, bad := range []string{"registry.k8s.io/kube-apiserver:v1.37.2@" + digestOf(t, other).String(), "registry.k8s.io/kube-apiserver@" + digestOf(t, other).String()} {
		te.reset()
		err := te.push(context.Background(), &te.out, &te.errOut, img, iref(t, bad))
		if want := bad + " names the digest " + digestOf(t, other).String() + ", but the source's image is " + d; err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("push of %s = %v, want %q", bad, err, want)
		}
	}
	if writes != 0 || te.out.Len() != 0 {
		t.Errorf("a refused push wrote %d times and printed %q, want nothing", writes, te.out.String())
	}

	te.reset()
	if err := te.push(context.Background(), &te.out, &te.errOut, img, iref(t, "registry.k8s.io/kube-apiserver:v1.37.2@"+d)); err != nil {
		t.Fatalf("push = %v; %s", err, te.errOut.String())
	}
	for _, e := range []string{"192.0.2.1", "192.0.2.2"} {
		if got, _ := te.digestAt(t, e, "registry.k8s.io/kube-apiserver:v1.37.2"); got.String() != d {
			t.Errorf("%s serves the tag v1.37.2 at %v, want %s", e, got, d)
		}
	}
	if !strings.Contains(te.out.String(), "192.0.2.1: pushed 192.0.2.1:5000/registry.k8s.io/kube-apiserver:v1.37.2 as "+d) {
		t.Errorf("output = %q, want the push under the tag", te.out.String())
	}

	// By digest alone, untagged.
	te.reset()
	if err := te.push(context.Background(), &te.out, &te.errOut, other, iref(t, "registry.k8s.io/pause@"+digestOf(t, other).String())); err != nil {
		t.Fatalf("push by digest = %v; %s", err, te.errOut.String())
	}
	if _, ok := te.digestAt(t, "192.0.2.2", "registry.k8s.io/pause@"+digestOf(t, other).String()); !ok {
		t.Error("the endpoint lacks the image pushed by digest")
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
	err := te.push(context.Background(), &te.out, &te.errOut, img, iref(t, "busybox:1.37.0"))
	if err == nil || !strings.Contains(err.Error(), "failed on 1 of 2 endpoints of the cluster registry: 192.0.2.2;") {
		t.Errorf("push = %v, want 192.0.2.2 named", err)
	}
	if want := "192.0.2.2: failed: the endpoint serves " + other.String() + ", not " + digestOf(t, img).String() + "\n"; te.errOut.String() != want {
		t.Errorf("errors = %q, want %q", te.errOut.String(), want)
	}
}

// TestPushIndex pushes the images of an index before the index, as the registry wants them, and
// checks the index's digest, also on an endpoint that go-containerregistry would speak plain HTTP
// to.
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
	te := startEndpoints(t, map[string]func(http.Handler) http.Handler{"10.0.0.1": record}, "10.0.0.1", "fd00::2")
	idx := randomIndex(t)
	if err := te.push(context.Background(), &te.out, &te.errOut, idx, iref(t, "ghcr.io/a/b:1")); err != nil {
		t.Fatalf("push = %v; %s", err, te.errOut.String())
	}
	want := digestOf(t, idx)
	for _, e := range []string{"10.0.0.1", "fd00::2"} {
		if d, _ := te.digestAt(t, e, "ghcr.io/a/b:1"); d != want {
			t.Errorf("%s serves %v, want the index %s", e, d, want)
		}
	}
	if len(puts) != 3 || puts[2] != "1" {
		t.Errorf("manifests put = %v, want the two images, then the tag 1", puts)
	}
}

// TestPushIndexFromRegistry pushes an index of several platforms that a registry serves: the
// index and every image it names arrive, at the source's digests.
func TestPushIndexFromRegistry(t *testing.T) {
	te := startEndpoints(t, nil, "192.0.2.1", "192.0.2.2")
	osCA, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	src := registrytest.Start(t, osCA, false, nil)
	admin, err := pki.IssueClient(osCA, "admin", pki.RoleAdmin, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	idx := randomIndex(t)
	if err := remote.WriteIndex(ref(t, src.Host+"/ghcr.io/a/b:1"), idx, remote.WithTransport(src.Transport(t, &admin))); err != nil {
		t.Fatal(err)
	}
	img, cleanup, err := openSource(context.Background(), src.Host+"/ghcr.io/a/b:1", remote.WithTransport(src.Transport(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := te.push(context.Background(), &te.out, &te.errOut, img, iref(t, "ghcr.io/a/b:1@"+digestOf(t, idx).String())); err != nil {
		t.Fatalf("push = %v; %s", err, te.errOut.String())
	}
	for _, e := range []string{"192.0.2.1", "192.0.2.2"} {
		if d, _ := te.digestAt(t, e, "ghcr.io/a/b:1"); d != digestOf(t, idx) {
			t.Errorf("%s serves %v, want the index %s", e, d, digestOf(t, idx))
		}
		for _, c := range childrenOfIndex(t, idx) {
			if _, ok := te.digestAt(t, e, "ghcr.io/a/b@"+c.String()); !ok {
				t.Errorf("%s lacks the index's image %s", e, c)
			}
		}
	}
}

// TestPushSources reads an OCI layout, an OCI archive, a docker archive and a layout of an index,
// and refuses a layout of two images, a layout without a layer's blob, an archive that leaves its
// directory or unpacks to more than it holds, and a path that neither exists nor names an image.
func TestPushSources(t *testing.T) {
	dir := t.TempDir()
	// Where an OCI archive is unpacked.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	img := randomImage(t)
	idx := randomIndex(t)
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
	lacking := filepath.Join(dir, "lacking")
	writeLayout(t, lacking, img)
	layers, _ := img.Layers()
	gone, _ := layers[1].Digest()
	if err := os.Remove(filepath.Join(lacking, "blobs", gone.Algorithm, gone.Hex)); err != nil {
		t.Fatal(err)
	}
	lackingIndex := filepath.Join(dir, "lacking-index")
	p, err = layout.Write(lackingIndex, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AppendIndex(idx); err != nil {
		t.Fatal(err)
	}
	child := childrenOfIndex(t, idx)[1]
	if err := os.Remove(filepath.Join(lackingIndex, "blobs", child.Algorithm, child.Hex)); err != nil {
		t.Fatal(err)
	}
	escaping := filepath.Join(dir, "escaping.tar")
	writeTar(t, escaping, map[string]string{"oci-layout": `{"imageLayoutVersion": "1.0.0"}`, "../x": "x"})
	for source, want := range map[string]string{
		two:                               "the layout names 2 images; push a layout of one image",
		lacking:                           "the layer " + gone.String() + ": ",
		lackingIndex:                      "the image " + child.String() + " of the index: ",
		escaping:                          "the entry ../x leaves the archive's directory",
		filepath.Join(dir, "Missing.tar"): "is no file, no directory and no image reference",
	} {
		if _, _, err := openSource(context.Background(), source); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("openSource(%s) = %v, want %q", filepath.Base(source), err, want)
		}
	}
	// An archive that unpacks to more than it holds, as sparse entries do.
	if err := unpack(ociArchive, t.TempDir(), 64); err == nil || !strings.Contains(err.Error(), "unpacks to more bytes than it holds") {
		t.Errorf("unpack beyond the archive's size = %v, want it refused", err)
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

// TestTargetOf names an image below an endpoint by the prefix of its host, by its tag where it
// names one, and refuses a host the ClusterRegistry does not mirror.
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
		{"ghcr.io/a/b:2@" + digest, "192.0.2.1", "192.0.2.1:5000/ghcr.io/a/b:2"},
	} {
		got, err := s.targetOf(netip.MustParseAddr(tc.endpoint), iref(t, tc.ref))
		if err != nil || got.String() != tc.want {
			t.Errorf("targetOf(%s, %s) = %v, %v, want %s", tc.endpoint, tc.ref, got, err, tc.want)
		}
	}
	want := "the ClusterRegistry mirrors docker.io and ghcr.io, not quay.io, so containerd never pulls quay.io/a/b:1 from it; add quay.io to its mirrors"
	if _, err := s.targetOf(r.Endpoints[0], iref(t, "quay.io/a/b:1")); err == nil || err.Error() != want {
		t.Errorf("targetOf(quay.io/a/b:1) = %v, want %q", err, want)
	}
}
