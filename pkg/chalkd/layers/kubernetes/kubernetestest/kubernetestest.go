// Package kubernetestest holds the fixtures of the tests of chalkd's Kubernetes layer, and of
// chalkd's own tests that run it: a node's Kubernetes files, its shares and CAs, and the stand-ins
// for the preparation, the API server's Node and chalkd.
package kubernetestest

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/kubetest"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// OSCA is the OS CA every test node trusts.
var OSCA = sync.OnceValues(func() (pki.CertKey, error) { return pki.NewOSCA(time.Now()) })

// Image is the image.json of a Kubernetes image with flannel, as the builtin kinds of
// test/fixtures/kinds are.
const Image = `{"version": "1.37.1", "cni": "flannel"}`

// Images is the images block of a Cluster document on Image, a YAML flow mapping on one line. Each
// reference names a digest, so chalkctl warns of none.
const Images = `{apiServer: "registry.k8s.io/kube-apiserver:v1.37.1@sha256:e190f914a6cc21cab9268485d1ae7282fdeab1315ac64b668aa42d732fd1061d", ` +
	`controllerManager: "registry.k8s.io/kube-controller-manager:v1.37.1@sha256:d470c1b85aebb466e971c06239cc6a31a4ac337a78600cef80375a79a1be1641", ` +
	`scheduler: "registry.k8s.io/kube-scheduler:v1.37.1@sha256:b6e2474a6c20309f0df1e7337364281fda95c6a0722259ba7f032a1e177d3f29", ` +
	`proxy: "registry.k8s.io/kube-proxy:v1.37.1@sha256:b9ae667ed9c160c9d5b66f55e8afec97fc5483a12db0ada5286541b0385cb9f4", ` +
	`etcd: "registry.k8s.io/etcd:3.7.0-0@sha256:6ecefbe2510c4a30573a62a4d6dd175acf881ca67003fcd91849a16df7a724d5", ` +
	`coredns: "registry.k8s.io/coredns/coredns:v1.14.6@sha256:900f9c109f7a33545d3c811516e8376df9019147b750f5ce3e254468769176ea", ` +
	`pause: "registry.k8s.io/pause:3.10.2@sha256:f548e0e8e3dc1896ca956272154dde3314e8cc4fde0a57577ee9fa1c63f5baf4", ` +
	`flannel: "ghcr.io/flannel-io/flannel:v0.28.9@sha256:708a2c9c1cfbfe529d1dbb249cb7113af117f3ed26b5e3e4c8ee015e10555204"}`

