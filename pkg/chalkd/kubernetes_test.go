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
	s.Kubernetes = NewKubernetes(KubernetesOptions{
		Paths: knode.Paths{
			State:      filepath.Join(s.Paths.StateDir, "kubernetes"),
			Cluster:    filepath.Join(root, "etc", "cluster.json"),
			NodeFile:   filepath.Join(root, "run", "node.json"),
			Run:        filepath.Join(root, "run", "kubernetes"),
			PKI:        filepath.Join(root, "run", "kubernetes", "pki"),
			KubeletPKI: filepath.Join(root, "var", "lib", "kubelet", "pki"),
			EtcdData:   filepath.Join(root, "var", "lib", "etcd"),
		},
		Hooks: KubernetesHooks{
			Slot:         s.Loader.Slot,
			Objects:      func(k8s.Cluster) ([]map[string]any, error) { return nil, errors.New("no objects in this test") },
			ControlPlane: func(_ context.Context, _ kpki.Share, applied func(int)) error { applied(19); return nil },
			Node:         func(context.Context) (*corev1.Node, error) { return readyNode(), nil },
			// No cluster answers at the endpoint.
			EtcdEndpoints: func(context.Context, k8s.Cluster, kpki.Share, []netip.Addr) ([]string, error) {
				return nil, errors.New("connection refused")
			},
			ClusterAnswers: func(context.Context, k8s.Cluster, kpki.Share) bool { return false },
			// The control plane runs with the certificates the node holds.
			ControlPlaneState: func(context.Context, k8s.Cluster, kpki.Share) string { return controlPlaneCurrent },
			// The API server keeps no objects.
			APIClient: func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return fake.NewClientset(), nil },
		},
	})
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
	s.Kubernetes.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error { <-ctx.Done(); return ctx.Err() }
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
	worker.Kubernetes.hooks.Node = func(context.Context) (*corev1.Node, error) { return nil, errors.New("connection refused") }
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
		s.Kubernetes.hooks.Node = func(context.Context) (*corev1.Node, error) { return readyNode(podCIDRs...), nil }
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

// The control plane's loop starts again after an error, with the share as STATE holds it then.
func TestControlPlaneLoopRestarts(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	k.timing.RestartBackoff = 10 * time.Millisecond
	replaced := testShare(t, k8s.KindControlPlane)
	want, err := kpki.ParseShare(replaced)
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	var second kpki.Share
	k.hooks.ControlPlane = func(_ context.Context, share kpki.Share, applied func(int)) error {
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
	k.timing.RestartBackoff, k.timing.MaxRestartBackoff = 10*time.Millisecond, 20*time.Millisecond
	runs := make(chan struct{}, 1)
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
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
	k.etcdURL = etcdtest.StartAdvertising(t, *share.EtcdCA, "n1", "https://192.168.100.11:2380").ClientURL
	ran := make(chan struct{})
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
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
	k.etcdURL = etcdtest.StartAdvertising(t, *share.EtcdCA, "n1", "https://192.168.100.12:2380").ClientURL
	k.timing.RestartBackoff, k.timing.MaxRestartBackoff = 10*time.Millisecond, 20*time.Millisecond
	var ran atomic.Bool
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
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

// Once chalkd stops, a Start or Reload that arrives meanwhile, as from ApplyConfig, starts no
// loop again.
func TestStartAfterStopDoesNothing(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	p := k.Paths
	var runs atomic.Int32
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
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
	k := NewKubernetes(KubernetesOptions{Hooks: KubernetesHooks{Slot: s.Loader.Slot}})
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
	objects, err := k.hooks.Objects(c)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(objects); n < 2 || objects[0]["kind"] != "ClusterRoleBinding" || objects[n-1]["kind"] != "Namespace" {
		t.Errorf("objects = %v", objects)
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
	k.hooks.Approve = func(ctx context.Context, _ *rest.Config) {
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
	k.hooks.Objects = func(k8s.Cluster) ([]map[string]any, error) { return nil, nil }
	if n, err := k.applyOnce(context.Background(), c, cfg); err != nil || n != 0 {
		t.Errorf("applyOnce = %d, %v", n, err)
	}
}
