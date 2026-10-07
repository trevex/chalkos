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
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"k8s.io/client-go/rest"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// kubernetesServer is an installed node of the kind with the files chalkd reads; with a share
// when share is set. Its control plane applies 19 objects at once.
func kubernetesServer(t *testing.T, kind string, share bool) (*Server, *fakeRunner) {
	t.Helper()
	s, r := installedServer(t, section("", ""), false)
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
		Manifests:    filepath.Join(root, "etc", "manifests.json"),
		ControlPlane: func(_ context.Context, _ kpki.Share, applied func(int)) error { applied(19); return nil },
		NodeReady:    func(context.Context) (string, error) { return "True", nil },
		// No cluster answers at the endpoint.
		EtcdEndpoints: func(context.Context, k8s.Cluster, kpki.Share, []net.IP) ([]string, error) {
			return nil, errors.New("connection refused")
		},
		ClusterAnswers: func(context.Context, k8s.Cluster, kpki.Share) bool { return false },
	}
	p := s.Kubernetes.Paths
	write(t, p.Cluster, `{"kind": "`+kind+`", "endpoint": "https://192.168.100.11:6443", "version": "1.37.1",
	  "podCIDR": "10.244.0.0/16", "serviceCIDR": "10.96.0.0/12", "dnsIP": "10.96.0.10", "domain": "cluster.local",
	  "extraArgs": {}, "images": {"etcd": "e", "kubeAPIServer": "a", "kubeControllerManager": "c", "kubeScheduler": "s"}}`)
	write(t, p.NodeFile, `{"hostname": "n1", "kubernetes": {"nodeName": "n1", "nodeIPs": ["192.168.100.11"]}}`)
	if share {
		write(t, p.Share(), string(testShare(t, kind)))
		if err := knode.Prepare(p, time.Now(), nodeIP("192.168.100.11"), nil); err != nil {
			t.Fatal(err)
		}
	}
	return s, r
}

// nodeIP is a resolver that finds the address at once.
func nodeIP(ip string) knode.Resolver {
	return func(nodeip.Selector, time.Duration) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
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
	share, err := kpki.ShareFor(k, kind, nodeName, time.Now())
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
	otherKind, _ := kubernetesServer(t, k8s.KindControlPlane, false)
	otherKindsShare(t, otherKind)
	noFirewall, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	withoutFirewall(t, noFirewall)
	for name, tc := range map[string]struct {
		s    *Server
		want string
	}{
		"no Kubernetes":         {plain, "has no Kubernetes"},
		"worker":                {worker, "is a worker"},
		"no share":              {noShare, "no Kubernetes share"},
		"etcd with data":        {etcdData, "holds etcd data"},
		"no address":            {noAddress, "no node address matches validSubnets 192.168.100.0/24"},
		"preparing":             {preparing, "the node's Kubernetes files are not prepared yet; see chalkctl logs <node> --unit chalkos-kubernetes"},
		"share of another kind": {otherKind, "preparation failed: the node's share is for a worker node, but its image is for controlplane nodes"},
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
	return `{"hostname": "n1", "networkUnits": {}, "extensions": {"rack": {"location": "rack-a"}}, "kubernetes": {"nodeName": "` + nodeName + `", "nodeIPs": ["192.168.100.11"]}, "storage": ` + section("", "") + `}`
}

func TestApplyIdentityDeliversShare(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, false)
	share := testShare(t, k8s.KindWorker)
	// STATE keeps the share as Encode writes it, not as it was delivered.
	var delivered bytes.Buffer
	if err := json.Indent(&delivered, share, "", "  "); err != nil {
		t.Fatal(err)
	}
	resp, err := s.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{
		Identity: kubernetesIdentity("n1"), KubernetesShare: delivered.Bytes(),
	}))
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
	if !slices.Contains(r.calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("commands %v", r.calls)
	}
	if !slices.Contains(resp.Msg.RestartedUnits, "kubelet.service") {
		t.Errorf("restarted %v", resp.Msg.RestartedUnits)
	}
}

