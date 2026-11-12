package chalkctl

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/chalkdtest"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config/slot"
	imagecontract "github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/kubetest"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

// testApp is chalkctl with the test cluster's inventory and a plaintext secrets file in a
// temporary directory.
type testApp struct {
	*app
	dir string
	// inventory is the test cluster's inventory in dir.
	inventory string
	secrets   clustersecrets.Secrets
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer
	// kind is n1's Kubernetes kind, which withKind gives it.
	kind string
	// prompts records what chalkctl asked on the terminal; answers are given in order.
	prompts []string
	answers []string
	// wrap, when set, wraps the handler of the nodes startNode serves for each request, as a
	// network between chalkctl and the node would change the answers. requests are the addresses
	// the requests came from, by their procedure. mu guards both.
	mu       sync.Mutex
	wrap     func(http.Handler) http.Handler
	requests map[string][]string
	// served holds the certificate each node startNode serves presents, by its address; mu guards
	// it. renewMaintenanceCertificate replaces one, as a reboot in maintenance mode does.
	served map[string]*atomic.Pointer[tls.Certificate]
}

// setWrap has the nodes startNode serves wrap their handler with wrap from now on.
func (ta *testApp) setWrap(wrap func(http.Handler) http.Handler) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	ta.wrap = wrap
}

// remotes are the addresses the requests for the procedure came from, one per request, in order.
func (ta *testApp) remotes(procedure string) []string {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	return slices.Clone(ta.requests[procedure])
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	return newTestAppAt(t, time.Now())
}

