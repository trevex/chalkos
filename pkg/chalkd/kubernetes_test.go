package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// kubernetesServer is an installed node of the kind with the files chalkd reads; with a share
// when share is set. Its control plane applies 19 objects at once.
func kubernetesServer(t *testing.T, kind string, share bool) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := installedServer(t, section("", ""), false)
	addKubernetes(t, s, kind, share)
	return s, r
}

// addKubernetes gives s the Kubernetes side of a node of the kind with the files chalkd reads;
// with a share when share is set.
func addKubernetes(t *testing.T, s *Server, kind string, share bool) {
	t.Helper()
	root := t.TempDir()
	s.Kubernetes = &Kubernetes{
		Paths: knode.Paths{
			State:      filepath.Join(s.Paths.StateDir, "kubernetes"),
			Cluster:    filepath.Join(root, "etc", "cluster.json"),
			NodeFile:   filepath.Join(root, "run", "node.json"),
			Run:        filepath.Join(root, "run", "kubernetes"),
			PKI:        filepath.Join(root, "run", "kubernetes", "pki"),
			KubeletPKI: filepath.Join(root, "var", "lib", "kubelet", "pki"),
			EtcdData:   filepath.Join(root, "var", "lib", "etcd"),
		},
		Slot:         s.Loader.Slot,
		Objects:      func(k8s.Cluster) ([]map[string]any, error) { return nil, errors.New("no objects in this test") },
		ControlPlane: func(_ context.Context, _ kpki.Share, applied func(int)) error { applied(19); return nil },
		Node:         func(context.Context) (*corev1.Node, error) { return readyNode(), nil },
		// No cluster answers at the endpoint.
		EtcdEndpoints: func(context.Context, k8s.Cluster, kpki.Share, []netip.Addr) ([]string, error) {
			return nil, errors.New("connection refused")
		},
		ClusterAnswers: func(context.Context, k8s.Cluster, kpki.Share) bool { return false },
		// The API server keeps no objects.
		APIClient: func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return fake.NewClientset(), nil },
	}
	// The loops a test started would run on against its removed files, and load the tests after
	// it, until the test binary ends.
	t.Cleanup(s.Kubernetes.Stop)
	p := s.Kubernetes.Paths
	write(t, p.Cluster, `{"kind": "`+kind+`", "endpoint": "https://192.168.100.11:6443", "version": "1.37.1",
	  "podCIDRs": {"ipv4": "10.244.0.0/16"}, "serviceCIDRs": {"ipv4": "10.96.0.0/12"},
	  "dnsIPs": {"ipv4": "10.96.0.10"}, "nodeCIDRMaskSizes": {"ipv4": 24}, "domain": "cluster.local",
	  "extraArgs": {}, "images": {"etcd": "e", "kubeAPIServer": "a", "kubeControllerManager": "c", "kubeScheduler": "s"}}`)
	write(t, p.NodeFile, `{"hostname": "n1", "kubernetes": {"nodeName": "n1", "nodeIPs": ["192.168.100.11"]}}`)
	if share {
		write(t, p.Share(), string(testShare(t, kind)))
		if err := knode.Prepare(p, time.Now(), nodeIP("192.168.100.11"), nil); err != nil {
			t.Fatal(err)
		}
	}
}

// readyNode is a Ready Node with the pod ranges.
func readyNode(podCIDRs ...string) *corev1.Node {
	return &corev1.Node{
		Spec:   corev1.NodeSpec{PodCIDRs: podCIDRs},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}
}

// nodeIP is a resolver that finds the address at once.
func nodeIP(ip string) knode.Resolver {
	return func(nodeip.Selector, time.Duration) ([]nodeip.Address, error) {
		return []nodeip.Address{{Interface: "eth0", IP: netip.MustParseAddr(ip)}}, nil
	}
}

