package kubernetes

import (
	"cmp"
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// fakeNode is the common chalkd as the plugin's tests need it.
type fakeNode struct {
	mu    sync.Mutex
	name  string
	cert  layer.Certificate
	run   tool.Runner
	slot  string
	state string
}

func (n *fakeNode) NodeName() string                   { return cmp.Or(n.name, "<node>") }
func (n *fakeNode) NodeCertificate() layer.Certificate { return n.cert }
func (n *fakeNode) Changes() sync.Locker               { return &n.mu }
func (n *fakeNode) Runner() tool.Runner                { return n.run }
func (n *fakeNode) Slot() (string, error)              { return n.slot, nil }
func (n *fakeNode) StateDir() string                   { return n.state }

// fakeCertificate is the node certificate as the plugin reads it: the OS CAs the node trusts.
type fakeCertificate struct{ osCA string }

func (c *fakeCertificate) Current() *pki.NodeCredential { return nil }
func (c *fakeCertificate) OSCA() string                 { return c.osCA }
func (c *fakeCertificate) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return nil, errors.New("the test node presents no certificate")
}

// kubernetesNode is the Kubernetes layer of node n1, a node of the kind with the files chalkd
// reads, which trusts the test OS CA; with a share when share is set. Its control plane applies
// 19 objects at once.
func kubernetesNode(t *testing.T, kind string, share bool) (*Plugin, *tooltest.Runner) {
	t.Helper()
	root := t.TempDir()
	osCA, err := kubernetestest.OSCA()
	if err != nil {
		t.Fatal(err)
	}
	r := &tooltest.Runner{}
	n := &fakeNode{name: "n1", cert: &fakeCertificate{osCA: osCA.Certificate}, run: r, slot: filepath.Join(root, "state", "config", "a"), state: filepath.Join(root, "state")}
	paths := kubernetestest.Paths(root, n.state)
	p := New(n, Options{
		Paths: paths,
		// The image.json is the test's to write; the cluster file the layer writes is not the one it
		// reads unless the test says so.
		ImageFile:   kubernetestest.ImageFile(paths),
		ClusterFile: kubernetestest.ClusterFile(root),
		Hooks: Hooks{
			Slot:              n.Slot,
			Objects:           kubernetestest.NoObjects,
			ControlPlane:      kubernetestest.AppliedAtOnce,
			Node:              kubernetestest.NodeReady,
			EtcdEndpoints:     kubernetestest.NoEtcdEndpoints,
			ClusterAnswers:    kubernetestest.NoClusterAnswers,
			CheckControlPlane: kubernetestest.ControlPlaneCurrent,
			APIClient:         kubernetestest.EmptyAPIServer,
		},
	})
	// The loops a test started would run on against its removed files, and load the tests after
	// it, until the test binary ends.
	t.Cleanup(p.Stop)
	kubernetestest.WriteNode(t, paths, kind, share)
	return p, r
}

// slotOf is the running image's config slot of the node of p.
func slotOf(t *testing.T, p *Plugin) string {
	t.Helper()
	dir, err := p.node.Slot()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
