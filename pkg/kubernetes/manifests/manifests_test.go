package manifests

import (
	"encoding/json"
	"maps"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/trevex/chalkos/pkg/internal/golden"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/kubetest"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

func testCluster() kubernetes.Cluster {
	return kubetest.Cluster(kubernetes.KindControlPlane, func(c *kubernetes.Cluster) {
		c.ExtraArgs = map[string]map[string]string{
			"kube-apiserver": {"audit-log-maxage": "30", "kubelet-preferred-address-types": "InternalIP"},
		}
	})
}

var testNode = kubetest.Node("cp1", "10.0.0.11")

// testFiles stands for the certificates: their content only feeds the hashes.
func testFiles() map[string][]byte {
	files := map[string][]byte{}
	for _, name := range []string{
		kpki.FileCA, kpki.FileCASigning, kpki.FileCAKey, kpki.FileFrontProxyCA, kpki.FileFrontProxyClient, kpki.FileFrontProxyClientKey,
		kpki.FileAPIServer, kpki.FileAPIServerKey, kpki.FileAPIServerKubeletClient, kpki.FileAPIServerKubeletKey,
		kpki.FileAPIServerEtcdClient, kpki.FileAPIServerEtcdClientKey, kpki.FileServiceAccountKey, kpki.FileServiceAccountPub,
		kpki.FileEncryptionConfig, kpki.FileAuthenticationConfig, kpki.FileControllerManagerConfig, kpki.FileSchedulerConfig,
		kpki.FileEtcdCA, kpki.FileEtcdServer, kpki.FileEtcdServerKey, kpki.FileEtcdPeer, kpki.FileEtcdPeerKey,
	} {
		files[name] = []byte("content of " + name)
	}
	return files
}

// familySetups are the clusters of each setup of address families by the name of their golden
// files' directory, with the node's addresses, the primary family's first.
func familySetups() map[string]struct {
	c kubernetes.Cluster
	n kubernetes.Node
} {
	v4, v6 := netip.MustParseAddr("10.0.0.11"), netip.MustParseAddr("fd00::11")
	setups := map[string]struct {
		c kubernetes.Cluster
		n kubernetes.Node
	}{}
	for name, families := range map[string][]netip.Addr{"ipv4": {v4}, "ipv4-ipv6": {v4, v6}, "ipv6-ipv4": {v6, v4}, "ipv6": {v6}} {
		c := testCluster()
		for _, f := range strings.Split(name, "-") {
			c.IPFamilies = append(c.IPFamilies, nodeip.Family(f))
		}
		if strings.HasPrefix(name, "ipv6") {
			c.Endpoint = "https://[fd00::10]:6443"
		}
		setups[name] = struct {
			c kubernetes.Cluster
			n kubernetes.Node
		}{c, kubernetes.Node{Name: "cp1", IPs: families}}
	}
	return setups
}

func TestStaticPodsGolden(t *testing.T) {
	for setup, tc := range familySetups() {
		if err := tc.c.Validate(); err != nil {
			t.Fatalf("%s: %v", setup, err)
		}
		pods, err := StaticPods(tc.c, tc.n, kubernetes.PKIDir, testFiles(), "")
		if err != nil {
			t.Fatal(err)
		}
		names := slices.Sorted(maps.Keys(pods))
		if strings.Join(names, " ") != "etcd.json kube-apiserver.json kube-controller-manager.json kube-scheduler.json" {
			t.Fatalf("pods %v", names)
		}
		for _, name := range names {
			golden.Check(t, filepath.Join("testdata", setup, name), string(pods[name]))
		}
	}
}

func decode(t *testing.T, data []byte) corev1.Pod {
	t.Helper()
	var p corev1.Pod
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// A member joining an existing cluster starts with the cluster's members.
func TestEtcdJoiningExistingCluster(t *testing.T) {
	initial := "cp1=https://10.0.0.11:2380,cp2=https://10.0.0.12:2380"
	n := kubetest.Node("cp2", "10.0.0.12")
	pods, err := StaticPods(testCluster(), n, kubernetes.PKIDir, testFiles(), initial)
	if err != nil {
		t.Fatal(err)
	}
	flags := commandFlags(t, pods["etcd.json"])
	if flags["initial-cluster"] != initial || flags["initial-cluster-state"] != "existing" || flags["initial-advertise-peer-urls"] != "https://10.0.0.12:2380" {
		t.Errorf("etcd flags %v", flags)
	}
	pods, err = StaticPods(testCluster(), testNode, kubernetes.PKIDir, testFiles(), "")
	if err != nil {
		t.Fatal(err)
	}
	if flags := commandFlags(t, pods["etcd.json"]); flags["initial-cluster"] != "cp1=https://10.0.0.11:2380" || flags["initial-cluster-state"] != "new" {
		t.Errorf("etcd flags of a new cluster %v", flags)
	}
}

func TestExtraArgsOverride(t *testing.T) {
	pods, err := StaticPods(testCluster(), testNode, kubernetes.PKIDir, testFiles(), "")
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(decode(t, pods["kube-apiserver.json"]).Spec.Containers[0].Command, " ")
	if !strings.Contains(cmd, "--kubelet-preferred-address-types=InternalIP ") || strings.Contains(cmd, "--kubelet-preferred-address-types=InternalIP,") ||
		!strings.Contains(cmd, "--audit-log-maxage=30") {
		t.Errorf("command %s", cmd)
	}
}

// commandFlags returns a pod's flags by name; a flag given twice fails the test.
func commandFlags(t *testing.T, data []byte) map[string]string {
	t.Helper()
	flags := map[string]string{}
	for _, arg := range decode(t, data).Spec.Containers[0].Command[1:] {
		name, value, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if _, ok := flags[name]; ok {
			t.Errorf("flag %s given twice", name)
		}
		flags[name] = value
	}
	return flags
}

func TestCertificatesHashFollowsMountedFiles(t *testing.T) {
	files := testFiles()
	before, err := StaticPods(testCluster(), testNode, kubernetes.PKIDir, files, "")
	if err != nil {
		t.Fatal(err)
	}
	files[kpki.FileEtcdServer] = []byte("reissued")
	after, err := StaticPods(testCluster(), testNode, kubernetes.PKIDir, files, "")
	if err != nil {
		t.Fatal(err)
	}
	changed := map[string]bool{}
	for name := range before {
		b := decode(t, before[name]).Annotations[CertificatesAnnotation]
		a := decode(t, after[name]).Annotations[CertificatesAnnotation]
		if a == "" {
			t.Errorf("%s has no certificates hash", name)
		}
		changed[name] = a != b
	}
	// etcd mounts its own certificates, the API server and the controller-manager the whole
	// directory, the scheduler only its kubeconfig.
	want := map[string]bool{"etcd.json": true, "kube-apiserver.json": true, "kube-controller-manager.json": true, "kube-scheduler.json": false}
	for name, w := range want {
		if changed[name] != w {
			t.Errorf("%s: hash changed %v, want %v", name, changed[name], w)
		}
	}
}

func TestStaticPodsNeedNodeIP(t *testing.T) {
	if _, err := StaticPods(testCluster(), kubernetes.Node{Name: "cp1"}, kubernetes.PKIDir, testFiles(), ""); err == nil {
		t.Error("rendered static pods without the node's address")
	}
}

// TestCertificatesHashFollowsBundles checks that a bundle that changes alone, as when a CA is
// accepted or no longer is, changes the hash of the pods that trust it, so they restart on it.
func TestCertificatesHashFollowsBundles(t *testing.T) {
	before, err := StaticPods(testCluster(), testNode, kubernetes.PKIDir, testFiles(), "")
	if err != nil {
		t.Fatal(err)
	}
	for file, pods := range map[string][]string{
		kpki.FileCA:           {"kube-apiserver.json", "kube-controller-manager.json"},
		kpki.FileFrontProxyCA: {"kube-apiserver.json"},
		kpki.FileEtcdCA:       {"etcd.json", "kube-apiserver.json"},
	} {
		files := testFiles()
		files[file] = append(files[file], []byte("\nanother CA")...)
		after, err := StaticPods(testCluster(), testNode, kubernetes.PKIDir, files, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range pods {
			if decode(t, before[name]).Annotations[CertificatesAnnotation] == decode(t, after[name]).Annotations[CertificatesAnnotation] {
				t.Errorf("%s changed alone; the hash of %s did not", file, name)
			}
		}
	}
}

// The API server's enable-admission-plugins adds to chalkos's plugins, in either spelling.
func TestExtraAdmissionPluginsKeepNodeRestriction(t *testing.T) {
	for _, name := range []string{"enable-admission-plugins", "enable_admission_plugins"} {
		c := testCluster()
		c.ExtraArgs["kube-apiserver"] = map[string]string{name: "PodSecurity,NodeRestriction"}
		pods, err := StaticPods(c, testNode, kubernetes.PKIDir, testFiles(), "")
		if err != nil {
			t.Fatal(err)
		}
		if got := commandFlags(t, pods["kube-apiserver.json"])["enable-admission-plugins"]; got != "NodeRestriction,PodSecurity" {
			t.Errorf("%s: enable-admission-plugins=%s, want NodeRestriction,PodSecurity", name, got)
		}
	}
}

// kubernetes.APIServerFlags, which extra flags must not override, are the flags chalkos sets.
func TestAPIServerFlagsAreChalkos(t *testing.T) {
	c := testCluster()
	c.ExtraArgs = nil
	pods, err := StaticPods(c, testNode, kubernetes.PKIDir, testFiles(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(commandFlags(t, pods["kube-apiserver.json"]))); !slices.Equal(got, kubernetes.APIServerFlags) {
		t.Errorf("the API server's flags are %v, kubernetes.APIServerFlags %v", got, kubernetes.APIServerFlags)
	}
}