// withoutAddress leaves s as a preparation that found no address leaves a node, for the reason
// given; "" stands for a preparation still waiting. Neither marks the node prepared.
func withoutAddress(t *testing.T, s *Server, reason string) {
	t.Helper()
	p := s.Kubernetes.Paths
	for _, f := range []string{p.Prepared(), p.NodeIP()} {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	if reason != "" {
		write(t, p.PrepareError(), reason+"\n")
	}
}

// otherKindsShare gives s a share for another kind of node than its image's and runs the
// preparation, which refuses it.
func otherKindsShare(t *testing.T, s *Server) {
	t.Helper()
	p := s.Kubernetes.Paths
	c, err := k8s.ReadCluster(p.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	other := k8s.KindWorker
	if c.Kind == k8s.KindWorker {
		other = k8s.KindControlPlane
	}
	write(t, p.Share(), string(testShare(t, other)))
	if err := knode.Prepare(p, time.Now(), nodeIP("192.168.100.11"), nil); err == nil {
		t.Fatal("prepared a share of another kind")
	}
}

// withoutFirewall runs the preparation of s, which has a share, with a firewall that cannot
// accept VXLAN.
func withoutFirewall(t *testing.T, s *Server) {
	t.Helper()
	fail := func(knode.Paths) error { return errors.New("accept VXLAN to the node's address: exit status 1") }
	if err := knode.Prepare(s.Kubernetes.Paths, time.Now(), nodeIP("192.168.100.11"), fail); err == nil {
		t.Fatal("prepared without the firewall's VXLAN rule")
	}
}

func testShare(t *testing.T, kind string) []byte {
	t.Helper()
	return testShareFor(t, kind, "n1")
}

func testShareFor(t *testing.T, kind, nodeName string) []byte {
	t.Helper()
	k, err := pki.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	share, err := kpki.ShareFor(&pki.Secrets{Kubernetes: *k, NodeCA: testNodeCA(t)}, kind, nodeName, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := share.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func bootstrap(s *Server, ctx context.Context) (*nodev1.BootstrapResponse, error) {
	resp, err := s.Bootstrap(ctx, connect.NewRequest(&nodev1.BootstrapRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func TestBootstrap(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	resp, err := bootstrap(s, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Applied != 19 {
		t.Errorf("applied %d objects", resp.Applied)
	}
	p := s.Kubernetes.Paths
	if bootstrapped, err := knode.Bootstrapped(p); err != nil || !bootstrapped {
		t.Errorf("bootstrapped = %v, %v", bootstrapped, err)
	}
	entries, _ := os.ReadDir(p.Manifests())
	if len(entries) != 4 {
		t.Errorf("static pods %v", entries)
	}
	if pin, err := knode.ReadPin(p); err != nil || len(pin) != 1 || pin[0].String() != "192.168.100.11" {
		t.Errorf("pin = %v, %v, want the node's address", pin, err)
	}
	_, err = bootstrap(s, context.Background())
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "bootstrapped already") {
		t.Errorf("second bootstrap: %v", err)
	}
}

func TestBootstrapRefusals(t *testing.T) {
	plain, _ := installedServer(t, section("", ""), false)
	worker, _ := kubernetesServer(t, k8s.KindWorker, true)
	noShare, _ := kubernetesServer(t, k8s.KindControlPlane, false)
	etcdData, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	write(t, filepath.Join(etcdData.Kubernetes.Paths.EtcdData, "member", "snap", "db"), "")
	noAddress, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	withoutAddress(t, noAddress, "no node address matches validSubnets 192.168.100.0/24 (the node has 10.0.2.15 on eth0)")
	// A preparation that picked the address and still writes the certificates.
	preparing, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	if err := os.Remove(preparing.Kubernetes.Paths.Prepared()); err != nil {
		t.Fatal(err)
	}
	withNodeCertificate(t, preparing)
	otherKind, _ := kubernetesServer(t, k8s.KindControlPlane, false)
	otherKindsShare(t, otherKind)
	noFirewall, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	withoutFirewall(t, noFirewall)
	for name, tc := range map[string]struct {
		s    *Server
		want string
	}{
		"no Kubernetes":         {plain, "the node's image has no Kubernetes"},
		"worker":                {worker, "is a worker"},
		"no share":              {noShare, "no Kubernetes share"},
		"etcd with data":        {etcdData, "holds etcd data"},
		"no address":            {noAddress, "no node address matches validSubnets 192.168.100.0/24"},
		"preparing":             {preparing, "the node's Kubernetes files are not prepared yet; see chalkctl logs n1 --unit chalkos-kubernetes"},
		"share of another kind": {otherKind, "preparation failed: the node's share is for a worker node, but its config gives it the type controlplane"},
		"firewall":              {noFirewall, "preparation failed: accept VXLAN to the node's address: exit status 1"},
	} {
		var before map[string]string
		if k := tc.s.Kubernetes; k != nil {
			before = files(t, k.Paths.State, k.Paths.Run, k.Paths.EtcdData)
		}
		_, err := bootstrap(tc.s, context.Background())
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
		if k := tc.s.Kubernetes; k != nil {
			after := files(t, k.Paths.State, k.Paths.Run, k.Paths.EtcdData)
			var changed []string
			for path := range maps.Keys(before) {
				if after[path] != before[path] {
					changed = append(changed, path)
				}
			}
			for path := range maps.Keys(after) {
				if _, ok := before[path]; !ok {
					changed = append(changed, path)
				}
			}
			if len(changed) > 0 {
				t.Errorf("%s: the refused bootstrap changed %v", name, changed)
			}
		}
	}
}

// files describes the files under dirs by their size, mode and modification time.
func files(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			found[path] = fmt.Sprintf("%d %v %v", info.Size(), info.Mode(), info.ModTime())
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	return found
}

func TestBootstrapReturnsBeforeManifestsApplied(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	s.Kubernetes.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error { <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := bootstrap(s, ctx)
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	// The node stays committed: it never initialises a second cluster.
	if bootstrapped, _ := knode.Bootstrapped(s.Kubernetes.Paths); !bootstrapped {
		t.Error("the node lost its bootstrap marker")
	}
}

// kubernetesIdentity is identityWith for the Kubernetes node nodeName.
func kubernetesIdentity(nodeName string) string {
	return `{"hostname": "n1", "platform": "metal", "kubernetes": {"nodeName": "` + nodeName + `", "nodeIPs": ["192.168.100.11"]}, "storage": ` + section("", "") + `}`
}

func TestApplyConfigDeliversShare(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, false)
	share := testShare(t, k8s.KindWorker)
	// STATE keeps the share as Encode writes it, not as it was delivered.
	var delivered bytes.Buffer
	if err := json.Indent(&delivered, share, "", "  "); err != nil {
		t.Fatal(err)
	}
	resp, err := deliver(context.Background(), s, kubernetesIdentity("n1"), &nodev1.ApplyConfigRequest{KubernetesShare: delivered.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	path := s.Kubernetes.Paths.Share()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, share) {
		t.Fatalf("the recorded share is not the canonical encoding: %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("share mode %v", info.Mode())
	}
	if !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("commands %v", r.Calls)
	}
	if !slices.Contains(resp.Msg.RestartedUnits, "kubelet.service") {
		t.Errorf("restarted %v", resp.Msg.RestartedUnits)
	}
}

func TestApplyConfigRefusesShare(t *testing.T) {
	plain, _ := installedServer(t, section("", ""), false)
	worker, r := kubernetesServer(t, k8s.KindWorker, false)
	controlPlane, _ := kubernetesServer(t, k8s.KindControlPlane, false)
	other, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherNodeCA, err := pki.NewNodeCA(other, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	k, err := pki.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	foreignNodeCA, err := kpki.ControlPlaneShare(k, otherNodeCA).Encode()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		s        *Server
		identity string
		share    []byte
		code     connect.Code
	}{
		"no Kubernetes":              {plain, kubernetesIdentity("n1"), testShare(t, k8s.KindWorker), connect.CodeInvalidArgument},
		"broken share":               {worker, kubernetesIdentity("n1"), []byte(`{"kind": "worker"}`), connect.CodeInvalidArgument},
		"another node's share":       {worker, kubernetesIdentity("n1"), testShareFor(t, k8s.KindWorker, "n2"), connect.CodeInvalidArgument},
		"no node name":               {worker, identityWith(section("", "")), testShare(t, k8s.KindWorker), connect.CodeInvalidArgument},
		"a node CA of another OS CA": {controlPlane, kubernetesIdentity("n1"), foreignNodeCA, connect.CodeInvalidArgument},
	} {
		_, err := deliver(context.Background(), tc.s, tc.identity, &nodev1.ApplyConfigRequest{KubernetesShare: tc.share})
		if connect.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %v", name, err, tc.code)
		}
	}
	if len(r.Calls) != 0 {
		t.Errorf("a refused share ran %v", r.Calls)
	}
	if _, err := os.Stat(worker.Kubernetes.Paths.Share()); err == nil {
		t.Error("a refused share was recorded")
	}
}

func TestStatusKubernetes(t *testing.T) {
	waiting, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	bootstrapped, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	write(t, bootstrapped.Kubernetes.Paths.Bootstrapped(), "")
	noShare, _ := kubernetesServer(t, k8s.KindWorker, false)
	worker, _ := kubernetesServer(t, k8s.KindWorker, true)
	noAddress, _ := kubernetesServer(t, k8s.KindWorker, true)
	withoutAddress(t, noAddress, "no node address matches the default filter (the node has no addresses)")
	preparing, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	withoutAddress(t, preparing, "")
	otherKind, _ := kubernetesServer(t, k8s.KindWorker, false)
	otherKindsShare(t, otherKind)
	noFirewall, _ := kubernetesServer(t, k8s.KindWorker, true)
	withoutFirewall(t, noFirewall)
	// The preparation picked the address and still writes the certificates.
	writing, _ := kubernetesServer(t, k8s.KindWorker, true)
	if err := os.Remove(writing.Kubernetes.Paths.Prepared()); err != nil {
		t.Fatal(err)
	}
	worker.Kubernetes.Node = func(context.Context) (*corev1.Node, error) { return nil, errors.New("connection refused") }
	// A dual-stack worker whose Node has a pod range of one family only, as when the control plane
	// runs with that family alone.
	dualStack := func(podCIDRs ...string) *Server {
		s, _ := kubernetesServer(t, k8s.KindWorker, true)
		p := s.Kubernetes.Paths
		data, err := os.ReadFile(p.Cluster)
		if err != nil {
			t.Fatal(err)
		}
		cluster := strings.NewReplacer(`"podCIDRs": {`, `"ipFamilies": ["ipv4", "ipv6"], "podCIDRs": {"ipv6": "fd00:10:244::/56", `,
			`"serviceCIDRs": {`, `"serviceCIDRs": {"ipv6": "fd00:10:96::/112", `, `"dnsIPs": {`, `"dnsIPs": {"ipv6": "fd00:10:96::a", `,
			`"nodeCIDRMaskSizes": {`, `"nodeCIDRMaskSizes": {"ipv6": 64, `).Replace(string(data))
		write(t, p.Cluster, cluster)
		s.Kubernetes.Node = func(context.Context) (*corev1.Node, error) { return readyNode(podCIDRs...), nil }
		return s
	}
	plain, _ := installedServer(t, section("", ""), false)
	for name, tc := range map[string]struct {
		s    *Server
		want *nodev1.KubernetesStatus
	}{
		"waiting":               {waiting, &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_WAITING, Kind: "controlplane", State: "waiting for bootstrap or for the cluster at https://192.168.100.11:6443"}},
		"bootstrapped":          {bootstrapped, &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_BOOTSTRAPPED, Kind: "controlplane", State: "bootstrapped", NodeReady: "True"}},
		"no share":              {noShare, &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_PREPARING, Kind: "worker", State: "no share"}},
		"no address":            {noAddress, &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_PREPARING, Kind: "worker", State: "preparation failed: no node address matches the default filter (the node has no addresses)"}},
		"share of another kind": {otherKind, &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_PREPARING, Kind: "worker", State: "preparation failed: the node's share is for a controlplane node, but its config gives it the type worker"}},
		"firewall":              {noFirewall, &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_PREPARING, Kind: "worker", State: "preparation failed: accept VXLAN to the node's address: exit status 1"}},
		"preparing":             {preparing, &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_PREPARING, Kind: "controlplane", State: "preparing"}},
		"writing":               {writing, &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_PREPARING, Kind: "worker", State: "preparing"}},
		"worker":                {worker, &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_JOINED, Kind: "worker", State: "joined", NodeReady: "unknown: connection refused"}},
		"dual-stack worker":     {dualStack("10.244.1.0/24", "fd00:10:244:1::/64"), &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_JOINED, Kind: "worker", State: "joined", NodeReady: "True"}},
		"no pod range yet":      {dualStack(), &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_JOINED, Kind: "worker", State: "joined", NodeReady: "True"}},
		"no ipv6 pod range": {dualStack("10.244.1.0/24"), &nodev1.KubernetesStatus{Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_JOINED, Kind: "worker", NodeReady: "True",
			State: "joined: the Node has no ipv6 pod range; the control plane allocates ranges of the families the cluster was created with"}},
		"no Kubernetes": {plain, nil},
	} {
		resp, err := tc.s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := resp.Msg.Kubernetes
		if (got == nil) != (tc.want == nil) || got != nil && (got.Kind != tc.want.Kind || got.State != tc.want.State || got.Phase != tc.want.Phase || got.NodeReady != tc.want.NodeReady) {
			t.Errorf("%s: kubernetes status %v, want %v", name, got, tc.want)
		}
	}
}

