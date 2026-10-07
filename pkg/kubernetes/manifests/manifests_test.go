package manifests

import (
	"encoding/json"
	"flag"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func testCluster() kubernetes.Cluster {
	return kubernetes.Cluster{
		Kind:        kubernetes.KindControlPlane,
		Endpoint:    "https://10.0.0.10:6443",
		Version:     "1.37.1",
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		DNSIP:       "10.96.0.10",
		Domain:      "cluster.local",
		ExtraArgs: map[string]map[string]string{
			"kube-apiserver": {"audit-log-maxage": "30", "kubelet-preferred-address-types": "InternalIP"},
		},
		Images: kubernetes.Images{
			Etcd:                  "registry.k8s.io/etcd:3.7.0-0",
			KubeAPIServer:         "registry.k8s.io/kube-apiserver:v1.37.1",
			KubeControllerManager: "registry.k8s.io/kube-controller-manager:v1.37.1",
			KubeScheduler:         "registry.k8s.io/kube-scheduler:v1.37.1",
		},
	}
}

var testNode = kubernetes.Node{Name: "cp1", IPs: []net.IP{net.ParseIP("10.0.0.11")}}

// testFiles stands for the certificates: their content only feeds the hashes.
func testFiles() map[string][]byte {
	files := map[string][]byte{}
	for _, name := range []string{
		kpki.FileCA, kpki.FileCAKey, kpki.FileFrontProxyCA, kpki.FileFrontProxyClient, kpki.FileFrontProxyClientKey,
		kpki.FileAPIServer, kpki.FileAPIServerKey, kpki.FileAPIServerKubeletClient, kpki.FileAPIServerKubeletKey,
		kpki.FileAPIServerEtcdClient, kpki.FileAPIServerEtcdClientKey, kpki.FileServiceAccountKey, kpki.FileServiceAccountPub,
		kpki.FileEncryptionConfig, kpki.FileAuthenticationConfig, kpki.FileControllerManagerConfig, kpki.FileSchedulerConfig,
		kpki.FileEtcdCA, kpki.FileEtcdServer, kpki.FileEtcdServerKey, kpki.FileEtcdPeer, kpki.FileEtcdPeerKey,
	} {
		files[name] = []byte("content of " + name)
	}
	return files
}

func TestStaticPodsGolden(t *testing.T) {
	pods, err := StaticPods(testCluster(), testNode, testFiles())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range pods {
		names = append(names, name)
	}
	sort.Strings(names)
	if strings.Join(names, " ") != "etcd.json kube-apiserver.json kube-controller-manager.json kube-scheduler.json" {
		t.Fatalf("pods %v", names)
	}
	for _, name := range names {
		golden := filepath.Join("testdata", name)
		if *update {
			if err := os.WriteFile(golden, pods[name], 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if string(pods[name]) != string(want) {
			t.Errorf("%s differs from %s; run go test ./pkg/kubernetes/manifests -update and review the diff:\n%s", name, golden, pods[name])
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

func TestExtraArgsOverride(t *testing.T) {
	pods, err := StaticPods(testCluster(), testNode, testFiles())
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

func TestSecurityFlags(t *testing.T) {
	pods, err := StaticPods(testCluster(), testNode, testFiles())
	if err != nil {
		t.Fatal(err)
	}
	apiServer := commandFlags(t, pods["kube-apiserver.json"])
	for name, want := range map[string]string{
		"authorization-mode":            "Node,RBAC",
		"enable-admission-plugins":      "NodeRestriction",
		"profiling":                     "false",
		"authentication-config":         "/etc/kubernetes/pki/" + kpki.FileAuthenticationConfig,
		"tls-min-version":               "VersionTLS12",
		"requestheader-allowed-names":   kpki.FrontProxyClientUser,
		"kubelet-certificate-authority": "/etc/kubernetes/pki/" + kpki.FileCA,
	} {
		if got, ok := apiServer[name]; !ok || got != want {
			t.Errorf("kube-apiserver --%s=%q, want %q", name, got, want)
		}
	}
	if v, ok := apiServer["enable-bootstrap-token-auth"]; ok && v != "false" {
		t.Errorf("kube-apiserver --enable-bootstrap-token-auth=%s", v)
	}
	// The authentication configuration limits anonymous requests; the flag would allow them
	// everywhere, and the API server refuses both together.
	if v, ok := apiServer["anonymous-auth"]; ok {
		t.Errorf("kube-apiserver --anonymous-auth=%s", v)
	}
	for _, name := range []string{"kube-controller-manager.json", "kube-scheduler.json"} {
		if got := commandFlags(t, pods[name])["profiling"]; got != "false" {
			t.Errorf("%s --profiling=%q", name, got)
		}
	}
}

func TestCertificatesHashFollowsMountedFiles(t *testing.T) {
	files := testFiles()
	before, err := StaticPods(testCluster(), testNode, files)
	if err != nil {
		t.Fatal(err)
	}
	files[kpki.FileEtcdServer] = []byte("reissued")
	after, err := StaticPods(testCluster(), testNode, files)
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
	if _, err := StaticPods(testCluster(), kubernetes.Node{Name: "cp1"}, testFiles()); err == nil {
		t.Error("rendered static pods without the node's address")
	}
}
