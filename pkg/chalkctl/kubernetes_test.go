package chalkctl

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/pki"
)

// withKind makes n1 a Kubernetes node of the kind, on an image with Kubernetes.
func withKind(t *testing.T, ta *testApp, kind string) {
	t.Helper()
	ta.writeNode(t, nodeDoc(kind), n1SystemDisk, n1Network)
	writeImageDir(t, filepath.Join(ta.dir, "images", "test-metal"), "test", "metal", testRootHash, true)
	ta.kind = kind
}

func installShare(t *testing.T, ta *testApp) (install.Request, error) {
	t.Helper()
	s := maintenanceNode()
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request, _ []byte) error { got = req; return nil }
	if ta.kind != "" {
		kubernetesImage(t, s)
	}
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--insecure"}, addr))
	return got, err
}

// shareOf is the share an install request has its layers write to STATE; nil when they write
// none.
func shareOf(t *testing.T, req install.Request) []byte {
	t.Helper()
	state := t.TempDir()
	for _, write := range req.Layers {
		if err := write(state); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(state, "kubernetes", "share.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInstallSendsShareOfTheNodeKind(t *testing.T) {
	ta := newTestApp(t)
	got, err := installShare(t, ta)
	if err != nil {
		t.Fatal(err)
	}
	if shareOf(t, got) != nil {
		t.Error("a node whose image has no Kubernetes got a share")
	}

	withKind(t, ta, k8s.KindWorker)
	if got, err = installShare(t, ta); err != nil {
		t.Fatal(err)
	}
	share, err := kpki.ParseShare(shareOf(t, got))
	if err != nil {
		t.Fatal(err)
	}
	if share.Kind != "worker" || share.Node() != "n1" || share.CA.Key != "" || share.CA.Certificate != ta.secrets.Kubernetes.CA.Certificate {
		t.Errorf("worker share for %q of kind %s", share.Node(), share.Kind)
	}

	withKind(t, ta, k8s.KindControlPlane)
	if got, err = installShare(t, ta); err != nil {
		t.Fatal(err)
	}
	if share, err = kpki.ParseShare(shareOf(t, got)); err != nil || share.Kind != "controlplane" || share.CA != ta.secrets.Kubernetes.CA {
		t.Errorf("control-plane share: %v", err)
	}
}

// kubernetesNode is an installed worker node whose Kubernetes side reports the node
// ready.
func kubernetesNode(t *testing.T, ta *testApp) (*chalkd.Server, *fakeRunner) {
	t.Helper()
	return kubernetesNodeWith(t, ta, kubernetesplugin.Options{})
}

// kubernetesNodeWith is kubernetesNode with the Kubernetes side o sets up. The paths and the
// hooks o leaves nil are kubernetesNode's.
func kubernetesNodeWith(t *testing.T, ta *testApp, o kubernetesplugin.Options) (*chalkd.Server, *fakeRunner) {
	t.Helper()
	current, _ := json.Marshal(ta.identity(t))
	s, r := installedNode(t, ta, current)
	root := t.TempDir()
	o.Paths = knode.Paths{
		State:    filepath.Join(s.Paths.StateDir, "kubernetes"),
		Cluster:  filepath.Join(root, "cluster.json"),
		NodeFile: filepath.Join(root, "node.json"),
		Run:      filepath.Join(root, "run"),
	}
	if o.Hooks.Node == nil {
		o.Hooks.Node = func(context.Context) (*corev1.Node, error) {
			return &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}, nil
		}
	}
	if o.Hooks.APIClient == nil {
		// The API server keeps no objects.
		o.Hooks.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return fake.NewClientset(), nil }
	}
	if o.Hooks.CheckControlPlane == nil {
		// The control plane runs with the certificates the node holds.
		o.Hooks.CheckControlPlane = func(context.Context, k8s.Cluster, kpki.Share) error { return nil }
	}
	o.ImageFile = imageJSON(t)
	o.ClusterFile = o.Paths.Cluster
	k := kubernetesplugin.New(s, o)
	withPlugins(t, s, layers.Of(layers.Kubernetes), k)
	writeEffects(t, s)
	// The loops a test started would run on against its removed files until the test binary ends.
	t.Cleanup(k.Stop)
	// The loader has the Kubernetes layer write the cluster file.
	s.Loader.Layers = layer.Active[nodeconfig.Layer](s.Plugins)
	// The cluster file chalkd writes from the node's config.
	writeFile(t, kubernetesPaths(s).Cluster, clusterFile("worker"))
	// The facts the kubelet's restart rules read, as the image declares them.
	writeFile(t, s.Paths.Facts, `{"recorded": {"file": "`+filepath.Join(s.Paths.StateDir, "storage", "storage.json")+`"},
	  "share": {"file": "`+kubernetesPaths(s).Share()+`", "fields": ["kind"], "hashOthers": true},
	  "cluster": {"file": "`+kubernetesPaths(s).Cluster+`"}}`)
	return s, r
}