// readyServer is an API server whose /readyz answers ok once ready is set.
func readyServer(t *testing.T, ready *atomic.Bool) *rest.Config {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" && ready.Load() {
			io.WriteString(w, "ok")
			return
		}
		http.Error(w, "not ready", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	return &rest.Config{Host: srv.URL, TLSClientConfig: rest.TLSClientConfig{CAData: ca}}
}

func TestEtcdInitialisedAfterReadiness(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	var ready atomic.Bool
	cfg := readyServer(t, &ready)
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := k.applyOnce(ctx, c, cfg); err == nil {
		t.Fatal("applied without a ready API server")
	}
	if initialised, err := knode.EtcdInitialised(k.Paths); err != nil || initialised {
		t.Fatalf("before readiness: initialised = %v, %v", initialised, err)
	}

	ready.Store(true)
	// The objects fail, so applying fails after the API server answered ready.
	if _, err := k.applyOnce(context.Background(), c, cfg); err == nil {
		t.Fatal("applied objects that fail")
	}
	if initialised, err := knode.EtcdInitialised(k.Paths); err != nil || !initialised {
		t.Errorf("after readiness: initialised = %v, %v", initialised, err)
	}
}

func TestStatusEtcdDataMissing(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	p := s.Kubernetes.Paths
	write(t, p.Bootstrapped(), "")
	write(t, p.EtcdInitialised(), "")
	resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Msg.Kubernetes.State; got != "etcd data missing: restore etcd or reinstall the node" {
		t.Errorf("state %q", got)
	}
}

func TestApplyConfigRefusesShareOfAnotherKind(t *testing.T) {
	for image, share := range map[string]string{k8s.KindWorker: k8s.KindControlPlane, k8s.KindControlPlane: k8s.KindWorker} {
		s, r := kubernetesServer(t, image, false)
		before, _ := readIdentity(s)
		_, err := deliver(context.Background(), s, kubernetesIdentity("n1"), &nodev1.ApplyConfigRequest{KubernetesShare: testShare(t, share)})
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), share) {
			t.Errorf("%s share on a %s image: %v", share, image, err)
		}
		if len(r.Calls) != 0 {
			t.Errorf("%s share on a %s image ran %v", share, image, r.Calls)
		}
		if _, err := os.Stat(s.Kubernetes.Paths.Share()); err == nil {
			t.Errorf("%s share on a %s image was recorded", share, image)
		}
		if after, _ := readIdentity(s); !bytes.Equal(after, before) {
			t.Errorf("%s share on a %s image: the identity was recorded", share, image)
		}
	}
}

// The control plane's loop starts again after an error, with the share as STATE holds it then.
func TestControlPlaneLoopRestarts(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	k.RestartBackoff = 10 * time.Millisecond
	replaced := testShare(t, k8s.KindControlPlane)
	want, err := kpki.ParseShare(replaced)
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	var second kpki.Share
	k.ControlPlane = func(_ context.Context, share kpki.Share, applied func(int)) error {
		if runs.Add(1) == 1 {
			if err := os.WriteFile(k.Paths.Share(), replaced, 0o600); err != nil {
				return err
			}
			return errors.New("the API server went away")
		}
		second = share
		applied(19)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	resp, err := bootstrap(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Applied != 19 || runs.Load() != 2 {
		t.Errorf("applied %d objects after %d runs", resp.Applied, runs.Load())
	}
	if second.CA.Certificate != want.CA.Certificate {
		t.Error("the restarted loop did not read the share again")
	}
}

// chalkd starts before the preparation at boot; the control plane's loop waits for it.
func TestControlPlaneLoopWaitsForPreparation(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	p := k.Paths
	write(t, p.Bootstrapped(), "")
	write(t, p.Pin(), "192.168.100.11\n")
	if err := os.Remove(p.Prepared()); err != nil {
		t.Fatal(err)
	}
	k.RestartBackoff, k.MaxRestartBackoff = 10*time.Millisecond, 20*time.Millisecond
	runs := make(chan struct{}, 1)
	k.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		select {
		case runs <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	k.Start()
	select {
	case <-runs:
		t.Fatal("the control plane's loop ran before the node's files were prepared")
	case <-time.After(200 * time.Millisecond):
	}
	if err := knode.Prepare(p, time.Now(), nodeIP("192.168.100.11"), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runs:
	case <-time.After(time.Minute):
		t.Fatal("the control plane's loop did not start once the node's files were prepared")
	}
}

// A bootstrapped control plane without a pin, as an older image left it, is pinned once its
// files are prepared and its etcd member's peer URL confirms its address.
func TestControlPlaneLoopPins(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	write(t, k.Paths.Bootstrapped(), "")
	share, _ := knode.ReadShare(k.Paths)
	k.LocalEtcd = etcdtest.StartAdvertising(t, *share.EtcdCA, "n1", "https://192.168.100.11:2380").ClientURL
	ran := make(chan struct{})
	k.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		close(ran)
		<-ctx.Done()
		return ctx.Err()
	}
	k.Start()
	select {
	case <-ran:
	case <-time.After(time.Minute):
		t.Fatal("the control plane's loop did not run")
	}
	if pin, err := knode.ReadPin(k.Paths); err != nil || len(pin) != 1 || pin[0].String() != "192.168.100.11" {
		t.Errorf("pin = %v, %v, want the node's address", pin, err)
	}
}

// A bootstrapped control plane whose address is not the one its etcd member was started with is
// never pinned to it.
func TestControlPlaneLoopRefusesMismatchedPin(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	write(t, k.Paths.Bootstrapped(), "")
	share, _ := knode.ReadShare(k.Paths)
	k.LocalEtcd = etcdtest.StartAdvertising(t, *share.EtcdCA, "n1", "https://192.168.100.12:2380").ClientURL
	k.RestartBackoff, k.MaxRestartBackoff = 10*time.Millisecond, 20*time.Millisecond
	var ran atomic.Bool
	k.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		ran.Store(true)
		<-ctx.Done()
		return ctx.Err()
	}
	k.Start()
	want := "bootstrapped: the node's address differs from its etcd peer URL https://192.168.100.12:2380; restore the address"
	eventually(t, "the mismatch's state", func() bool {
		st, err := k.status(context.Background())
		return err == nil && st.State == want
	})
	if exists(k.Paths.Pin()) {
		t.Error("the node was pinned to an address its etcd member does not have")
	}
	if ran.Load() {
		t.Error("the control plane's loop ran without the node's pin")
	}
}

func TestCredentialIsShortLived(t *testing.T) {
	share, err := kpki.ParseShare(testShare(t, k8s.KindControlPlane))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	c := newCredential(share, time.Hour, func() time.Time { return clock })
	first, err := c.GetClientCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := first.Leaf.NotAfter.Sub(clock); d < 59*time.Minute || d > time.Hour {
		t.Errorf("the certificate expires after %v, want an hour", d)
	}
	if !slices.Equal(first.Leaf.Subject.Organization, []string{kpki.MastersGroup}) {
		t.Errorf("groups %v", first.Leaf.Subject.Organization)
	}

	clock = clock.Add(29 * time.Minute)
	if again, err := c.GetClientCertificate(nil); err != nil || again != first {
		t.Errorf("issued again before half the lifetime passed: %v", err)
	}
	clock = clock.Add(2 * time.Minute)
	next, err := c.GetClientCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if next == first || !next.Leaf.NotAfter.After(first.Leaf.NotAfter) {
		t.Error("not issued again after half the lifetime passed")
	}
	if !c.rotated() || c.rotated() {
		t.Error("the replaced certificate's connections are not closed exactly once")
	}
}

// A change of what the node's Kubernetes files are made from runs their preparation again,
// which picks the node's address and fills the firewall's VXLAN chain, and restarts the kubelet.
// The firewall itself is left alone, so a node whose firewall is disabled takes a new address.
// TestApplyConfigRestartsKubernetesOnChange: the kubelet and its preparation restart when what
// they read of the node's config changes, compared with the running slot: the Node's spec, the
// Host, the static addresses of the Network documents, the cluster the Cluster document gives,
// its endpoint among it, and the Flannel document. Other changes, of a Network beside its
// addresses, of the Node's name or of another kind, restart neither.
func TestApplyConfigRestartsKubernetesOnChange(t *testing.T) {
	base := workerConfig + networkConfig
	node := func(spec string) string {
		return strings.Replace(base, "spec: {type: worker, nodeIP: {addresses: [192.168.100.21]}}", "spec: "+spec, 1)
	}
	for name, c := range map[string]struct {
		config   string
		restarts bool
	}{
		"unchanged":           {base, false},
		"kubeletExtraArgs":    {node("{type: worker, nodeIP: {addresses: [192.168.100.21]}, kubeletExtraArgs: {max-pods: \"50\"}}"), true},
		"nodeIP.addresses":    {node("{type: worker, nodeIP: {addresses: [192.168.100.22]}}"), true},
		"nodeIP.validSubnets": {node("{type: worker, nodeIP: {addresses: [], validSubnets: [192.168.100.0/24]}}"), true},
		"the Node's name":     {strings.Replace(base, "kind: Node\nmetadata: {name: default}", "kind: Node\nmetadata: {name: node}", 1), false},
		"a Host":              {base + hostConfig("default", "h1"), true},
		"a static address":    {strings.Replace(base, "10.0.0.11/24", "10.0.0.12/24", 1), true},
		"the cluster":         {strings.Replace(base, `spec: {endpoint: "https://192.168.100.11:6443"}`, `spec: {endpoint: "https://192.168.100.11:6443", domain: lab.local}`, 1), true},
		"the endpoint":        {strings.Replace(base, "192.168.100.11:6443", "192.168.100.12:6443", 1), true},
		"a Flannel's sources": {base + "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Flannel\nmetadata: {name: default}\nspec: {vxlanSourceSubnets: [192.168.100.0/24]}\n", true},
		"a Network's gateway": {strings.Replace(base, "address: [10.0.0.11/24]", "address: [10.0.0.11/24], gateway: 10.0.0.1", 1), false},
		"a Demo":              {strings.Replace(base, "name: first", "name: second", 1), false},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := clusterServer(t, k8s.KindWorker)
			if _, err := sendConfig(s, base); err != nil {
				t.Fatal(err)
			}
			r.ResetCalls()
			resp, err := sendConfig(s, c.config)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service"); got != c.restarts {
				t.Errorf("restarted %v, want %v: %v", got, c.restarts, r.Calls)
			}
			if restarted := slices.Contains(resp.RestartedUnits, "kubelet.service"); restarted != c.restarts {
				t.Errorf("restarted units %v", resp.RestartedUnits)
			}
		})
	}
}