// NodeCA returns a new node CA of the OS CA.
func NodeCA(t *testing.T) pki.CertKey {
	t.Helper()
	osCA, err := OSCA()
	if err != nil {
		t.Fatal(err)
	}
	nodeCA, err := pki.NewNodeCA(osCA, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return nodeCA
}

// Share is a new cluster's share for the node of the kind and name, with a node CA of the OS CA.
func Share(t *testing.T, kind, nodeName string) []byte {
	t.Helper()
	k, err := clustersecrets.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	share, err := kpki.ShareFor(&clustersecrets.Secrets{Kubernetes: *k, NodeCA: NodeCA(t)}, kind, nodeName, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := share.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Paths are the Kubernetes files of a test node: its STATE's below stateDir, the others below
// root.
func Paths(root, stateDir string) knode.Paths {
	return knode.Paths{
		StateDir:   filepath.Join(stateDir, "kubernetes"),
		Cluster:    filepath.Join(root, "etc", "cluster.json"),
		NodeFile:   filepath.Join(root, "run", "node.json"),
		RunDir:     filepath.Join(root, "run", "kubernetes"),
		PKI:        filepath.Join(root, "run", "kubernetes", "pki"),
		KubeletPKI: filepath.Join(root, "var", "lib", "kubelet", "pki"),
		EtcdData:   filepath.Join(root, "var", "lib", "etcd"),
	}
}

// ImageFile is the image.json of a test node, beside its cluster file.
func ImageFile(p knode.Paths) string { return filepath.Join(filepath.Dir(p.Cluster), "image.json") }

// ClusterFile is where a test node's config loader writes the cluster, which its Kubernetes layer
// does not read unless the test says so.
func ClusterFile(root string) string { return filepath.Join(root, "run", "chalkos", "cluster.json") }

// WriteNode writes the files of node n1 of the kind, whose cluster's endpoint is
// https://192.168.100.11:6443; with a share, which the preparation took, when share is set.
func WriteNode(t *testing.T, p knode.Paths, kind string, share bool) {
	t.Helper()
	write(t, p.Cluster, kubetest.JSON(t, kubetest.Cluster(kind, kubetest.IPv4Only, func(c *k8s.Cluster) {
		c.Endpoint = "https://192.168.100.11:6443"
		c.ExtraArgs = map[string]map[string]string{}
	})))
	write(t, p.NodeFile, `{"hostname": "n1", "layers": {"kubernetes": {"nodeName": "n1", "nodeIPs": ["192.168.100.11"]}}}`)
	if share {
		write(t, p.Share(), string(Share(t, kind, "n1")))
		if err := prepare(p, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// prepare runs the preparation of the node at 192.168.100.11 with the firewall.
func prepare(p knode.Paths, firewall knode.Firewall) error {
	return knode.State{Paths: p}.Prepare(time.Now(), NodeIP("192.168.100.11"), firewall)
}

// ReadyNode is a Ready Node with the pod ranges.
func ReadyNode(podCIDRs ...string) *corev1.Node {
	return &corev1.Node{
		Spec:   corev1.NodeSpec{PodCIDRs: podCIDRs},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}
}

// NodeIP is a resolver that finds the address at once.
func NodeIP(ip string) knode.Resolver {
	return func(nodeip.Selector, time.Duration) ([]nodeip.Address, error) {
		return []nodeip.Address{{Interface: "eth0", IP: netip.MustParseAddr(ip)}}, nil
	}
}

// WithoutAddress leaves the node's files as a preparation that found no address leaves them, for
// the reason given; "" stands for a preparation still waiting. Neither marks the node prepared.
func WithoutAddress(t *testing.T, p knode.Paths, reason string) {
	t.Helper()
	for _, f := range []string{p.PreparedMarker(), p.NodeIP()} {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	if reason != "" {
		write(t, p.PrepareError(), reason+"\n")
	}
}

// OtherKindsShare gives the node a share for another kind of node than its cluster file's and
// runs the preparation, which refuses it.
func OtherKindsShare(t *testing.T, p knode.Paths) {
	t.Helper()
	c, err := k8s.ReadCluster(p.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	other := k8s.KindWorker
	if c.Kind == k8s.KindWorker {
		other = k8s.KindControlPlane
	}
	write(t, p.Share(), string(Share(t, other, "n1")))
	if err := prepare(p, nil); err == nil {
		t.Fatal("prepared a share of another kind")
	}
}

// WithoutFirewall runs the preparation of the node, which has a share, with a firewall that cannot
// accept VXLAN.
func WithoutFirewall(t *testing.T, p knode.Paths) {
	t.Helper()
	fail := func(knode.Paths) error { return errors.New("accept VXLAN to the node's address: exit status 1") }
	if err := prepare(p, fail); err == nil {
		t.Fatal("prepared without the firewall's VXLAN rule")
	}
}

// Bootstrap asks the Kubernetes layer k to bootstrap the cluster.
func Bootstrap(ctx context.Context, k kubernetesv1connect.KubernetesServiceHandler) (*kubernetesv1.BootstrapResponse, error) {
	resp, err := k.Bootstrap(ctx, connect.NewRequest(&kubernetesv1.BootstrapRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Status is the state of the node's Kubernetes as the layer k serves it.
func Status(t *testing.T, k kubernetesv1connect.KubernetesServiceHandler) *kubernetesv1.StatusResponse {
	t.Helper()
	resp, err := k.Status(context.Background(), connect.NewRequest(&kubernetesv1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg
}

// ChalkdServing stands for chalkd: a TLS server that completes handshakes with the node's
// certificate, whose file it returns with its address.
func ChalkdServing(t *testing.T) (addr, cert string) {
	t.Helper()
	self, err := pki.SelfSigned("n1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert = filepath.Join(t.TempDir(), "node.pem")
	write(t, cert, self.Certificate+self.Key)
	pair, err := tls.X509KeyPair([]byte(self.Certificate), []byte(self.Key))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), cert
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

// The stand-ins for the hooks of a test node's Kubernetes layer, which reach no cluster.

// NoObjects is a control plane with no objects to apply.
func NoObjects(k8s.Cluster) ([]map[string]any, error) {
	return nil, errors.New("no objects in this test")
}

// AppliedAtOnce is a control plane's loop that applies 19 objects at once.
func AppliedAtOnce(_ context.Context, _ kpki.Share, applied func(int)) error {
	applied(19)
	return nil
}

// NodeReady is an API server whose Node of the node is Ready.
func NodeReady(context.Context) (*corev1.Node, error) { return ReadyNode(), nil }

// NoPods is an API server that lists no pods on the node.
func NoPods(context.Context, string) ([]corev1.Pod, error) { return nil, nil }

// NoEtcdEndpoints is a cluster endpoint at which no API server answers.
func NoEtcdEndpoints(context.Context, k8s.Cluster, kpki.Share, []netip.Addr) ([]string, error) {
	return nil, errors.New("connection refused")
}

// NoClusterAnswers is a cluster endpoint at which no API server answers.
func NoClusterAnswers(context.Context, k8s.Cluster, kpki.Share) (bool, error) { return false, nil }

// ControlPlaneCurrent is a control plane that runs with the certificates the node holds.
func ControlPlaneCurrent(context.Context, k8s.Cluster, kpki.Share) error { return nil }

// EmptyAPIServer is an API server that keeps no objects.
func EmptyAPIServer(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) {
	return fake.NewClientset(), nil
}
