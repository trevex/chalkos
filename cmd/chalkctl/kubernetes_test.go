package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/install"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// withKind makes n1's role a Kubernetes role of the kind.
func withKind(t *testing.T, ta *testApp, kind string) {
	t.Helper()
	ta.editManifest(t, func(m *manifest.Manifest) {
		r := m.Roles["test"]
		r.Kind = kind
		m.Roles["test"] = r
		n := m.Nodes["n1"]
		n.Identity.Kubernetes = &manifest.KubernetesIdentity{NodeName: "n1", NodeIPs: []string{"10.0.0.11"}}
		m.Nodes["n1"] = n
	})
}

func installShare(t *testing.T, ta *testApp) (install.Request, error) {
	t.Helper()
	s := maintenanceNode()
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request) error { got = req; return nil }
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--insecure"}, addr))
	return got, err
}

func TestInstallSendsShareOfTheRole(t *testing.T) {
	ta := newTestApp(t)
	got, err := installShare(t, ta)
	if err != nil {
		t.Fatal(err)
	}
	if got.KubernetesShare != nil {
		t.Error("a node of a role without Kubernetes got a share")
	}

	withKind(t, ta, manifest.KindWorker)
	if got, err = installShare(t, ta); err != nil {
		t.Fatal(err)
	}
	share, err := kpki.ParseShare(got.KubernetesShare)
	if err != nil {
		t.Fatal(err)
	}
	if share.Kind != "worker" || share.Node() != "n1" || share.CA.Key != "" || share.CA.Certificate != ta.secrets.Kubernetes.CA.Certificate {
		t.Errorf("worker share for %q of kind %s", share.Node(), share.Kind)
	}

	withKind(t, ta, manifest.KindControlPlane)
	if got, err = installShare(t, ta); err != nil {
		t.Fatal(err)
	}
	if share, err = kpki.ParseShare(got.KubernetesShare); err != nil || share.Kind != "controlplane" || share.CA != ta.secrets.Kubernetes.CA {
		t.Errorf("control-plane share: %v", err)
	}
}

// kubernetesNode is an installed node of a worker role whose Kubernetes side reports the node
// ready.
func kubernetesNode(t *testing.T, ta *testApp) (*chalkd.Server, *fakeRunner) {
	t.Helper()
	current, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	s, r := installedNode(t, ta, current)
	root := t.TempDir()
	s.Kubernetes = &chalkd.Kubernetes{
		Paths: knode.Paths{
			State:    filepath.Join(s.Paths.StateDir, "kubernetes"),
			Cluster:  filepath.Join(root, "cluster.json"),
			NodeFile: filepath.Join(root, "node.json"),
			Run:      filepath.Join(root, "run"),
		},
		Node: func(context.Context) (*corev1.Node, error) {
			return &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}, nil
		},
	}
	// The loops a test started would run on against its removed files until the test binary ends.
	t.Cleanup(s.Kubernetes.Stop)
	writeFile(t, s.Kubernetes.Paths.Cluster, `{"kind": "worker", "endpoint": "https://10.0.0.10:6443",
	  "podCIDRs": {"ipv4": "10.244.0.0/16"}, "serviceCIDRs": {"ipv4": "10.96.0.0/12"},
	  "dnsIPs": {"ipv4": "10.96.0.10"}, "nodeCIDRMaskSizes": {"ipv4": 24}, "domain": "cluster.local"}`)
	return s, r
}

func TestApplyIdentityDeliversShare(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, manifest.KindWorker)
	s, r := kubernetesNode(t, ta)
	addr := ta.startNode(t, s)

	if err := ta.run(context.Background(), ta.args([]string{"apply-identity", "n1", "--kubernetes-share"}, addr)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.Kubernetes.Paths.Share())
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
	if err := ta.run(context.Background(), ta.args([]string{"apply-identity", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Error("apply-identity without --kubernetes-share restarted the kubelet")
	}
}
func TestKubeconfig(t *testing.T) {
	ta := newTestApp(t)
	out := filepath.Join(ta.dir, "kubeconfig")
	args := []string{"kubeconfig", "--manifest", filepath.Join(ta.dir, "manifest.json"), "--flake", ta.dir, "--out", out, "--server", "https://127.0.0.1:16443", "--ttl", "1h"}
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

func TestStatusShowsKubernetes(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, manifest.KindWorker)
	s, _ := kubernetesNode(t, ta)
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "kubernetes worker: no share") {
		t.Errorf("status without share = %q", ta.stdout)
	}
	if err := ta.run(context.Background(), ta.args([]string{"apply-identity", "n1", "--kubernetes-share"}, addr)); err != nil {
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
	writeFile(t, s.Kubernetes.Paths.NodeIP(), "10.0.0.11\n")
	writeFile(t, s.Kubernetes.Paths.Prepared(), "")
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "kubernetes worker: joined, node ready: True") {
		t.Errorf("status = %q", ta.stdout)
	}
}

func TestKubernetesLine(t *testing.T) {
	for want, k := range map[string]*nodev1.KubernetesStatus{
		"kubernetes worker: joined, node ready: True":                                    {Kind: "worker", State: "joined", NodeReady: "True"},
		"kubernetes controlplane: bootstrapped, node ready: True, vip holder":            {Kind: "controlplane", State: "bootstrapped", NodeReady: "True", Vip: "holder"},
		"kubernetes controlplane: preparing":                                             {Kind: "controlplane", State: "preparing"},
		"kubernetes controlplane: bootstrapped, node ready: True, control plane current": {Kind: "controlplane", State: "bootstrapped", NodeReady: "True", ControlPlane: "current"},
	} {
		if got := kubernetesLine(k); got != want {
			t.Errorf("kubernetesLine(%v) = %q, want %q", k, got, want)
		}
	}
}

func TestBootstrapNeedsControlPlane(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, manifest.KindWorker)
	s, _ := kubernetesNode(t, ta)
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"bootstrap", "n1"}, addr)); err == nil || !strings.Contains(err.Error(), "not a control-plane node") {
		t.Errorf("err = %v, want a refusal for a worker", err)
	}
	// A control-plane role whose node runs no Kubernetes: the node refuses.
	withKind(t, ta, manifest.KindControlPlane)
	plain, _ := installedNode(t, ta, []byte(`{}`))
	addr = ta.startNode(t, plain)
	if err := ta.run(context.Background(), ta.args([]string{"bootstrap", "n1"}, addr)); err == nil || !strings.Contains(err.Error(), "has no Kubernetes") {
		t.Errorf("err = %v, want the node's refusal", err)
	}
}

// TestKubeconfigTrustsBothCAs checks that a kubeconfig issued while the Kubernetes CAs rotate
// trusts both, so it keeps verifying the API server once the new CA issues its certificate, and
// that its client certificate comes from the new CA once every node accepts it, so the finish
// does not refuse it.
func TestKubeconfigTrustsBothCAs(t *testing.T) {
	ta := newTestApp(t)
	ta.beginRotation(t, pki.RotateKubernetesCA)
	issue := func() (ca []byte, client *x509.Certificate) {
		t.Helper()
		out := filepath.Join(t.TempDir(), "kubeconfig")
		if err := ta.run(context.Background(), []string{"kubeconfig", "--manifest", filepath.Join(ta.dir, "manifest.json"), "--flake", ta.dir, "--out", out}); err != nil {
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