// hostConfig is a Host document of the name and hostname.
func hostConfig(name, hostname string) string {
	return "---\napiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: " + name + "}\nspec: {hostname: " + hostname + "}\n"
}

// TestApplyConfigRestartsKubernetesOnHostChange: another hostname in the Host restarts the kubelet
// and its preparation, and the same Host under another name restarts neither.
func TestApplyConfigRestartsKubernetesOnHostChange(t *testing.T) {
	base := workerConfig + networkConfig
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, base); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name, config string
		restarts     bool
	}{
		{"a Host", base + hostConfig("default", "h1"), true},
		{"another hostname", base + hostConfig("default", "h2"), true},
		{"another name", base + hostConfig("host", "h2"), false},
	} {
		r.ResetCalls()
		resp, err := sendConfig(s, step.config)
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service"); got != step.restarts {
			t.Errorf("%s: restarted %v, want %v: %v", step.name, got, step.restarts, r.Calls)
		}
		if restarted := slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}); restarted != step.restarts {
			t.Errorf("%s: restarted units %v", step.name, resp.RestartedUnits)
		}
	}
}

// A share delivered to a running control plane reaches its loop at once, which starts again
// with it.
func TestApplyConfigRestartsControlPlaneLoopWithNewShare(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	// Only a reload restarts the loop this quickly.
	k.RestartBackoff = time.Hour
	shares := make(chan kpki.Share, 4)
	k.ControlPlane = func(ctx context.Context, share kpki.Share, applied func(int)) error {
		shares <- share
		applied(19)
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := bootstrap(s, context.Background()); err != nil {
		t.Fatal(err)
	}
	<-shares
	replaced := testShare(t, k8s.KindControlPlane)
	want, err := kpki.ParseShare(replaced)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deliver(context.Background(), s, kubernetesIdentity("n1"), &nodev1.ApplyConfigRequest{KubernetesShare: replaced}); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-shares:
		if second.CA.Certificate != want.CA.Certificate {
			t.Error("the loop started again with the old share")
		}
	case <-time.After(time.Minute):
		t.Fatal("the running loop did not start again with the delivered share")
	}
}

// A node that finds no address keeps the config, runs no kubelet, and says why.
func TestApplyConfigReportsMissingAddress(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	const reason = "no node address matches validSubnets 192.168.100.0/24 (the node has 10.0.2.15 on eth0)"
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}, r.Rules...)
	withoutAddress(t, s, reason)
	_, err := sendConfig(s, workerConfig)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the node runs no kubelet: preparation failed: "+reason) {
		t.Errorf("err = %v, want the missing address", err)
	}
}

// A restart that fails says why: the preparation's reason, or else the restart's own error, and
// names the units to restart by hand, since the config is stored.
func TestApplyConfigReportsFailedRestart(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}, r.Rules...)
	withoutFirewall(t, s)
	_, err := sendConfig(s, workerConfig)
	want := "restart chalkos-kubernetes.service kubelet.service: exit status 1; the node runs no kubelet: preparation failed: accept VXLAN to the node's address: exit status 1; the delivery is applied, so restart chalkos-kubernetes.service kubelet.service by hand"
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
		t.Errorf("a failed preparation: err = %v, want %s", err, want)
	}

	// The preparation succeeded; the restart failed for another reason.
	other, r := clusterServer(t, k8s.KindWorker)
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}, r.Rules...)
	_, err = sendConfig(other, workerConfig)
	want = "restart chalkos-kubernetes.service kubelet.service: exit status 1; the delivery is applied, so restart chalkos-kubernetes.service kubelet.service by hand"
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "preparation") {
		t.Errorf("failed restart: err = %v, want %s", err, want)
	}
}

// The preparation's reason is the Kubernetes units' alone: another unit whose restart fails on a
// node whose preparation failed says only its own error.
func TestApplyConfigReportsThePreparationForKubernetesUnitsAlone(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	withoutFirewall(t, s)
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl try-restart demo.service", Err: errors.New("exit status 1")}}, r.Rules...)
	_, err := sendConfig(s, strings.Replace(workerConfig, "name: first", "name: second", 1))
	want := "try-restart demo.service: exit status 1; the delivery is applied, so restart demo.service by hand"
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "kubelet") {
		t.Errorf("err = %v, want %s", err, want)
	}
}