func TestApplyConfigDeliversShare(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, k8s.KindWorker)
	s, r := kubernetesNode(t, ta)
	addr := ta.startNode(t, s)

	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1", "--kubernetes-share"}, addr)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(kubernetesPaths(s).Share())
	if err != nil {
		t.Fatal(err)
	}
	if share, err := kpki.ParseShare(data); err != nil || share.Node() != "n1" {
		t.Errorf("share on the node: %v", err)
	}
	if !slices.Contains(r.calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("node ran %v", r.calls)
	}

	// Without --kubernetes-share the node keeps its share.
	r.calls = nil
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Error("apply-config without --kubernetes-share restarted the kubelet")
	}
}
func TestKubeconfig(t *testing.T) {
	ta := newTestApp(t)
	out := filepath.Join(ta.dir, "kubeconfig")
	args := []string{"kubeconfig", "--inventory", ta.inventory, "--out", out, "--server", "https://127.0.0.1:16443", "--ttl", "1h"}
	if err := ta.run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("kubeconfig: %v, %v", info, err)
	}
	data, _ := os.ReadFile(out)
	var kc struct {
		Clusters []struct {
			Name    string `json:"name"`
			Cluster struct {
				Server     string `json:"server"`
				ServerName string `json:"tls-server-name"`
			} `json:"cluster"`
		} `json:"clusters"`
		Users []struct {
			User struct {
				Cert []byte `json:"client-certificate-data"`
			} `json:"user"`
		} `json:"users"`
	}
	if err := json.Unmarshal(data, &kc); err != nil {
		t.Fatal(err)
	}
	c := kc.Clusters[0]
	if c.Name != "lab" || c.Cluster.Server != "https://127.0.0.1:16443" || c.Cluster.ServerName != "10.0.0.10" {
		t.Errorf("cluster = %+v", c)
	}
	cert, err := pki.ParseCertificate(kc.Users[0].User.Cert)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := pki.ParseCertificate([]byte(ta.secrets.Kubernetes.CA.Certificate))
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("admin certificate: %v", err)
	}
	if !slices.Equal(cert.Subject.Organization, []string{"chalkos:cluster-admins"}) || cert.NotAfter.Sub(cert.NotBefore).Hours() > 2 {
		t.Errorf("admin certificate %v until %v", cert.Subject, cert.NotAfter)
	}
	if err := ta.run(context.Background(), args); err == nil {
		t.Error("replaced an existing kubeconfig without --force")
	}
	if err := ta.run(context.Background(), append(args, "--force")); err != nil {
		t.Errorf("--force: %v", err)
	}
}

// TestKubeconfigContext checks that the kubeconfig's cluster, user and context are named after
// the inventory unless --context names them.
func TestKubeconfigContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"inventory", nil, "lab"},
		{"flag", []string{"--context", "home@lab"}, "home@lab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t)
			out := filepath.Join(ta.dir, "kubeconfig")
			args := append([]string{"kubeconfig", "--inventory", ta.inventory, "--out", out}, tc.args...)
			if err := ta.run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(out)
			type named struct {
				Name string `json:"name"`
			}
			var kc struct {
				Clusters []named `json:"clusters"`
				Users    []named `json:"users"`
				Contexts []struct {
					Name    string `json:"name"`
					Context struct {
						Cluster string `json:"cluster"`
						User    string `json:"user"`
					} `json:"context"`
				} `json:"contexts"`
				Current string `json:"current-context"`
			}
			if err := json.Unmarshal(data, &kc); err != nil {
				t.Fatal(err)
			}
			if len(kc.Clusters) != 1 || kc.Clusters[0].Name != tc.want || len(kc.Users) != 1 || kc.Users[0].Name != tc.want ||
				len(kc.Contexts) != 1 || kc.Contexts[0].Name != tc.want || kc.Contexts[0].Context.Cluster != tc.want ||
				kc.Contexts[0].Context.User != tc.want || kc.Current != tc.want {
				t.Errorf("kubeconfig names %+v, %+v, %+v, %q; want %s", kc.Clusters, kc.Users, kc.Contexts, kc.Current, tc.want)
			}
		})
	}
}

// TestKubeconfigNeedsAnEndpoint refuses an inventory whose nodes' Cluster documents name no
// endpoint, as there is none to point clients at.
func TestKubeconfigNeedsAnEndpoint(t *testing.T) {
	ta := newTestApp(t)
	writeFile(t, filepath.Join(ta.dir, "cluster.yaml"), "")
	err := ta.run(context.Background(), []string{"kubeconfig", "--inventory", ta.inventory, "--out", "-"})
	if err == nil || !strings.Contains(err.Error(), "no Cluster document of the cluster lab names an endpoint") {
		t.Errorf("kubeconfig without an endpoint = %v", err)
	}
}

