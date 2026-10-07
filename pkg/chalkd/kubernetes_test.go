package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
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
		ControlPlane: func(_ context.Context, _ kpki.Share, applied func(int)) { applied(19) },
		NodeReady:    func(context.Context) (string, error) { return "True", nil },
	}
	p := s.Kubernetes.Paths
	write(t, p.Cluster, `{"kind": "`+kind+`", "endpoint": "https://192.168.100.11:6443", "version": "1.37.1",
	  "podCIDR": "10.244.0.0/16", "serviceCIDR": "10.96.0.0/12", "dnsIP": "10.96.0.10", "domain": "cluster.local",
	  "extraArgs": {}, "images": {"etcd": "e", "kubeAPIServer": "a", "kubeControllerManager": "c", "kubeScheduler": "s"}}`)
	write(t, p.NodeFile, `{"hostname": "n1", "kubernetes": {"nodeName": "n1", "nodeIP": "192.168.100.11"}}`)
	if share {
		write(t, p.Share(), string(testShare(t, kind)))
		if err := knode.Prepare(p, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return s, r
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
	for name, tc := range map[string]struct {
		s    *Server
		want string
	}{
		"no Kubernetes":  {plain, "has no Kubernetes"},
		"worker":         {worker, "is a worker"},
		"no share":       {noShare, "no Kubernetes share"},
		"etcd with data": {etcdData, "holds etcd data"},
	} {
		_, err := bootstrap(tc.s, context.Background())
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
		if k := tc.s.Kubernetes; k != nil {
			if bootstrapped, _ := knode.Bootstrapped(k.Paths); bootstrapped {
				t.Errorf("%s: the refused node is marked bootstrapped", name)
			}
		}
	}
}

func TestBootstrapReturnsBeforeManifestsApplied(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	s.Kubernetes.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) { <-ctx.Done() }
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
	return `{"hostname": "n1", "networkUnits": {}, "extensions": {"rack": {"location": "rack-a"}}, "kubernetes": {"nodeName": "` + nodeName + `", "nodeIP": "192.168.100.11"}, "storage": ` + section("", "") + `}`
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
		"no Kubernetes":        {plain, kubernetesIdentity("n1"), testShare(t, k8s.KindWorker), connect.CodeFailedPrecondition},
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
	worker.Kubernetes.NodeReady = func(context.Context) (string, error) { return "", errors.New("connection refused") }
	plain, _ := installedServer(t, section("", ""), false)
	for name, tc := range map[string]struct {
		s    *Server
		want *nodev1.KubernetesStatus
	}{
		"waiting":       {waiting, &nodev1.KubernetesStatus{Kind: "controlplane", State: "waiting for bootstrap"}},
		"bootstrapped":  {bootstrapped, &nodev1.KubernetesStatus{Kind: "controlplane", State: "bootstrapped", NodeReady: "True"}},
		"no share":      {noShare, &nodev1.KubernetesStatus{Kind: "worker", State: "no share"}},
		"worker":        {worker, &nodev1.KubernetesStatus{Kind: "worker", State: "joined", NodeReady: "unknown: connection refused"}},
		"no Kubernetes": {plain, nil},
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