func TestApplyConfigRefusesInvalidAddressSettings(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, true)
	for name, kubernetes := range map[string]string{
		"nodeIPs":      `{"nodeName": "n1", "nodeIPs": ["192.168.100"]}`,
		"validSubnets": `{"nodeName": "n1", "nodeIPs": [], "validSubnets": ["192.168.100.0"]}`,
	} {
		identity := strings.Replace(kubernetesIdentity("n1"), `{"nodeName": "n1", "nodeIPs": ["192.168.100.11"]}`, kubernetes, 1)
		if _, err := apply(s, identity, ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want invalid argument", name, err)
		}
	}
	if len(r.Calls) != 0 {
		t.Errorf("a refused identity ran %v", r.Calls)
	}
}

// Once chalkd stops, a Start or Reload that arrives meanwhile, as from ApplyConfig, starts no
// loop again.
func TestStartAfterStopDoesNothing(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	p := k.Paths
	var runs atomic.Int32
	k.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		runs.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}
	k.Stop()
	// Not bootstrapped, the node would join.
	k.Start()
	k.Reload()
	write(t, p.Bootstrapped(), "")
	write(t, p.Pin(), "192.168.100.11\n")
	k.Start()
	k.Reload()
	time.Sleep(200 * time.Millisecond)
	k.mu.Lock()
	started, joining := k.started, k.joining
	k.mu.Unlock()
	if started || joining || runs.Load() != 0 {
		t.Errorf("after Stop: started %v, joining %v, %d runs of the control plane's loop", started, joining, runs.Load())
	}
}

// testOSCA is the OS CA of every test server's STATE.
var testOSCA = sync.OnceValues(func() (pki.CertKey, error) { return pki.NewOSCA(time.Now()) })

// testNodeCA returns a new node CA of the test servers' OS CA.
func testNodeCA(t *testing.T) pki.CertKey {
	t.Helper()
	osCA, err := testOSCA()
	if err != nil {
		t.Fatal(err)
	}
	nodeCA, err := pki.NewNodeCA(osCA, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return nodeCA
}

func TestApplyConfigRecordsNodeCAWithoutRestarts(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindControlPlane, true)
	p := s.Kubernetes.Paths
	current, err := knode.ReadShare(p)
	if err != nil {
		t.Fatal(err)
	}
	nodeCA := testNodeCA(t)
	next := current
	next.NodeCA = &nodeCA
	data, err := next.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{KubernetesShare: data})); err != nil {
		t.Fatal(err)
	}
	if got, err := knode.ReadShare(p); err != nil || *got.NodeCA != nodeCA {
		t.Errorf("the new node CA was not recorded: %v", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("a new node CA ran %v", r.Calls)
	}

	// Any other change prepares the node's files again.
	other := testShare(t, k8s.KindControlPlane)
	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{KubernetesShare: other})); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("a new share ran %v", r.Calls)
	}
}

// The same share delivered again changes no file the kubelet or chalkd's loops read, so nothing
// restarts and the loops are left alone: a delivery that changes nothing does nothing.
func TestApplyConfigTheSameShareAgainDoesNothing(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	share := []byte(readFile(t, filepath.Join(s.Paths.StateDir, "kubernetes", "share.json")))
	acted := loops(s)
	r.Calls = nil
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{KubernetesShare: share}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.RestartedUnits) != 0 || len(r.Calls) != 0 || len(*acted) != 0 {
		t.Errorf("restarted %v, ran %v, acted on the loops %v", resp.Msg.RestartedUnits, r.Calls, *acted)
	}
}

// A share that differs in its node CA alone, delivered with config, restarts the kubelet when the
// config changes what it reads, and only then; chalkd's loops are left alone.
func TestApplyConfigNodeCAWithConfig(t *testing.T) {
	s, r := clusterServer(t, k8s.KindControlPlane)
	controlPlane := strings.Replace(workerConfig, "type: worker", "type: controlplane", 1)
	if _, err := sendConfig(s, controlPlane); err != nil {
		t.Fatal(err)
	}
	withNodeCA := func() []byte {
		current, err := knode.ReadShare(s.Kubernetes.Paths)
		if err != nil {
			t.Fatal(err)
		}
		nodeCA := testNodeCA(t)
		current.NodeCA = &nodeCA
		data, err := current.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	acted := loops(s)
	for _, step := range []struct {
		name, config string
		want         []string
	}{
		{"the Node's subnets", strings.Replace(controlPlane, "nodeIP: {addresses: [192.168.100.21]}}", "nodeIP: {addresses: [192.168.100.21], validSubnets: [192.168.100.0/24]}}", 1), []string{"chalkos-controlplane.service", "chalkos-kubernetes.service", "kubelet.service"}},
		{"a Demo", strings.NewReplacer("nodeIP: {addresses: [192.168.100.21]}}", "nodeIP: {addresses: [192.168.100.21], validSubnets: [192.168.100.0/24]}}", "name: first", "name: second").Replace(controlPlane), []string{"demo.service"}},
	} {
		r.Calls, *acted = nil, nil
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: step.config, KubernetesShare: withNodeCA()}))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(resp.Msg.RestartedUnits, step.want) || len(*acted) != 0 {
			t.Errorf("%s: restarted %v, acted on the loops %v; want %v", step.name, resp.Msg.RestartedUnits, *acted, step.want)
		}
	}
}

