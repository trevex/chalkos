package node

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testNode prepares the files of a node of the kind named name, with a share issued from k.
func testNode(t *testing.T, kind, name string, k *pki.KubernetesSecrets) Paths {
	t.Helper()
	root := t.TempDir()
	p := Paths{
		State:      filepath.Join(root, "state", "kubernetes"),
		Cluster:    filepath.Join(root, "etc", "cluster.json"),
		NodeFile:   filepath.Join(root, "run", "node.json"),
		Run:        filepath.Join(root, "run", "kubernetes"),
		PKI:        filepath.Join(root, "run", "kubernetes", "pki"),
		KubeletPKI: filepath.Join(root, "var", "lib", "kubelet", "pki"),
		EtcdData:   filepath.Join(root, "var", "lib", "etcd"),
	}
	write(t, p.Cluster, `{"kind": "`+kind+`", "endpoint": "https://192.168.100.11:6443", "version": "1.37.1",
	  "podCIDR": "10.244.0.0/16", "serviceCIDR": "10.96.0.0/12", "dnsIP": "10.96.0.10", "domain": "cluster.local",
	  "allowSchedulingOnControlPlanes": false, "extraArgs": {},
	  "images": {"etcd": "e", "kubeAPIServer": "a", "kubeControllerManager": "c", "kubeScheduler": "s"}}`)
	write(t, p.NodeFile, `{"hostname": "`+name+`", "network": {"networks": {"10-lan": {"address": ["192.168.100.11/24"]}}},
	  "labels": {"zone": "a", "disk": "ssd"}, "taints": [], "kubernetes": {"nodeName": "`+name+`", "nodeIP": "192.168.100.11"}}`)
	if k != nil {
		share, err := kpki.ShareFor(k, kind, name, now)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := share.Encode()
		write(t, p.Share(), string(data))
	}
	return p
}

