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
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// OSCA is the OS CA every test node trusts.
var OSCA = sync.OnceValues(func() (pki.CertKey, error) { return pki.NewOSCA(time.Now()) })

// Image is the image.json of a Kubernetes image without flannel.
const Image = `{"version": "1.37.1", "cni": "none", "images": {"etcd": "e", "kubeAPIServer": "a",
  "kubeControllerManager": "c", "kubeScheduler": "s", "kubeProxy": "p", "coredns": "d", "flannel": ""}}`

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
		State:      filepath.Join(stateDir, "kubernetes"),
		Cluster:    filepath.Join(root, "etc", "cluster.json"),
		NodeFile:   filepath.Join(root, "run", "node.json"),
		Run:        filepath.Join(root, "run", "kubernetes"),
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
	write(t, p.Cluster, `{"kind": "`+kind+`", "endpoint": "https://192.168.100.11:6443", "version": "1.37.1",
	  "podCIDRs": {"ipv4": "10.244.0.0/16"}, "serviceCIDRs": {"ipv4": "10.96.0.0/12"},
	  "dnsIPs": {"ipv4": "10.96.0.10"}, "nodeCIDRMaskSizes": {"ipv4": 24}, "domain": "cluster.local",
	  "extraArgs": {}, "images": {"etcd": "e", "kubeAPIServer": "a", "kubeControllerManager": "c", "kubeScheduler": "s"}}`)
	write(t, p.NodeFile, `{"hostname": "n1", "kubernetes": {"nodeName": "n1", "nodeIPs": ["192.168.100.11"]}}`)
	if share {
		write(t, p.Share(), string(Share(t, kind, "n1")))
		if err := knode.Prepare(p, time.Now(), NodeIP("192.168.100.11"), nil); err != nil {
			t.Fatal(err)
		}
	}
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
	for _, f := range []string{p.Prepared(), p.NodeIP()} {
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
	if err := knode.Prepare(p, time.Now(), NodeIP("192.168.100.11"), nil); err == nil {
		t.Fatal("prepared a share of another kind")
	}
}

// WithoutFirewall runs the preparation of the node, which has a share, with a firewall that cannot
// accept VXLAN.
func WithoutFirewall(t *testing.T, p knode.Paths) {
	t.Helper()
	fail := func(knode.Paths) error { return errors.New("accept VXLAN to the node's address: exit status 1") }
	if err := knode.Prepare(p, time.Now(), NodeIP("192.168.100.11"), fail); err == nil {
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

// NoEtcdEndpoints is a cluster endpoint at which no API server answers.
func NoEtcdEndpoints(context.Context, k8s.Cluster, kpki.Share, []netip.Addr) ([]string, error) {
	return nil, errors.New("connection refused")
}

// NoClusterAnswers is a cluster endpoint at which no API server answers.
func NoClusterAnswers(context.Context, k8s.Cluster, kpki.Share) bool { return false }

// ControlPlaneCurrent is a control plane that runs with the certificates the node holds.
func ControlPlaneCurrent(context.Context, k8s.Cluster, kpki.Share) error { return nil }

// EmptyAPIServer is an API server that keeps no objects.
func EmptyAPIServer(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) {
	return fake.NewClientset(), nil
}