func TestControlPlaneRenewsItsCertificates(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	if _, err := bootstrap(s, context.Background()); err != nil {
		t.Fatal(err)
	}
	k := s.Kubernetes
	k.mu.Lock()
	leaves := k.leaves
	k.mu.Unlock()
	if leaves == nil {
		t.Fatal("a bootstrapped control plane does not renew its certificates")
	}
	due, expires, err := leaves.Due(time.Now())
	if err != nil || due {
		t.Fatalf("due %v, %v with new certificates", due, err)
	}
	// Due once a third of a year remains.
	if due, _, _ := leaves.Due(expires.Add(-130 * 24 * time.Hour)); due {
		t.Error("due with 130 days left")
	}
	if due, _, _ := leaves.Due(expires.Add(-100 * 24 * time.Hour)); !due {
		t.Error("not due with 100 days left")
	}
	apiServer := filepath.Join(k.Paths.PKI, kpki.FileAPIServer)
	before, _ := os.ReadFile(apiServer)
	if err := leaves.Renew(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(apiServer); bytes.Equal(after, before) {
		t.Error("the API server's certificate was not issued again")
	}
	if k.LeavesProblem() != "" {
		t.Errorf("problem = %q", k.LeavesProblem())
	}
}

// staleStaticPod makes the API server's static pod name other certificates than the current
// ones, as a renewal interrupted before it rendered the pods leaves it, and returns the manifest.
func staleStaticPod(t *testing.T, p knode.Paths) string {
	t.Helper()
	path := filepath.Join(p.Manifests(), "kube-apiserver.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := json.Unmarshal(data, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Annotations[manifests.CertificatesAnnotation] = "the certificates before"
	stale, err := json.MarshalIndent(&pod, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(stale))
	return string(data)
}

func TestControlPlaneRendersStaleStaticPods(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	if _, err := bootstrap(s, context.Background()); err != nil {
		t.Fatal(err)
	}
	k := s.Kubernetes
	p := k.Paths
	k.mu.Lock()
	leaves := k.leaves
	k.mu.Unlock()
	apiServer := filepath.Join(p.PKI, kpki.FileAPIServer)
	before, _ := os.ReadFile(apiServer)

	// The renewal is due at once and renders the pods, without issuing certificates again.
	rendered := staleStaticPod(t, p)
	if due, _, err := leaves.Due(time.Now()); err != nil || !due {
		t.Errorf("due %v, %v with stale static pods, want due", due, err)
	}
	if err := leaves.Renew(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(p.Manifests(), "kube-apiserver.json")); string(got) != rendered {
		t.Error("the renewal did not render the stale static pod")
	}
	if after, _ := os.ReadFile(apiServer); !bytes.Equal(after, before) {
		t.Error("the renewal issued certificates again while the pods ran on the previous ones")
	}

	// The control plane's loop renders them as it starts.
	k.stopLoops()
	staleStaticPod(t, p)
	k.Start()
	eventually(t, "the stale static pod to be rendered", func() bool {
		data, _ := os.ReadFile(filepath.Join(p.Manifests(), "kube-apiserver.json"))
		return string(data) == rendered
	})
}

// Stopping the loops, as before the node leaves etcd, waits for a renewal of the control plane's
// certificates that runs, which would render static pods.
func TestStopLoopsWaitsForTheCertificateRenewal(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	entered, release := make(chan struct{}), make(chan struct{})
	k.leaves = newRenewal("test", func(time.Time) (bool, time.Time, error) {
		return true, time.Time{}, nil
	}, func(context.Context, time.Time) error {
		close(entered)
		<-release
		return nil
	})
	write(t, k.Paths.Bootstrapped(), "")
	write(t, k.Paths.Pin(), "192.168.100.11\n")
	k.Start()
	k.leaves.Force()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the renewal did not run")
	}
	stopped := make(chan struct{})
	go func() { k.stopLoops(); close(stopped) }()
	select {
	case <-stopped:
		t.Error("the loops stopped while a renewal still ran")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Error("the loops did not stop once the renewal ended")
	}
}

// TestObjectsAreTheAddOnsThenTheAddonDocuments: chalkd applies the add-ons it renders for the
// cluster, then the objects of the stored Addon documents.
func TestObjectsAreTheAddOnsThenTheAddonDocuments(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, false)
	k := NewKubernetes()
	k.Slot = s.Loader.Slot
	c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := config.Parse(strings.NewReader(`apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: apps}
spec: {objects: [{apiVersion: v1, kind: Namespace, metadata: {name: apps}}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := slot.Write(slotOf(t, s), slot.Stored{Delivered: docs, Documents: docs, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	objects, err := k.Objects(c)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(objects); n < 2 || objects[0]["kind"] != "ClusterRoleBinding" || objects[n-1]["kind"] != "Namespace" {
		t.Errorf("objects = %v", objects)
	}
}

// clusterServer is a configured node of the kind with a share whose loader writes the cluster
// from the image facts and the delivered config into its Kubernetes side's cluster file.
func clusterServer(t *testing.T, kind string) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := configServer(t)
	addKubernetes(t, s, kind, true)
	s.Loader.ClusterFile = s.Kubernetes.Paths.Cluster
	writeFacts(t, s)
	kubernetesImage(t, s)
	return s, r
}

// withClusterSpec is workerConfig with the fields the YAML mapping spec gives added to its
// Cluster document's endpoint.
func withClusterSpec(spec string) string {
	return strings.Replace(workerConfig, `spec: {endpoint: "https://192.168.100.11:6443"}`,
		`spec: {endpoint: "https://192.168.100.11:6443", `+strings.TrimPrefix(spec, "{"), 1)
}

func TestApplyConfigWritesTheClusterAndRestartsKubernetes(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
	if err != nil || c.Domain != "cluster.local" || c.Images.KubeProxy != "p" {
		t.Fatalf("cluster = %+v, %v", c, err)
	}

	// The same config changes no cluster and restarts nothing.
	r.Calls = nil
	resp, err := sendConfig(s, workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(resp.RestartedUnits, "kubelet.service") {
		t.Errorf("restarted %v for the same cluster", resp.RestartedUnits)
	}

	r.Calls = nil
	if resp, err = sendConfig(s, withClusterSpec("{domain: lab.local}")); err != nil {
		t.Fatal(err)
	}
	if c, _ := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); c.Domain != "lab.local" {
		t.Errorf("domain = %q", c.Domain)
	}
	if !slices.Contains(resp.RestartedUnits, "kubelet.service") || !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
}

// A restart that failed once the config was stored names the units to restart by hand. The
// effects compare a retry with the config and facts from before the delivery that
// failed, so the retry restarts them, although the slot and the cluster file are the delivered
// ones already.
func TestApplyConfigNamesAFailedClusterRestart(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	next := withClusterSpec("{domain: lab.local}")
	r.Rules = []tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}
	if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "restart chalkos-kubernetes.service kubelet.service by hand once that is fixed, or deliver it again") {
		t.Fatalf("err = %v, want the units named", err)
	}
	if c, _ := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); c.Domain != "lab.local" {
		t.Fatalf("domain = %q, want the cluster file written", c.Domain)
	}
	r.Rules, r.Calls = nil, nil
	resp, err := sendConfig(s, next)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}) || !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("the retry restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
	// Applied whole, the delivery leaves no baseline: the next one compares with the slot.
	if _, err := os.Stat(s.Paths.EffectsBaseline); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("baseline after a delivery applied whole: %v", err)
	}
	r.Calls = nil
	if resp, err = sendConfig(s, next); err != nil || len(resp.RestartedUnits) != 0 {
		t.Errorf("the same config again restarted %v: %v", resp.GetRestartedUnits(), err)
	}
}

// loops records what deliveries ask of chalkd's own loops.
func loops(s *Server) *[]string {
	var acted []string
	s.actedOnSelf = func(reload, restart bool) {
		switch {
		case restart:
			acted = append(acted, "restart")
		case reload:
			acted = append(acted, "reload")
		}
	}
	return &acted
}

// A share delivered with the same config, whose slot cannot be written once the share is
// recorded, restarts nothing but chalkd's loops, which read the share. Its retry compares with the
// share from before, so it restarts the kubelet and its preparation and reloads the loops again,
// although STATE holds the delivered share already.
func TestApplyConfigRetriesAShareWhoseSlotFailed(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	acted := loops(s)
	share := testShare(t, k8s.KindWorker)
	send := func() (*nodev1.ApplyConfigResponse, error) {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: workerConfig, KubernetesShare: share}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	slots := filepath.Dir(slotOf(t, s))
	if err := os.Chmod(slots, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slots, 0o755) })
	r.Calls = nil
	if _, err := send(); err == nil || !strings.Contains(err.Error(), "store the config") {
		t.Fatalf("err = %v, want the slot's failure", err)
	}
	if recorded := readFile(t, filepath.Join(s.Paths.StateDir, "kubernetes", "share.json")); !strings.Contains(recorded, "BEGIN CERTIFICATE") {
		t.Fatalf("share.json = %s, want the delivered share recorded", recorded)
	}
	if slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") || !slices.Equal(*acted, []string{"reload"}) {
		t.Errorf("ran %v, acted on the loops %v; want the loops reloaded alone", r.Calls, *acted)
	}
	if err := os.Chmod(slots, 0o755); err != nil {
		t.Fatal(err)
	}
	r.Calls, *acted = nil, nil
	resp, err := send()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}) || !slices.Equal(*acted, []string{"reload"}) {
		t.Errorf("the retry restarted %v, acted on the loops %v", resp.RestartedUnits, *acted)
	}
}

// A share delivered alone whose kubelet restart fails restarts the kubelet when it is delivered
// again. The baseline the retry compares with holds the share's kind and the hashes of its other
// keys, readable by root alone, and none of its certificates or keys.
func TestApplyConfigRetriesAShareWhoseRestartFailed(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	share := testShare(t, k8s.KindWorker)
	send := func() (*nodev1.ApplyConfigResponse, error) {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{KubernetesShare: share}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	r.Rules = []tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}
	if _, err := send(); err == nil || !strings.Contains(err.Error(), "restart chalkos-kubernetes.service kubelet.service by hand") {
		t.Fatalf("err = %v, want the units named", err)
	}
	info, err := os.Stat(s.Paths.EffectsBaseline)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("baseline: %v, %v; want it readable by root alone", info, err)
	}
	if b := readFile(t, s.Paths.EffectsBaseline); strings.Contains(b, "BEGIN") || strings.Contains(b, "s3cret") || !strings.Contains(b, `"kind":"worker"`) {
		t.Errorf("baseline = %s, want the kind and hashes alone", b)
	}
	r.Rules, r.Calls = nil, nil
	resp, err := send()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}) {
		t.Errorf("the retry restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
}

// A slot whose documents cannot be written for the granted units leaves chalkd's loops acted on
// all the same, since what they read is in place, and its retry acts on them again.
func TestApplyConfigActsOnTheLoopsWhenTheGrantedConfigFails(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	acted := loops(s)
	// A file where demo's credentials go.
	credentials := filepath.Join(s.Loader.RunDir, "credentials", "demo")
	if err := os.RemoveAll(credentials); err != nil {
		t.Fatal(err)
	}
	write(t, credentials, "")
	next := withClusterSpec("{domain: lab.local}")
	if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "write the config for the granted units") {
		t.Fatalf("err = %v, want the granted units' config to fail", err)
	}
	if !slices.Equal(*acted, []string{"restart"}) {
		t.Errorf("acted on the loops %v, want them restarted for the new cluster", *acted)
	}
	if err := os.Remove(credentials); err != nil {
		t.Fatal(err)
	}
	*acted = nil
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*acted, []string{"restart"}) {
		t.Errorf("the retry acted on the loops %v", *acted)
	}
}

// A delivery that fails once it wrote the cluster file, here to record its node certificate,
// restarts chalkd's loops all the same, which read the new cluster, and its retry restarts them
// again.
func TestApplyConfigActsOnTheLoopsWhenTheNodeCertificateFails(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	nodeCA := withNodeCertificate(t, s)
	cert, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	acted := loops(s)
	send := func() (*nodev1.ApplyConfigResponse, error) {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{
			Config: withClusterSpec("{domain: lab.local}"), NodeCertificate: []byte(cert.Certificate), NodeKey: []byte(cert.Key),
		}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	dir := filepath.Join(s.Paths.StateDir, "chalkd")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := send(); err == nil || !strings.Contains(err.Error(), "record the node certificate") {
		t.Fatalf("err = %v, want the node certificate's failure", err)
	}
	if c, _ := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); c.Domain != "lab.local" {
		t.Fatalf("domain = %q, want the cluster file written", c.Domain)
	}
	if !slices.Equal(*acted, []string{"restart"}) {
		t.Errorf("acted on the loops %v, want them restarted for the new cluster", *acted)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.Calls, *acted = nil, nil
	resp, err := send()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}) || !slices.Equal(*acted, []string{"restart"}) {
		t.Errorf("the retry restarted %v, acted on the loops %v", resp.RestartedUnits, *acted)
	}
}

// A share delivered alone after a slot whose documents could not be written for the granted
// units writes them before it restarts the units the failed delivery changed, and keeps the
// baseline while it cannot; once it is applied whole, a retry of the config restarts nothing.
func TestApplyConfigWritesTheGrantedConfigForAShareAfterAFailure(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	// A file where demo's credentials go.
	credentials := filepath.Join(s.Loader.RunDir, "credentials", "demo")
	if err := os.RemoveAll(credentials); err != nil {
		t.Fatal(err)
	}
	write(t, credentials, "")
	next := strings.Replace(workerConfig, "token: s3cret", "token: n3w", 1)
	if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "write the config for the granted units") {
		t.Fatalf("err = %v, want the granted units' config to fail", err)
	}
	share := testShare(t, k8s.KindWorker)
	send := func() (*nodev1.ApplyConfigResponse, error) {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{KubernetesShare: share}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	r.Calls = nil
	if _, err := send(); err == nil || !strings.Contains(err.Error(), "write the config for the granted units") {
		t.Fatalf("err = %v, want the granted units' config to fail again", err)
	}
	if slices.Contains(r.Calls, "systemctl try-restart demo.service") {
		t.Errorf("ran %v, want demo left alone on the files from before", r.Calls)
	}
	if _, err := os.Stat(s.Paths.EffectsBaseline); err != nil {
		t.Fatalf("baseline after a share that failed: %v", err)
	}
	if err := os.Remove(credentials); err != nil {
		t.Fatal(err)
	}
	var credential string
	r.Before = func(line string) {
		if line == "systemctl try-restart demo.service" {
			b, _ := os.ReadFile(filepath.Join(credentials, "demo.token"))
			credential = string(b)
		}
	}
	resp, err := send()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "demo.service", "kubelet.service"}) || credential != "n3w" {
		t.Errorf("restarted %v, demo's credential %q when it restarted; want demo on the new one", resp.RestartedUnits, credential)
	}
	if _, err := os.Stat(s.Paths.EffectsBaseline); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("baseline after a delivery applied whole: %v", err)
	}
	r.Before = nil
	if resp, err = sendConfig(s, next); err != nil || len(resp.RestartedUnits) != 0 {
		t.Errorf("the retry of the config restarted %v: %v", resp.GetRestartedUnits(), err)
	}
}

// A cluster file that differs from the one the running slot's config gives, as one a boot whose
// config loader failed left missing, restarts the kubelet too.
func TestApplyConfigRestartsForAClusterFileThatDiffers(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Kubernetes.Paths.Cluster); err != nil {
		t.Fatal(err)
	}
	r.Calls = nil
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("ran %v", r.Calls)
	}
}

// The settings a cluster's certificates, etcd and allocated ranges were made for are refused,
// also when the node's cluster file is missing, as after a boot whose loader failed.
func TestApplyConfigRefusesChangedFixedSettings(t *testing.T) {
	for field, spec := range map[string]string{
		"ipFamilies":   "{ipFamilies: [ipv4, ipv6]}",
		"podCIDRs":     "{podCIDRs: {ipv4: 10.245.0.0/16}}",
		"serviceCIDRs": "{serviceCIDRs: {ipv4: 10.97.0.0/16}}",
		"dnsIPs":       "{dnsIPs: {ipv4: 10.96.0.53}}",
	} {
		t.Run(field, func(t *testing.T) {
			s, r := clusterServer(t, k8s.KindWorker)
			if _, err := sendConfig(s, workerConfig); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(s.Kubernetes.Paths.Cluster); err != nil {
				t.Fatal(err)
			}
			r.Calls = nil
			_, err := sendConfig(s, withClusterSpec(spec))
			if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "cluster's "+field+" from") {
				t.Fatalf("err = %v", err)
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v", r.Calls)
			}
			if exists(s.Kubernetes.Paths.Cluster) {
				t.Error("wrote the refused cluster")
			}
		})
	}
}

// A node holding a share keeps its type: its certificates are those of its kind, and a control
// plane's etcd data stays its own. The type is compared with the share the node holds, also when
// the stored config gives no cluster, and a share of the new type delivered with the config does
// not change it. A node without a share takes any type.
func TestApplyConfigRefusesAnotherNodeType(t *testing.T) {
	controlPlane := strings.Replace(workerConfig, "type: worker", "type: controlplane", 1)
	const refused = `the node's type from "worker" to "controlplane"`
	for name, tc := range map[string]struct {
		share, stored bool
		delivered     []byte
		want          string
	}{
		"a stored cluster":                 {true, true, nil, refused},
		"no stored cluster":                {true, false, nil, refused},
		"a share of the new type with it":  {true, true, testShare(t, k8s.KindControlPlane), refused},
		"no share":                         {false, true, nil, ""},
		"the first config without a share": {false, false, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := configServer(t)
			addKubernetes(t, s, k8s.KindWorker, tc.share)
			s.Loader.ClusterFile = s.Kubernetes.Paths.Cluster
			kubernetesImage(t, s)
			if tc.stored {
				if _, err := sendConfig(s, workerConfig); err != nil {
					t.Fatal(err)
				}
			}
			r.Calls = nil
			_, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: controlPlane, KubernetesShare: tc.delivered}))
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); err != nil || c.Kind != k8s.KindControlPlane {
					t.Errorf("cluster = %+v, %v", c, err)
				}
				return
			}
			if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v", r.Calls)
			}
			if c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); err != nil || c.Kind != k8s.KindWorker {
				t.Errorf("cluster = %+v, %v; want the worker's kept", c, err)
			}
		})
	}
}

