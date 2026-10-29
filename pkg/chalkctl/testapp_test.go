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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
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
	if s.Loader.Schemas == "" {
		s.Loader.Schemas = filepath.Join(t.TempDir(), "schemas")
		copySchemas(t, s.Loader.Schemas)
	}
	// It boots the store of the root hash abab…, whose config slot an install in place writes.
	if s.Paths.Cmdline == "" {
		s.Paths.Cmdline = filepath.Join(t.TempDir(), "cmdline")
		writeFile(t, s.Paths.Cmdline, "init=/x usrhash="+strings.Repeat("ab", 32)+"\n")
	}
	ca, _ := pki.ParseCertificate([]byte(ta.secrets.OSCA.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	s.ClientCAs = chalkd.StaticCAs(pool)
	return serveTLS(t, s.Handler(), chalkd.TLSConfig(chalkd.StaticCertificate(&pair), s.ClientCAs))
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

// clusterFile is the cluster file chalkd writes for a node of the kind.
func clusterFile(kind string) string {
	return `{"kind": "` + kind + `", "endpoint": "https://10.0.0.10:6443", "version": "1.37.1",
	  "ipFamilies": ["ipv4"], "podCIDRs": {"ipv4": "10.244.0.0/16"}, "serviceCIDRs": {"ipv4": "10.96.0.0/12"},
	  "dnsIPs": {"ipv4": "10.96.0.10"}, "nodeCIDRMaskSizes": {"ipv4": 24}, "domain": "cluster.local",
	  "nodeIP": {"validSubnets": [], "timeout": 300}, "vip": {"addresses": [], "mode": "l2"},
	  "extraArgs": {}, "images": {"etcd": "e", "kubeAPIServer": "a", "kubeControllerManager": "c", "kubeScheduler": "s"}}`
}

// testImageJSON is the image.json of a Kubernetes image without flannel.
const testImageJSON = `{"version": "1.37.1", "cni": "none", "images": {"etcd": "e", "kubeAPIServer": "a",
  "kubeControllerManager": "c", "kubeScheduler": "s", "kubeProxy": "p", "coredns": "d", "flannel": ""}}`

// installedNode is a normal-mode chalkd whose STATE holds the test node's identity.
func installedNode(t *testing.T, ta *testApp, recorded []byte) (*chalkd.Server, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	r := &fakeRunner{}
	s := &chalkd.Server{
		Mode: nodev1.Mode_MODE_NORMAL,
		Paths: chalkd.Paths{
			StateDir:       filepath.Join(root, "state"),
			BootDisk:       "/dev/disk/chalk-boot-disk",
			BootPartitions: "/dev/disk/chalk-boot",
			StorageStatus:  filepath.Join(root, "storage-status.json"),
			MountInfo:      filepath.Join(root, "mountinfo"),
		},
		Run:        r,
		Host:       storage.Host{SysRoot: filepath.Join(root, "sys"), UdevRoot: filepath.Join(root, "udev"), DevRoot: filepath.Join(root, "dev")},
		RebootNode: func() {},
	}
	s.Loader.RunDir = filepath.Join(root, "run")
	s.Loader.Effects = filepath.Join(root, "effects.json")
	s.Loader.SetHostname = func(string) error { return nil }
	// The node runs the image of the root hash abab…, whose slot holds the identity alone.
	s.Loader.Slots = filepath.Join(root, "state", "config")
	s.Loader.Cmdline = filepath.Join(root, "cmdline")
	s.Paths.Cmdline = s.Loader.Cmdline
	writeFile(t, s.Loader.Cmdline, "init=/x usrhash="+strings.Repeat("ab", 32)+"\n")
	if err := slot.Write(slotOf(t, s), slot.Stored{Identity: recorded}); err != nil {
		t.Fatal(err)
	}
	section, _ := json.Marshal(ta.identity(t).Storage)
	writeFile(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), string(section))
	// The storage rules compare the config with the section STATE records.
	s.Paths.Facts = filepath.Join(root, "facts.json")
	s.Paths.EffectsBaseline = filepath.Join(root, "run", "effects-baseline.json")
	writeFile(t, s.Paths.Facts, `{"recorded": {"file": "`+filepath.Join(s.Paths.StateDir, "storage", "storage.json")+`"}}`)
	writeFile(t, s.Paths.MountInfo, "41 30 253:1 / /var rw - ext4 /dev/mapper/var rw\n")
	withPlugins(t, s, layers.Of(), nil)
	writeEffects(t, s)
	return s, r
}

// writeEffects declares the effects of the default image, which the node carries out, without
// those that run an action of a layer s's image does not carry, as an image without the layer
// has none.
func writeEffects(t *testing.T, s *chalkd.Server) {
	t.Helper()
	raw, err := os.ReadFile("../../test/fixtures/kinds/effects.json")
	if err != nil {
		t.Fatal(err)
	}
	var defs map[string]effects.Definition
	if err := json.Unmarshal(raw, &defs); err != nil {
		t.Fatal(err)
	}
	for name, d := range defs {
		// An action its layer lacks stays, so the delivery refuses it.
		if l, _, _ := strings.Cut(d.Chalkd, ":"); d.Chalkd != "" && !s.Plugins.Layers().Has(l) {
			delete(defs, name)
		}
	}
	data, err := json.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.Loader.Effects, string(data))
}

// withPlugins compiles the Kubernetes and router layers into s, with s's Kubernetes side and the
// router side r, or one that runs s's programs when r is nil, on an image of the layers l.
func withPlugins(t *testing.T, s *chalkd.Server, l layers.Set, r *chalkd.Router) {
	t.Helper()
	if r == nil {
		r = &chalkd.Router{Run: s.Run}
	}
	reg, err := layer.NewRegistry(l, chalkd.NewKubernetesLayer(s, s.Kubernetes), chalkd.NewRouterLayer(r))
	if err != nil {
		t.Fatal(err)
	}
	s.Plugins = reg
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

type fakeRunner struct{ calls []string }

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunWithEnv(ctx, nil, name, args...)
}

func (f *fakeRunner) RunWithEnv(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	return nil, nil
}

func (f *fakeRunner) RunWithInput(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	return f.RunWithEnv(ctx, nil, name, args...)
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

// kubernetesImage gives the node s, which has no Kubernetes side, as in maintenance mode, a running
// image with Kubernetes: its image.json and the Kubernetes layer with a side of its own that reads
// it, whose files are in a directory of the test.
func kubernetesImage(t *testing.T, s *chalkd.Server) {
	t.Helper()
	file := imageJSON(t)
	root := t.TempDir()
	k := chalkd.NewKubernetes(chalkd.KubernetesOptions{
		Paths: knode.Paths{
			State: filepath.Join(root, "state"), Cluster: filepath.Join(root, "cluster.json"), NodeFile: filepath.Join(root, "node.json"),
			Run: filepath.Join(root, "run"), PKI: filepath.Join(root, "pki"), KubeletPKI: filepath.Join(root, "kubelet"), EtcdData: filepath.Join(root, "etcd"),
		},
		ImageFile:   file,
		ClusterFile: filepath.Join(root, "cluster.json"),
	})
	reg, err := layer.NewRegistry(layers.Of(layers.Kubernetes), chalkd.NewKubernetesLayer(s, k), chalkd.NewRouterLayer(&chalkd.Router{Run: s.Run}))
	if err != nil {
		t.Fatal(err)
	}
	s.Plugins = reg
	s.Loader.Layers = layer.Active[nodeconfig.Layer](reg)
}