func TestApplyIdentityRefusesShare(t *testing.T) {
	plain, _ := installedServer(t, section("", ""), false)
	worker, r := kubernetesServer(t, k8s.KindWorker, false)
	for name, tc := range map[string]struct {
		s        *Server
		identity string
		share    []byte
		code     connect.Code
	}{
		"no Kubernetes":        {plain, kubernetesIdentity("n1"), testShare(t, k8s.KindWorker), connect.CodeInvalidArgument},
		"broken share":         {worker, kubernetesIdentity("n1"), []byte(`{"kind": "worker"}`), connect.CodeInvalidArgument},
		"another node's share": {worker, kubernetesIdentity("n1"), testShareFor(t, k8s.KindWorker, "n2"), connect.CodeInvalidArgument},
		"no node name":         {worker, identityWith("rack-a", section("", "")), testShare(t, k8s.KindWorker), connect.CodeInvalidArgument},
	} {
		_, err := tc.s.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{
			Identity: tc.identity, KubernetesShare: tc.share,
		}))
		if connect.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %v", name, err, tc.code)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("a refused share ran %v", r.calls)
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
	worker.Kubernetes.NodeReady = func(context.Context) (string, error) { return "", errors.New("connection refused") }
	plain, _ := installedServer(t, section("", ""), false)
	for name, tc := range map[string]struct {
		s    *Server
		want *nodev1.KubernetesStatus
	}{
		"waiting":               {waiting, &nodev1.KubernetesStatus{Kind: "controlplane", State: "waiting for bootstrap or for the cluster at https://192.168.100.11:6443"}},
		"bootstrapped":          {bootstrapped, &nodev1.KubernetesStatus{Kind: "controlplane", State: "bootstrapped", NodeReady: "True"}},
		"no share":              {noShare, &nodev1.KubernetesStatus{Kind: "worker", State: "no share"}},
		"no address":            {noAddress, &nodev1.KubernetesStatus{Kind: "worker", State: "preparation failed: no node address matches the default filter (the node has no addresses)"}},
		"share of another kind": {otherKind, &nodev1.KubernetesStatus{Kind: "worker", State: "preparation failed: the node's share is for a controlplane node, but its image is for worker nodes"}},
		"firewall":              {noFirewall, &nodev1.KubernetesStatus{Kind: "worker", State: "preparation failed: accept VXLAN to the node's address: exit status 1"}},
		"preparing":             {preparing, &nodev1.KubernetesStatus{Kind: "controlplane", State: "preparing"}},
		"writing":               {writing, &nodev1.KubernetesStatus{Kind: "worker", State: "preparing"}},
		"worker":                {worker, &nodev1.KubernetesStatus{Kind: "worker", State: "joined", NodeReady: "unknown: connection refused"}},
		"no Kubernetes":         {plain, nil},
	} {
		resp, err := tc.s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := resp.Msg.Kubernetes
		if (got == nil) != (tc.want == nil) || got != nil && (got.Kind != tc.want.Kind || got.State != tc.want.State || got.NodeReady != tc.want.NodeReady) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := k.applyOnce(ctx, cfg); err == nil {
		t.Fatal("applied without a ready API server")
	}
	if initialised, err := knode.EtcdInitialised(k.Paths); err != nil || initialised {
		t.Fatalf("before readiness: initialised = %v, %v", initialised, err)
	}

	ready.Store(true)
	// The manifests do not exist, so applying fails after the API server answered ready.
	if _, err := k.applyOnce(context.Background(), cfg); err == nil {
		t.Fatal("applied manifests that do not exist")
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

func TestApplyIdentityRefusesShareOfAnotherKind(t *testing.T) {
	for image, share := range map[string]string{k8s.KindWorker: k8s.KindControlPlane, k8s.KindControlPlane: k8s.KindWorker} {
		s, r := kubernetesServer(t, image, false)
		identity := filepath.Join(s.Paths.StateDir, "identity.json")
		before, _ := os.ReadFile(identity)
		_, err := s.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{
			Identity: kubernetesIdentity("n1"), KubernetesShare: testShare(t, share),
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), share) {
			t.Errorf("%s share on a %s image: %v", share, image, err)
		}
		if len(r.calls) != 0 {
			t.Errorf("%s share on a %s image ran %v", share, image, r.calls)
		}
		if _, err := os.Stat(s.Kubernetes.Paths.Share()); err == nil {
			t.Errorf("%s share on a %s image was recorded", share, image)
		}
		if after, _ := os.ReadFile(identity); !bytes.Equal(after, before) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	case <-time.After(10 * time.Second):
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
	case <-time.After(10 * time.Second):
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
func TestApplyIdentityRestartsKubernetesOnChange(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, true)
	const restart = "systemctl restart chalkos-kubernetes.service kubelet.service"
	base := kubernetesIdentity("n1")
	edit := func(f func(id map[string]any)) string {
		t.Helper()
		var id map[string]any
		if err := json.Unmarshal([]byte(base), &id); err != nil {
			t.Fatal(err)
		}
		f(id)
		data, err := json.Marshal(id)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, step := range []struct {
		name, identity string
		want           []string
	}{
		// The recorded identity has no Kubernetes section yet.
		{"kubernetes added", base, []string{restart}},
		{"unchanged", base, nil},
		{"extension", edit(func(id map[string]any) {
			id["extensions"] = map[string]any{"rack": map[string]any{"location": "rack-b"}}
		}), nil},
		{"labels", edit(func(id map[string]any) { id["labels"] = map[string]any{"node.kubernetes.io/storage": "ssd"} }), []string{restart}},
		{"taints", edit(func(id map[string]any) {
			id["taints"] = []any{map[string]any{"key": "dedicated", "value": nil, "effect": "NoSchedule"}}
		}), []string{restart}},
		{"nodeIPs", edit(func(id map[string]any) {
			id["kubernetes"] = map[string]any{"nodeName": "n1", "nodeIPs": []any{"192.168.100.21"}}
		}), []string{restart}},
		{"validSubnets", edit(func(id map[string]any) {
			id["kubernetes"] = map[string]any{"nodeName": "n1", "nodeIPs": []any{}, "validSubnets": []any{"192.168.100.0/24"}}
		}), []string{restart}},
	} {
		r.mu.Lock()
		r.calls = nil
		r.mu.Unlock()
		resp, err := apply(s, step.identity, "")
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		var got []string
		for _, c := range r.calls {
			if strings.Contains(c, "kubernetes") || strings.Contains(c, "kubelet") || strings.Contains(c, "firewall") {
				got = append(got, c)
			}
		}
		if !slices.Equal(got, step.want) {
			t.Errorf("%s: commands %v, want %v", step.name, got, step.want)
		}
		if restarted := slices.Contains(resp.RestartedUnits, "kubelet.service"); restarted != slices.Contains(step.want, restart) {
			t.Errorf("%s: restarted %v", step.name, resp.RestartedUnits)
		}
	}
}

// A share delivered to a running control plane reaches its loop at once, which starts again
// with it.
func TestApplyIdentityRestartsControlPlaneLoopWithNewShare(t *testing.T) {
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
	if _, err := s.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{
		Identity: kubernetesIdentity("n1"), KubernetesShare: replaced,
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-shares:
		if second.CA.Certificate != want.CA.Certificate {
			t.Error("the loop started again with the old share")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the running loop did not start again with the delivered share")
	}
}

// A node that finds no address keeps the identity, runs no kubelet, and says why.
func TestApplyIdentityReportsMissingAddress(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, true)
	const reason = "no node address matches validSubnets 192.168.100.0/24 (the node has 10.0.2.15 on eth0)"
	r.rules = append([]rule{{prefix: "systemctl restart chalkos-kubernetes.service", err: errors.New("exit status 1")}}, r.rules...)
	withoutAddress(t, s, reason)
	_, err := apply(s, kubernetesIdentity("n1"), "")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), reason) {
		t.Errorf("err = %v, want the missing address", err)
	}
}

// A restart that fails says why: the preparation's reason, or else the restart's own error.
func TestApplyIdentityReportsFailedRestart(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, false)
	r.rules = append([]rule{{prefix: "systemctl restart chalkos-kubernetes.service", err: errors.New("exit status 1")}}, r.rules...)
	otherKindsShare(t, s)
	_, err := apply(s, kubernetesIdentity("n1"), "")
	want := "the node runs no kubelet: preparation failed: the node's share is for a controlplane node, but its image is for worker nodes"
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
		t.Errorf("share of another kind: err = %v, want %s", err, want)
	}

	// The preparation succeeded; the restart failed for another reason.
	other, r := kubernetesServer(t, k8s.KindWorker, true)
	r.rules = append([]rule{{prefix: "systemctl restart chalkos-kubernetes.service", err: errors.New("exit status 1")}}, r.rules...)
	_, err = apply(other, kubernetesIdentity("n1"), "")
	want = "restart chalkos-kubernetes.service kubelet.service: exit status 1"
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "address") {
		t.Errorf("failed restart: err = %v, want %s", err, want)
	}
}

func TestApplyIdentityRefusesInvalidAddressSettings(t *testing.T) {
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
	if len(r.calls) != 0 {
		t.Errorf("a refused identity ran %v", r.calls)
	}
}

// Once chalkd stops, a Start or Reload that arrives meanwhile, as from ApplyIdentity, starts no
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