// The kubelet applies the node's labels and taints, and the control-plane taint
// allowSchedulingOnControlPlanes leaves off, only when it registers the node, so a node holding a
// share refuses changes to them. The first Node and Cluster documents, unchanged values and a node
// without a share take any.
func TestApplyConfigRefusesChangedRegistrationFields(t *testing.T) {
	node := func(spec string) string {
		return strings.Replace(workerConfig, "nodeIP: {addresses: [192.168.100.21]}}", "nodeIP: {addresses: [192.168.100.21]}, "+spec+"}", 1)
	}
	labelled := node("labels: {node.kubernetes.io/storage: ssd}")
	tainted := node("taints: [{key: dedicated, effect: NoSchedule}]")
	for name, tc := range map[string]struct {
		share         bool
		before, after string
		want          []string
	}{
		"labels added":    {true, workerConfig, labelled, []string{`Node "default" spec.labels: `, "kubectl label"}},
		"labels changed":  {true, labelled, node("labels: {node.kubernetes.io/storage: hdd}"), []string{`Node "default" spec.labels: `, "kubectl label"}},
		"labels removed":  {true, labelled, workerConfig, []string{`Node "default" spec.labels: `}},
		"taints added":    {true, workerConfig, tainted, []string{`Node "default" spec.taints: `, "kubectl taint"}},
		"taints removed":  {true, tainted, workerConfig, []string{`Node "default" spec.taints: `}},
		"scheduling":      {true, workerConfig, withClusterSpec("{allowSchedulingOnControlPlanes: true}"), []string{`Cluster "default" spec.allowSchedulingOnControlPlanes: `, "kubectl taint"}},
		"unchanged":       {true, labelled, labelled, nil},
		"the first Node":  {true, "", labelled, nil},
		"no share":        {false, workerConfig, labelled, nil},
		"no share, taint": {false, workerConfig, tainted, nil},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := configServer(t)
			addKubernetes(t, s, k8s.KindWorker, tc.share)
			s.Loader.ClusterFile = s.Kubernetes.Paths.Cluster
			writeFacts(t, s)
			kubernetesImage(t, s)
			if tc.before != "" {
				if _, err := sendConfig(s, tc.before); err != nil {
					t.Fatal(err)
				}
			}
			r.Calls = nil
			_, err := sendConfig(s, tc.after)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("err = %v", err)
			}
			for _, want := range append(tc.want, "applies when the node registers with the cluster", "or reinstall the node") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want %q", err, want)
				}
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v", r.Calls)
			}
		})
	}
}

