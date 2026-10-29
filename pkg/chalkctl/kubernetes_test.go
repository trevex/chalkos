package chalkctl

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/layers"
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

func TestInstallSendsShareOfTheNodeKind(t *testing.T) {
	ta := newTestApp(t)
	got, err := installShare(t, ta)
	if err != nil {
		t.Fatal(err)
	}
	if got.KubernetesShare != nil {
		t.Error("a node whose image has no Kubernetes got a share")
	}

	withKind(t, ta, k8s.KindWorker)
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

	withKind(t, ta, k8s.KindControlPlane)
	if got, err = installShare(t, ta); err != nil {
		t.Fatal(err)
	}
	if share, err = kpki.ParseShare(got.KubernetesShare); err != nil || share.Kind != "controlplane" || share.CA != ta.secrets.Kubernetes.CA {
		t.Errorf("control-plane share: %v", err)
	}
}

// kubernetesNode is an installed worker node whose Kubernetes side reports the node
// ready.
func kubernetesNode(t *testing.T, ta *testApp) (*chalkd.Server, *fakeRunner) {
	t.Helper()
	return kubernetesNodeWith(t, ta, chalkd.KubernetesOptions{})
}

// kubernetesNodeWith is kubernetesNode with the Kubernetes side o sets up. The paths and the
// hooks o leaves nil are kubernetesNode's.
func kubernetesNodeWith(t *testing.T, ta *testApp, o chalkd.KubernetesOptions) (*chalkd.Server, *fakeRunner) {
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
	s.Kubernetes = chalkd.NewKubernetes(o)
	withPlugins(t, s, layers.Of(layers.Kubernetes), nil)
	// The loops a test started would run on against its removed files until the test binary ends.
	t.Cleanup(s.Kubernetes.Stop)
	kubernetesImage(t, s)
	// The cluster file chalkd writes from the node's config.
	s.Loader.ClusterFile = s.Kubernetes.Paths.Cluster
	writeFile(t, s.Kubernetes.Paths.Cluster, clusterFile("worker"))
	// The facts the kubelet's restart rules read, as the image declares them.
	writeFile(t, s.Paths.Facts, `{"recorded": {"file": "`+filepath.Join(s.Paths.StateDir, "storage", "storage.json")+`"},
	  "share": {"file": "`+s.Kubernetes.Paths.Share()+`", "fields": ["kind"], "hashOthers": true},
	  "cluster": {"file": "`+s.Loader.ClusterFile+`"}}`)
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
	for want, k := range map[string]*kubernetesv1.StatusResponse{
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

// TestBootLines names each image by its root hash, with the tag and revision the node knows.
func TestBootLines(t *testing.T) {
	running := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x81, 0xbd}, 16), Generation: 7, Revision: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Tag: "v1.4.0"}
	next := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x3f, 0xa9}, 16), Generation: 8, Tag: "v1.5.0"}
	for _, tc := range []struct {
		boot *nodev1.BootStatus
		want []string
	}{
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true}, []string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7+2-1.efi"}, []string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7+2-1.efi, not found healthy yet"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Staged: next},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) installed; it boots next"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Failed: next, Journal: []string{"a", "b"}},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) failed: rolled back to 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6)", "  a", "  b"}},
		{&nodev1.BootStatus{Running: &nodev1.BootImage{RootHash: next.RootHash}, Error: "no UKI on the ESP boots the running store"}, []string{"image 3fa93fa93fa9; its boot is unknown: no UKI on the ESP boots the running store"}},
		{nil, nil},
	} {
		if got := bootLines(tc.boot); !slices.Equal(got, tc.want) {
			t.Errorf("bootLines(%v) = %q, want %q", tc.boot, got, tc.want)
		}
	}
}

// TestImageName names an image by its root hash's first 12 hex digits, then the tag and revision
// it was built from as far as they are known; a dirty revision says so.
func TestImageName(t *testing.T) {
	root := bytes.Repeat([]byte{0x3f, 0xa9}, 16)
	for _, tc := range []struct {
		revision, tag, want string
	}{
		{"", "", "3fa93fa93fa9"},
		{"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "v1.5.0", "3fa93fa93fa9 (v1.5.0, a1b2c3d4e5f6)"},
		{"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "", "3fa93fa93fa9 (a1b2c3d4e5f6)"},
		{"", "v1.5.0", "3fa93fa93fa9 (v1.5.0)"},
		{"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678-dirty", "", "3fa93fa93fa9 (a1b2c3d4e5f6-dirty)"},
		{"abc", "", "3fa93fa93fa9 (abc)"},
	} {
		if got := imageName(root, tc.revision, tc.tag); got != tc.want {
			t.Errorf("imageName(%q, %q) = %q, want %q", tc.revision, tc.tag, got, tc.want)
		}
	}
	if got := imageName(nil, "", ""); got != "unknown" {
		t.Errorf("the name of an image of no root hash = %q", got)
	}
}
