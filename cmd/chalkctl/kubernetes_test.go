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
		n.Identity.Kubernetes = &manifest.KubernetesIdentity{NodeName: "n1", NodeIP: "10.0.0.11"}
		m.Nodes["n1"] = n
	})
}

// versionOne replaces the test cluster's secrets file with one from before the Kubernetes
// secrets.
func versionOne(t *testing.T, ta *testApp) pki.Secrets {
	t.Helper()
	old := ta.secrets
	old.Version = 1
	old.Kubernetes = nil
	data, err := old.Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ta.dir, "secrets.json"), string(data))
	return old
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

func TestInstallRefusesVersionOneSecretsForKubernetes(t *testing.T) {
	ta := newTestApp(t)
	versionOne(t, ta)
	// Nodes without Kubernetes install from a version 1 file as before.
	if _, err := installShare(t, ta); err != nil {
		t.Fatal(err)
	}
	withKind(t, ta, manifest.KindWorker)
	if _, err := installShare(t, ta); err == nil || !strings.Contains(err.Error(), "chalkctl secrets upgrade") {
		t.Errorf("err = %v, want a refusal naming chalkctl secrets upgrade", err)
	}
}

func TestSecretsUpgrade(t *testing.T) {
	ta := newTestApp(t)
	old := versionOne(t, ta)
	out := filepath.Join(ta.dir, "secrets.v2.json")
	pub := filepath.Join(ta.dir, "secrets.v2.pub.json")
	args := []string{"secrets", "upgrade", "--flake", ta.dir, "--plaintext", "--out", out, "--public-out", pub}
	if err := ta.run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	s, err := pki.ReadSecrets(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 2 || s.Kubernetes == nil || s.OSCA != old.OSCA || s.Admin != old.Admin || string(s.RecoverySecret) != string(old.RecoverySecret) {
		t.Error("the upgraded file lost or changed secrets")
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
	var p pki.Public
	pubData, _ := os.ReadFile(pub)
	if err := json.Unmarshal(pubData, &p); err != nil || p.Kubernetes == nil || p.Kubernetes.CA.Certificate != s.Kubernetes.CA.Certificate || strings.Contains(string(pubData), "PRIVATE") {
		t.Errorf("public file = %s, %v", pubData, err)
	}
	if err := ta.run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("err = %v, want a refusal to overwrite", err)
	}
	if err := ta.run(context.Background(), []string{"secrets", "upgrade", "--flake", ta.dir, "--plaintext"}); err == nil || !strings.Contains(err.Error(), "--out") {
		t.Errorf("err = %v, want --out required", err)
	}
	// The upgraded file upgrades no further.
	if err := ta.run(context.Background(), []string{"secrets", "upgrade", "--secrets", out, "--plaintext", "--out", filepath.Join(ta.dir, "v3.json")}); err == nil {
		t.Error("upgraded a version 2 file")
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
		},
		NodeReady: func(context.Context) (string, error) { return "True", nil },
	}
	writeFile(t, s.Kubernetes.Paths.Cluster, `{"kind": "worker", "endpoint": "https://10.0.0.10:6443", "podCIDR": "10.244.0.0/16",
	  "serviceCIDR": "10.96.0.0/12", "dnsIP": "10.96.0.10", "domain": "cluster.local"}`)
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
	versionOne(t, ta)
	if err := ta.run(context.Background(), append(args, "--force")); err == nil || !strings.Contains(err.Error(), "chalkctl secrets upgrade") {
		t.Errorf("err = %v, want a refusal naming chalkctl secrets upgrade", err)
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
	if !strings.Contains(ta.stdout.String(), "kubernetes worker: joined, node ready: True") {
		t.Errorf("status = %q", ta.stdout)
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