// A changed cluster starts a control plane's loop again once the one before ended.
func TestApplyConfigRestartsTheControlPlaneLoop(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindControlPlane)
	k := s.Kubernetes
	p := k.Paths
	ips, err := knode.ReadNodeIPs(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := knode.WritePin(p, ips); err != nil {
		t.Fatal(err)
	}
	write(t, p.Bootstrapped(), "")
	var mu sync.Mutex
	var events []string
	record := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(events)
	}
	k.ControlPlane = func(ctx context.Context, _ kpki.Share, applied func(int)) error {
		record("start")
		applied(1)
		<-ctx.Done()
		// A loop that takes a while to end must have ended before the next starts.
		time.Sleep(50 * time.Millisecond)
		record("end")
		return ctx.Err()
	}
	k.Start()
	eventually(t, "the first loop", func() bool { return len(seen()) == 1 })

	config := strings.Replace(workerConfig, "type: worker", "type: controlplane", 1)
	if _, err := sendConfig(s, config); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the loop after the first config", func() bool { return len(seen()) == 3 })
	if _, err := sendConfig(s, strings.Replace(config, `:6443"}`, `:6443", domain: lab.local}`, 1)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the loop after the changed cluster", func() bool { return len(seen()) == 5 })
	if got := seen(); !slices.Equal(got, []string{"start", "end", "start", "end", "start"}) {
		t.Errorf("events = %v", got)
	}
}

// Without a stored cluster, as on a node whose stored config holds no Cluster document, the
// fixed settings have nothing to compare with.
func TestApplyConfigFirstClusterIsNotCompared(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, withClusterSpec("{podCIDRs: {ipv4: 10.245.0.0/16}}")); err != nil {
		t.Fatal(err)
	}
}

// Ranges and addresses compare as what they denote, not as they are spelt.
func TestApplyConfigComparesRangesCanonically(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, withClusterSpec(`{ipFamilies: [ipv4, ipv6], serviceCIDRs: {ipv4: 10.96.0.0/12, ipv6: "fd00:10:96::/108"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := sendConfig(s, withClusterSpec(`{ipFamilies: [ipv4, ipv6], serviceCIDRs: {ipv4: 10.96.0.0/12, ipv6: "fd00:10:96:0::/108"}, dnsIPs: {ipv6: "fd00:10:96:0::a"}}`)); err != nil {
		t.Fatal(err)
	}
	_, err := sendConfig(s, withClusterSpec(`{ipFamilies: [ipv4, ipv6], serviceCIDRs: {ipv4: 10.96.0.0/12, ipv6: "fd00:10:97::/108"}}`))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "serviceCIDRs") {
		t.Errorf("another range: err = %v", err)
	}
}

// A control plane applies the objects of changed Addon documents at once, without restarting
// a unit, from the config stored by then.
func TestApplyConfigAppliesChangedAddons(t *testing.T) {
	s, r := clusterServer(t, k8s.KindControlPlane)
	k := s.Kubernetes
	p := k.Paths
	ips, err := knode.ReadNodeIPs(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := knode.WritePin(p, ips); err != nil {
		t.Fatal(err)
	}
	write(t, p.Bootstrapped(), "")
	k.Slot = s.Loader.Slot
	k.Objects = k.objects
	applied := make(chan string, 10)
	k.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		c, err := k8s.ReadCluster(p.Cluster)
		if err != nil {
			return err
		}
		objects, err := k.Objects(c)
		if err != nil {
			return err
		}
		last, _ := objects[len(objects)-1]["metadata"].(map[string]any)
		applied <- fmt.Sprint(last["name"])
		<-ctx.Done()
		return ctx.Err()
	}
	config := strings.Replace(workerConfig, "type: worker", "type: controlplane", 1)
	if _, err := sendConfig(s, config); err != nil {
		t.Fatal(err)
	}
	if got := <-applied; got != "kube-dns" {
		t.Errorf("applied up to %s, want chalkos's add-ons alone", got)
	}

	r.Calls = nil
	resp, err := sendConfig(s, config+`---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: apps}
spec: {objects: [{apiVersion: v1, kind: Namespace, metadata: {name: apps}}]}
`)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-applied:
		if got != "apps" {
			t.Errorf("applied up to %s, want the Addon's namespace", got)
		}
	case <-time.After(time.Minute):
		t.Fatal("the changed Addon documents were not applied")
	}
	if len(resp.RestartedUnits) != 0 || slices.ContainsFunc(r.Calls, func(c string) bool { return strings.HasPrefix(c, "systemctl restart") }) {
		t.Errorf("restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
}

// Objects that fail to apply, such as a bad Addon document's, leave the approver of kubelet
// serving certificates running while the apply retries.
func TestApproverRunsWhileObjectsFail(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	var ready atomic.Bool
	ready.Store(true)
	cfg := readyServer(t, &ready)
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	approving := make(chan struct{})
	k.Approve = func(ctx context.Context, _ *rest.Config) {
		close(approving)
		<-ctx.Done()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- k.serveControlPlane(ctx, c, cfg, func(int) { t.Error("applied objects that fail") })
	}()
	select {
	case <-approving:
	case <-time.After(time.Minute):
		t.Fatal("the approver did not start")
	}
	cancel()
	if err := <-served; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

// A worker applies no Addon documents: chalkctl delivers none to it, and a changed one it is
// given anyway restarts no unit.
func TestApplyConfigOnAWorkerKeepsChangedAddons(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	r.Calls = nil
	resp, err := sendConfig(s, workerConfig+`---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: apps}
spec: {objects: [{apiVersion: v1, kind: Namespace, metadata: {name: apps}}]}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.RestartedUnits) != 0 || slices.ContainsFunc(r.Calls, func(c string) bool { return strings.Contains(c, "restart") }) {
		t.Errorf("restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
}

// The add-ons are applied whether or not the revoked nodes' admission policy can be: the
// approver applies the policy and reports its failures apart, so an API server that refuses the
// policy, here by answering nothing but its readiness, still gets the add-ons.
func TestApplyOnceLeavesTheRevokedNodesToTheApprover(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	var ready atomic.Bool
	ready.Store(true)
	cfg := readyServer(t, &ready)
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	k.Objects = func(k8s.Cluster) ([]map[string]any, error) { return nil, nil }
	if n, err := k.applyOnce(context.Background(), c, cfg); err != nil || n != 0 {
		t.Errorf("applyOnce = %d, %v", n, err)
	}
}