func TestKubeconfigRefusesEmptyContext(t *testing.T) {
	ta := newTestApp(t)
	err := ta.run(context.Background(), []string{"kubeconfig", "--inventory", ta.inventory, "--out", "-", "--context", ""})
	if err == nil || !strings.Contains(err.Error(), "--context") {
		t.Errorf("kubeconfig --context '': %v", err)
	}
}

func TestStatusShowsKubernetes(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, k8s.KindWorker)
	s, _ := kubernetesNode(t, ta)
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "kubernetes worker: no share") {
		t.Errorf("status without share = %q", ta.stdout)
	}
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1", "--kubernetes-share"}, addr)); err != nil {
		t.Fatal(err)
	}
	ta.stdout.Reset()
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "kubernetes worker: preparing") {
		t.Errorf("status before the preparation = %q", ta.stdout)
	}
	ta.stdout.Reset()
	// The address the node's preparation picks once it has a share, and its marker.
	writeFile(t, kubernetesPaths(s).NodeIP(), "10.0.0.11\n")
	writeFile(t, kubernetesPaths(s).Prepared(), "")
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "kubernetes worker: joined, node ready: True") {
		t.Errorf("status = %q", ta.stdout)
	}
}

func TestBootstrapNeedsControlPlane(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, k8s.KindWorker)
	s, _ := kubernetesNode(t, ta)
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"bootstrap", "n1"}, addr)); err == nil || !strings.Contains(err.Error(), "not a control-plane node") {
		t.Errorf("err = %v, want a refusal for a worker", err)
	}
	// A control-plane node whose image lacks the Kubernetes layer: the node refuses.
	withKind(t, ta, k8s.KindControlPlane)
	plain, _ := installedNode(t, ta, []byte(`{}`))
	addr = ta.startNode(t, plain)
	if err := ta.run(context.Background(), ta.args([]string{"bootstrap", "n1"}, addr)); err == nil || !strings.Contains(err.Error(), "the kubernetes layer is not in this image") {
		t.Errorf("err = %v, want the node's refusal", err)
	}
}

// TestKubeconfigTrustsBothCAs checks that a kubeconfig issued while the Kubernetes CAs rotate
// trusts both, so it keeps verifying the API server once the new CA issues its certificate, and
// that its client certificate comes from the new CA once every node accepts it, so the finish
// does not refuse it.
func TestKubeconfigTrustsBothCAs(t *testing.T) {
	ta := newTestApp(t)
	ta.beginRotation(t, clustersecrets.RotateKubernetesCA)
	issue := func() (ca []byte, client *x509.Certificate) {
		t.Helper()
		out := filepath.Join(t.TempDir(), "kubeconfig")
		if err := ta.run(context.Background(), []string{"kubeconfig", "--inventory", ta.inventory, "--out", out}); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(out)
		var kc struct {
			Clusters []struct {
				Cluster struct {
					CA []byte `json:"certificate-authority-data"`
				} `json:"cluster"`
			} `json:"clusters"`
			Users []struct {
				User struct {
					Certificate []byte `json:"client-certificate-data"`
				} `json:"user"`
			} `json:"users"`
		}
		if err := json.Unmarshal(data, &kc); err != nil {
			t.Fatal(err)
		}
		cert, err := pki.ParseCertificate(kc.Users[0].User.Certificate)
		if err != nil {
			t.Fatal(err)
		}
		return kc.Clusters[0].Cluster.CA, cert
	}
	old, _ := pki.ParseCertificate([]byte(ta.secrets.Kubernetes.CA.Certificate))
	next, _ := pki.ParseCertificate([]byte(ta.secrets.Rotation.New.CA.Certificate))

	ca, cert := issue()
	if string(ca) != ta.secrets.Kubernetes.CABundle() || strings.Count(string(ca), "BEGIN") != 2 {
		t.Error("the kubeconfig does not trust both Kubernetes CAs")
	}
	// Until every node accepts the new CA, the API server may not trust it yet.
	if cert.CheckSignatureFrom(old) != nil {
		t.Error("before the accept phase is applied, the client certificate is not of the issuing CA")
	}
	ta.secrets.Rotation.Applied = true
	data, _ := ta.secrets.Encode()
	writeFile(t, filepath.Join(ta.dir, "secrets.json"), string(data))
	if _, cert := issue(); cert.CheckSignatureFrom(next) != nil {
		t.Error("once the accept phase is applied, the client certificate is not of the new CA")
	}
}