// newTestAppAt is newTestApp with secrets generated at now.
func newTestAppAt(t *testing.T, now time.Time) *testApp {
	t.Helper()
	dir := t.TempDir()
	secrets, err := clustersecrets.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := secrets.Encode()
	writeFile(t, filepath.Join(dir, "secrets.json"), string(data))
	ta := &testApp{dir: dir, inventory: writeInventory(t, dir), secrets: secrets, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	ta.app = &app{
		stdin:  strings.NewReader(""),
		stdout: ta.stdout,
		stderr: ta.stderr,
		home:   dir,
	}
	ta.readSecret = func(_ context.Context, prompt string) ([]byte, error) {
		ta.prompts = append(ta.prompts, prompt)
		if len(ta.answers) == 0 {
			return nil, errNoTerminal
		}
		answer := ta.answers[0]
		ta.answers = ta.answers[1:]
		return []byte(answer), nil
	}
	return ta
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// args appends the flags that point chalkctl at the test cluster and the node's address.
func (ta *testApp) args(cmd []string, addr string) []string {
	return append(cmd, "--inventory", ta.inventory, "--address", addr)
}

// writeNode replaces n1's file with the documents given.
func (ta *testApp) writeNode(t *testing.T, docs ...string) {
	t.Helper()
	writeFile(t, filepath.Join(ta.dir, "nodes", "n1.yaml"), strings.Join(docs, "---\n"))
}

// configVersion is the version of n1's config as the inventory gives it.
func (ta *testApp) configVersion(t *testing.T) string {
	t.Helper()
	return inventoryVersion(t, ta.inventory)
}

// identity is n1's identity as its config translates on its image in the inventory.
func (ta *testApp) identity(t *testing.T) identity.Identity {
	t.Helper()
	c, err := loadInventory(context.Background(), ta.inventory)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := c.config("n1")
	if err != nil {
		t.Fatal(err)
	}
	md, err := nc.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := imageContract(nc.ImageDir)
	if err != nil {
		t.Fatal(err)
	}
	ct.platform = md.Platform
	d, err := checkNodeConfig(nc, ct)
	if err != nil {
		t.Fatal(err)
	}
	return d.identity
}

// startNode serves a chalkd in the mode with the test cluster's OS CA. In normal mode it serves
// a node certificate for n1, which names the node to chalkd too.
func (ta *testApp) startNode(t *testing.T, s *chalkd.Server) string {
	t.Helper()
	cert := ta.secrets.OSCA
	var err error
	if s.Mode == nodev1.Mode_MODE_NORMAL {
		cert, err = pki.IssueNode(ta.secrets.NodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
	} else {
		cert, err = pki.SelfSigned("chalkd", time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode == nodev1.Mode_MODE_NORMAL && s.Certificate == nil {
		dir := filepath.Join(t.TempDir(), "chalkd")
		writeFile(t, filepath.Join(dir, chalkd.NodeCertificateFile), cert.Certificate+cert.Key)
		writeFile(t, filepath.Join(dir, chalkd.CAFile), ta.secrets.OSCA.Certificate)
		if s.Certificate, err = chalkd.LoadNodeCertificate(dir); err != nil {
			t.Fatal(err)
		}
	}
	pair, _ := tls.X509KeyPair([]byte(cert.Certificate), []byte(cert.Key))
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	s.Fingerprint = pki.Fingerprint(leaf.Raw)
	// The node runs the test node's image in the inventory.
	if s.Paths.OSRelease == "" {
		s.Paths.OSRelease = filepath.Join(t.TempDir(), "os-release")
		writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=test\nCHALKOS_PLATFORM=metal\n")
	}
	contractDir(t, s)
	// It boots the store of the root hash abab…, whose config slot an install in place writes.
	if s.Paths.Cmdline == "" {
		s.Paths.Cmdline = filepath.Join(t.TempDir(), "cmdline")
		writeFile(t, s.Paths.Cmdline, "init=/x usrhash="+strings.Repeat("ab", 32)+"\n")
	}
	ca, _ := pki.ParseCertificate([]byte(ta.secrets.OSCA.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	s.ClientCAs = chalkd.StaticCAs(pool)
	h := s.Handler()
	record := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		procedure := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		ta.mu.Lock()
		if ta.requests == nil {
			ta.requests = map[string][]string{}
		}
		ta.requests[procedure] = append(ta.requests[procedure], r.RemoteAddr)
		wrap := ta.wrap
		ta.mu.Unlock()
		if wrap != nil {
			wrap(h).ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
	served := &atomic.Pointer[tls.Certificate]{}
	served.Store(&pair)
	addr := serveTLS(t, record, chalkd.TLSConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return served.Load(), nil }, s.ClientCAs))
	ta.mu.Lock()
	defer ta.mu.Unlock()
	if ta.served == nil {
		ta.served = map[string]*atomic.Pointer[tls.Certificate]{}
	}
	ta.served[addr] = served
	return addr
}

// renewMaintenanceCertificate has the node at addr present a new self-signed certificate, as
// chalkd makes one when it boots in maintenance mode, and returns its fingerprint.
func (ta *testApp) renewMaintenanceCertificate(t *testing.T, addr string) string {
	t.Helper()
	cert, err := pki.SelfSigned("chalkd", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cert.Certificate), []byte(cert.Key))
	if err != nil {
		t.Fatal(err)
	}
	ta.mu.Lock()
	defer ta.mu.Unlock()
	ta.served[addr].Store(&pair)
	return pki.Fingerprint(pair.Certificate[0])
}

// serveTLS serves h with the configuration as chalkd does, and returns the address. httptest's
// servers would add a certificate of their own, which TLS prefers to GetCertificate.
func serveTLS(t *testing.T, h http.Handler, cfg *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, TLSConfig: cfg}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func maintenanceNode() *chalkd.Server {
	return &chalkd.Server{
		Mode:       nodev1.Mode_MODE_MAINTENANCE,
		InPlace:    func(context.Context, install.Request, []byte) error { return errors.New("unexpected install in place") },
		FromParts:  func(context.Context, install.PartsRequest) error { return errors.New("unexpected install onto a disk") },
		RebootNode: func() {},
	}
}

// fixtureSchemas are the schemas of the built-in kinds and of networkd's Network.
var fixtureSchemas = []string{"../../test/fixtures/kinds/builtin", "../../test/fixtures/config/networkd/schemas"}

// copySchemas writes the fixture schemas into dir, as an image ships them.
func copySchemas(t *testing.T, dir string) {
	t.Helper()
	for _, src := range fixtureSchemas {
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, e.Name()), string(data))
		}
	}
}

// contractDir is the directory of s's contract; a server without one gets the fixture schemas.
func contractDir(t *testing.T, s *chalkd.Server) string {
	t.Helper()
	if s.Loader.Contract == nil {
		dir := t.TempDir()
		copySchemas(t, filepath.Join(dir, imagecontract.SchemasDir))
		loadContract(t, s, dir)
	}
	return s.Loader.Contract.Dir
}

// writeContract writes the file name of s's contract and loads the contract again, as chalkd loads
// the running image's as it starts.
func writeContract(t *testing.T, s *chalkd.Server, name, content string) {
	t.Helper()
	dir := contractDir(t, s)
	writeFile(t, filepath.Join(dir, name), content)
	loadContract(t, s, dir)
}

// loadContract makes the contract of dir s's.
func loadContract(t *testing.T, s *chalkd.Server, dir string) {
	t.Helper()
	var opts []imagecontract.Option
	if s.Loader.Render.Root != "" {
		opts = append(opts, imagecontract.RenderRoot(s.Loader.Render.Root))
	}
	c, err := imagecontract.Load(dir, opts...)
	if err != nil {
		t.Fatal(err)
	}
	s.Loader.Contract = c
}

// clusterFile is the cluster file chalkd writes for a node of the kind.
func clusterFile(t *testing.T, kind string) string {
	return kubetest.JSON(t, kubetest.Cluster(kind, kubetest.IPv4Only, func(c *kubernetes.Cluster) {
		c.IPFamilies = []nodeip.Family{nodeip.IPv4}
		c.NodeIP = kubernetes.NodeIP{ValidSubnets: []string{}, Timeout: 300}
		c.VIP = kubernetes.VIP{Addresses: []netip.Addr{}, Mode: "l2"}
		c.ExtraArgs = map[string]map[string]string{}
	}))
}

// testImageJSON is the image.json of a Kubernetes image with flannel, as the fixture schemas are.
const testImageJSON = kubernetestest.Image

// installedNode is a normal-mode chalkd whose STATE holds the test node's identity.
func installedNode(t *testing.T, ta *testApp, recorded []byte) (*chalkd.Server, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	r := &fakeRunner{}
	s := &chalkd.Server{
		Mode:       nodev1.Mode_MODE_NORMAL,
		Paths:      chalkd.PathsUnder(root),
		Run:        r,
		Host:       storage.Host{SysRoot: filepath.Join(root, "sys"), UdevRoot: filepath.Join(root, "udev"), DevRoot: filepath.Join(root, "dev")},
		RebootNode: func() {},
	}
	s.Loader = nodeconfig.LoaderUnder(root)
	s.Loader.Render.Run = outputs.Direct{}
	s.Loader.SetHostname = func(string) error { return nil }
	writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=test\nCHALKOS_PLATFORM=metal\n")
	// The node runs the image of the root hash abab…, whose slot holds the identity alone.
	writeFile(t, s.Paths.Cmdline, "init=/x usrhash="+strings.Repeat("ab", 32)+"\n")
	// The ESP holds the UKI the node booted the image from, uncounted, so its boot is found
	// healthy, as after an install.
	writeFile(t, filepath.Join(s.Paths.ESP, "EFI", "Linux", "chalkos_1.efi"), string(ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos", "IMAGE_VERSION": ""}, "init=/x usrhash="+strings.Repeat("ab", 32))))
	if err := slot.Write(slotOf(t, s), slot.Stored{Identity: recorded}); err != nil {
		t.Fatal(err)
	}
	section, _ := json.Marshal(ta.identity(t).Storage)
	writeFile(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), string(section))
	// The storage rules compare the config with the section STATE records.
	writeContract(t, s, imagecontract.FactsFile, `{"recorded": {"file": "`+filepath.Join(s.Paths.StateDir, "storage", "storage.json")+`"}}`)
	writeFile(t, s.Paths.MountInfo, "41 30 253:1 / /var rw - ext4 /dev/mapper/var rw\n")
	s.Plugins = chalkdtest.Plugins(t, s, layers.Of())
	writeEffects(t, s)
	return s, r
}

// writeEffects declares the effects of the default image, which the node carries out, as
// chalkdtest.Effects gives them for the layers of s's image.
func writeEffects(t *testing.T, s *chalkd.Server) {
	t.Helper()
	data, err := json.Marshal(chalkdtest.Effects(t, s.Plugins.Layers()))
	if err != nil {
		t.Fatal(err)
	}
	writeContract(t, s, imagecontract.EffectsFile, string(data))
}

// kubernetesState is the state the Kubernetes layer of s reads and writes.
func kubernetesState(s *chalkd.Server) knode.State {
	for _, p := range s.Plugins.Compiled() {
		if k, ok := p.(*kubernetesplugin.Plugin); ok {
			return knode.State{Paths: k.Paths()}
		}
	}
	return knode.State{}
}

// slotOf is the running image's config slot of a node installedNode made.
func slotOf(t *testing.T, s *chalkd.Server) string {
	t.Helper()
	dir, err := s.Loader.Slot()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeRunner records the commands run and answers each with nothing, but systemctl's list of
// failed units with failedUnits, which a test sets before the node serves, and a command with the
// argument fail with an error. A trial's deadline runs commands besides the requests, so it locks.
type fakeRunner struct {
	mu          sync.Mutex
	calls       []string
	failedUnits string
	fail        string
}

// setFail has the commands with the argument given fail from now on; "" has none fail.
func (f *fakeRunner) setFail(arg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = arg
}

func (f *fakeRunner) Exec(_ context.Context, c tool.Cmd) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := c.String()
	f.calls = append(f.calls, cmd)
	switch {
	case f.fail != "" && slices.Contains(c.Args, f.fail):
		return nil, errors.New(cmd + " failed")
	case strings.HasPrefix(cmd, "systemctl list-units --state=failed"):
		return []byte(f.failedUnits), nil
	}
	return nil, nil
}

// run runs chalkctl's command tree with args, with the test's streams, and returns the error of
// the command that ran.
func (a *app) run(ctx context.Context, args []string) error {
	root := newCommand(a)
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.ExecuteContext(ctx)
}

// run runs chalkctl's command tree as a.run does, naming the test cluster's secrets file with
// --secrets, as an operator does, when the command takes one, args name neither a secrets file nor
// a client file, and the file exists.
func (ta *testApp) run(ctx context.Context, args []string) error {
	return ta.app.run(ctx, ta.withSecrets(args))
}

// withSecrets is args with the test cluster's secrets file named, where run names it.
func (ta *testApp) withSecrets(args []string) []string {
	path := filepath.Join(ta.dir, "secrets.json")
	if _, err := os.Stat(path); err != nil {
		return args
	}
	cmd, _, err := newCommand(ta.app).Find(args)
	if err != nil || cmd.Flags().Lookup("secrets") == nil {
		return args
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		for _, flag := range []string{"--secrets", "--config"} {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return args
			}
		}
	}
	return append(slices.Clone(args), "--secrets", path)
}

// imageJSON writes the image.json of a running image with Kubernetes and returns its path.
func imageJSON(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "image.json")
	writeFile(t, file, testImageJSON)
	return file
}

// kubernetesImage gives the node s, whose image has no Kubernetes layer, as in maintenance mode, a
// running image with Kubernetes: its image.json and the Kubernetes layer that reads it, whose files
// are in a directory of the test.
func kubernetesImage(t *testing.T, s *chalkd.Server) {
	t.Helper()
	file := imageJSON(t)
	root := t.TempDir()
	k := kubernetesplugin.New(s, kubernetesplugin.Options{
		Paths: knode.Paths{
			StateDir: filepath.Join(root, "state"), Cluster: filepath.Join(root, "cluster.json"), NodeFile: filepath.Join(root, "node.json"),
			RunDir: filepath.Join(root, "run"), PKI: filepath.Join(root, "pki"), KubeletPKI: filepath.Join(root, "kubelet"), EtcdData: filepath.Join(root, "etcd"),
		},
		ImageFile:   file,
		ClusterFile: filepath.Join(root, "cluster.json"),
	})
	s.Plugins = chalkdtest.Plugins(t, s, layers.Of(layers.Kubernetes), k)
	s.Loader.Layers = layer.Active[nodeconfig.Layer](s.Plugins)
}