func secrets(t *testing.T) *pki.KubernetesSecrets {
	t.Helper()
	k, err := pki.NewKubernetesSecrets(now)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// currentClient returns the kubelet's current client certificate.
func currentClient(t *testing.T, p Paths) pki.CertKey {
	t.Helper()
	data, err := os.ReadFile(p.kubeletClient())
	if err != nil {
		t.Fatal(err)
	}
	cert, rest, _ := strings.Cut(string(data), "-----END CERTIFICATE-----\n")
	return pki.CertKey{Certificate: cert + "-----END CERTIFICATE-----\n", Key: rest}
}

func TestPrepareWithoutShare(t *testing.T) {
	p := testNode(t, kubernetes.KindWorker, "w1", nil)
	write(t, p.Kubeconfig(), "stale")
	if err := Prepare(p, now); err != nil {
		t.Fatal(err)
	}
	if exists(p.Kubeconfig()) {
		t.Error("a node without a share has a kubelet kubeconfig")
	}
}

func TestPrepareWorker(t *testing.T) {
	k := secrets(t)
	p := testNode(t, kubernetes.KindWorker, "w1", k)
	if err := Prepare(p, now); err != nil {
		t.Fatal(err)
	}
	share, err := ReadShare(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := currentClient(t, p); got != *share.Kubelet {
		t.Error("the kubelet's current client certificate is not the share's")
	}
	if link, err := os.Readlink(p.kubeletClient()); err != nil || link != "kubelet-client-chalkos.pem" {
		t.Errorf("current client certificate link = %q, %v", link, err)
	}
	ca, _ := os.ReadFile(filepath.Join(p.KubeletDir(), "ca.crt"))
	if string(ca) != k.CA.Certificate {
		t.Error("the kubelet's CA is not the cluster's")
	}
	kubeconfig, _ := os.ReadFile(p.Kubeconfig())
	for _, want := range []string{`"server": "https://192.168.100.11:6443"`, `"client-certificate": "` + p.kubeletClient() + `"`} {
		if !strings.Contains(string(kubeconfig), want) {
			t.Errorf("kubeconfig lacks %s:\n%s", want, kubeconfig)
		}
	}
	flags, _ := os.ReadFile(filepath.Join(p.KubeletDir(), "flags"))
	if string(flags) != "KUBELET_ARGS=--hostname-override=w1 --node-ip=192.168.100.11 --node-labels=disk=ssd,zone=a\n" {
		t.Errorf("flags = %q", flags)
	}
	if exists(p.PKI) || exists(p.Manifests()) {
		t.Error("a worker has control-plane files")
	}
}

// kubeletKey re-encodes a PKCS #8 key as the SEC 1 key the kubelet writes when it renews.
func kubeletKey(t *testing.T, key string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(key))
	if block == nil {
		t.Fatal("no PEM key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(parsed.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

// storeRenewed puts a certificate and key into the kubelet's store as the kubelet does after
// renewing: a dated file linked from the current one.
func storeRenewed(t *testing.T, p Paths, cert, key string) {
	t.Helper()
	write(t, filepath.Join(p.KubeletPKI, "kubelet-client-2026-11-05.pem"), cert+kubeletKey(t, key))
	if err := os.Symlink("kubelet-client-2026-11-05.pem", p.kubeletClient()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareKeepsRenewedCertificate(t *testing.T) {
	k := secrets(t)
	p := testNode(t, kubernetes.KindWorker, "w1", k)
	renewed, err := kpki.IssueKubeletClient(k.CA, "w1", now.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	storeRenewed(t, p, renewed.Certificate, renewed.Key)
	if err := Prepare(p, now.Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := currentClient(t, p); got.Certificate != renewed.Certificate {
		t.Error("Prepare replaced a certificate the kubelet renewed")
	}

	// A share delivered later carries a newer certificate, which replaces the renewed one.
	newer, err := kpki.WorkerShare(k, "w1", now.Add(60*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := newer.Encode()
	write(t, p.Share(), string(data))
	if err := Prepare(p, now.Add(61*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := currentClient(t, p); got != *newer.Kubelet {
		t.Error("a newer share's certificate did not replace the current one")
	}
}

// A certificate in the kubelet's store outranks the share's only if it is a valid kubelet
// certificate for this node from the share's CA, with its own key.
func TestPrepareReplacesUntrustedRenewedCertificate(t *testing.T) {
	later := now.Add(30 * 24 * time.Hour)
	issue := func(t *testing.T, ca pki.CertKey, cn string, groups []string, server bool) pki.CertKey {
		t.Helper()
		ck, err := pki.IssueLeaf(ca, pki.Leaf{CommonName: cn, Organization: groups, Client: !server, Server: server}, later)
		if err != nil {
			t.Fatal(err)
		}
		return ck
	}
	for _, tc := range []struct {
		name string
		// renewed returns the stored certificate and key.
		renewed func(t *testing.T, k *pki.KubernetesSecrets) (cert, key string)
		at      time.Time
	}{
		{"foreign CA", func(t *testing.T, _ *pki.KubernetesSecrets) (string, string) {
			ck := issue(t, secrets(t).CA, "system:node:w1", []string{kpki.NodesGroup}, false)
			return ck.Certificate, ck.Key
		}, later},
		{"other node", func(t *testing.T, k *pki.KubernetesSecrets) (string, string) {
			ck := issue(t, k.CA, "system:node:w2", []string{kpki.NodesGroup}, false)
			return ck.Certificate, ck.Key
		}, later},
		{"extra group", func(t *testing.T, k *pki.KubernetesSecrets) (string, string) {
			ck := issue(t, k.CA, "system:node:w1", []string{kpki.NodesGroup, kpki.MastersGroup}, false)
			return ck.Certificate, ck.Key
		}, later},
		{"no client authentication", func(t *testing.T, k *pki.KubernetesSecrets) (string, string) {
			ck := issue(t, k.CA, "system:node:w1", []string{kpki.NodesGroup}, true)
			return ck.Certificate, ck.Key
		}, later},
		{"mismatched key", func(t *testing.T, k *pki.KubernetesSecrets) (string, string) {
			ck := issue(t, k.CA, "system:node:w1", []string{kpki.NodesGroup}, false)
			other := issue(t, k.CA, "system:node:w1", []string{kpki.NodesGroup}, false)
			return ck.Certificate, other.Key
		}, later},
		// It expires after the share's certificate, which has expired too.
		{"expired", func(t *testing.T, k *pki.KubernetesSecrets) (string, string) {
			ck := issue(t, k.CA, "system:node:w1", []string{kpki.NodesGroup}, false)
			return ck.Certificate, ck.Key
		}, later.Add(pki.LeafValidity + time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := secrets(t)
			p := testNode(t, kubernetes.KindWorker, "w1", k)
			cert, key := tc.renewed(t, k)
			storeRenewed(t, p, cert, key)
			if err := Prepare(p, tc.at); err != nil {
				t.Fatal(err)
			}
			share, err := ReadShare(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := currentClient(t, p); got != *share.Kubelet {
				t.Error("Prepare kept an untrusted certificate in the kubelet's store")
			}
		})
	}
}

func TestPrepareControlPlane(t *testing.T) {
	k := secrets(t)
	p := testNode(t, kubernetes.KindControlPlane, "cp1", k)
	if err := Prepare(p, now); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(p.PKI); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("certificate directory: %v, %v", info, err)
	}
	for _, f := range []string{kpki.FileCAKey, kpki.FileAPIServerKey, kpki.FileEtcdServerKey, kpki.FileEncryptionConfig, kpki.FileAuthenticationConfig} {
		info, err := os.Stat(filepath.Join(p.PKI, f))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", f, info, err)
		}
	}
	cert, _, err := currentClient(t, p).Parse()
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "system:node:cp1" {
		t.Errorf("kubelet client certificate for %s", cert.Subject.CommonName)
	}
	flags, _ := os.ReadFile(filepath.Join(p.KubeletDir(), "flags"))
	if !strings.Contains(string(flags), "--register-with-taints=node-role.kubernetes.io/control-plane:NoSchedule") {
		t.Errorf("flags = %q", flags)
	}
	entries, err := os.ReadDir(p.Manifests())
	if err != nil || len(entries) != 0 {
		t.Errorf("a control plane waiting for bootstrap has static pods: %v, %v", entries, err)
	}

	write(t, p.Bootstrapped(), "")
	if err := Prepare(p, now); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(p.Manifests())
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"etcd.json", "kube-apiserver.json", "kube-controller-manager.json", "kube-scheduler.json"}) {
		t.Errorf("static pods %v", names)
	}
}

func TestPrepareRefusesMismatchedShare(t *testing.T) {
	k := secrets(t)
	p := testNode(t, kubernetes.KindControlPlane, "cp1", nil)
	worker, _ := kpki.WorkerShare(k, "cp1", now)
	data, _ := worker.Encode()
	write(t, p.Share(), string(data))
	if err := Prepare(p, now); err == nil || !strings.Contains(err.Error(), "worker") {
		t.Errorf("err = %v, want a kind mismatch", err)
	}

	p = testNode(t, kubernetes.KindWorker, "w1", nil)
	other, _ := kpki.WorkerShare(k, "w2", now)
	data, _ = other.Encode()
	write(t, p.Share(), string(data))
	if err := Prepare(p, now); err == nil || !strings.Contains(err.Error(), "w2") {
		t.Errorf("err = %v, want a node mismatch", err)
	}
}

func TestKubeletFlags(t *testing.T) {
	c := kubernetes.Cluster{Kind: kubernetes.KindControlPlane, AllowSchedulingOnControlPlanes: true}
	n := kubernetes.Node{Name: "cp1", Taints: []manifest.Taint{{Key: "dedicated", Value: "db", Effect: "NoSchedule"}, {Key: "spot", Effect: "PreferNoSchedule"}}}
	got := strings.Join(KubeletFlags(c, n), " ")
	if got != "--hostname-override=cp1 --register-with-taints=dedicated=db:NoSchedule,spot:PreferNoSchedule" {
		t.Errorf("flags = %s", got)
	}
}

func TestEtcdHasData(t *testing.T) {
	p := testNode(t, kubernetes.KindControlPlane, "cp1", nil)
	if has, err := EtcdHasData(p); err != nil || has {
		t.Errorf("missing directory: %v, %v", has, err)
	}
	write(t, filepath.Join(p.EtcdData, "member", "snap", "db"), "")
	if has, err := EtcdHasData(p); err != nil || !has {
		t.Errorf("directory with a member: %v, %v", has, err)
	}
}

func staticPods(t *testing.T, p Paths) []string {
	t.Helper()
	entries, err := os.ReadDir(p.Manifests())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Once etcd was initialised, an empty data directory means VAR was reset or lost: a new etcd
// would start a second, empty cluster.
func TestPrepareRefusesMissingEtcdData(t *testing.T) {
	p := testNode(t, kubernetes.KindControlPlane, "cp1", secrets(t))
	write(t, p.Bootstrapped(), "")
	write(t, p.EtcdInitialised(), "")
	err := Prepare(p, now)
	if !errors.Is(err, ErrEtcdDataMissing) || !strings.Contains(err.Error(), "etcd data is missing on a node whose cluster was initialised; restore etcd or reinstall the node") {
		t.Errorf("err = %v, want missing etcd data", err)
	}
	if pods := staticPods(t, p); len(pods) != 0 {
		t.Errorf("static pods %v", pods)
	}
	if err := RenderStaticPods(p); !errors.Is(err, ErrEtcdDataMissing) {
		t.Errorf("RenderStaticPods() = %v, want missing etcd data", err)
	}

	write(t, filepath.Join(p.EtcdData, "member", "snap", "db"), "")
	if err := Prepare(p, now); err != nil {
		t.Fatal(err)
	}
	if pods := staticPods(t, p); len(pods) != 4 {
		t.Errorf("restored etcd data: static pods %v", pods)
	}
}

// A bootstrap interrupted before etcd became ready is retried.
func TestPrepareRetriesInterruptedBootstrap(t *testing.T) {
	p := testNode(t, kubernetes.KindControlPlane, "cp1", secrets(t))
	write(t, p.Bootstrapped(), "")
	if err := Prepare(p, now); err != nil {
		t.Fatal(err)
	}
	if pods := staticPods(t, p); len(pods) != 4 {
		t.Errorf("static pods %v", pods)
	}
}

func TestMarkEtcdInitialised(t *testing.T) {
	p := testNode(t, kubernetes.KindControlPlane, "cp1", nil)
	if initialised, err := EtcdInitialised(p); err != nil || initialised {
		t.Fatalf("before: %v, %v", initialised, err)
	}
	if err := MarkEtcdInitialised(p, now); err != nil {
		t.Fatal(err)
	}
	if initialised, err := EtcdInitialised(p); err != nil || !initialised {
		t.Fatalf("after: %v, %v", initialised, err)
	}
	first, _ := os.ReadFile(p.EtcdInitialised())
	if err := MarkEtcdInitialised(p, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(p.EtcdInitialised()); string(again) != string(first) {
		t.Errorf("the marker changed from %q to %q", first, again)
	}
}
