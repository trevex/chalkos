package kubernetes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// kubelet stands for the kubelet's health endpoint, healthy or not.
func kubelet(t *testing.T, healthy *bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !*healthy {
			http.Error(w, "not healthy", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/healthz"
}

func checkHealth(t *testing.T, h *chalkd.Health, want string) {
	t.Helper()
	err := h.Check(context.Background())
	switch {
	case want == "" && err != nil:
		t.Errorf("not healthy: %v", err)
	case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
		t.Errorf("health = %v, want %q", err, want)
	}
}

func TestHealthOfAWorker(t *testing.T) {
	addr, cert := kubernetestest.ChalkdServing(t)
	for _, tc := range []struct {
		name    string
		share   bool
		kubelet bool
		node    error
		want    string
	}{
		{"healthy", true, true, nil, ""},
		{"kubelet not healthy", true, false, nil, "the kubelet is not healthy"},
		{"Node not registered", true, true, errors.New("nodes \"n1\" not found"), "the Node is not registered"},
		// A worker not given a share yet belongs to no cluster.
		{"no share", false, false, errors.New("no kubeconfig"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := kubernetesNode(t, k8s.KindWorker, tc.share)
			s.k.hooks.Node = func(context.Context) (*corev1.Node, error) { return kubernetestest.ReadyNode(), tc.node }
			healthy := tc.kubelet
			s.k.kubelet = kubelet(t, &healthy)
			h := &chalkd.Health{Run: r, Layers: []layer.Health{s}, Chalkd: addr, NodeCertificate: cert}
			checkHealth(t, h, tc.want)
		})
	}
	s, r := kubernetesNode(t, k8s.KindWorker, true)
	kubernetestest.WithoutAddress(t, s.k.paths, "no ipv4 node address matches validSubnets 10.0.0.0/8")
	healthy := true
	s.k.kubelet = kubelet(t, &healthy)
	checkHealth(t, &chalkd.Health{Run: r, Layers: []layer.Health{s}, Chalkd: addr, NodeCertificate: cert}, "preparation failed: no ipv4 node address")
}

func TestHealthOfAControlPlane(t *testing.T) {
	addr, cert := kubernetestest.ChalkdServing(t)
	s, _, members := memberNode(t)
	k := s.k
	ready, healthy := true, true
	k.hooks.APIServerReady = func(context.Context, k8s.Cluster, kpki.Share) bool { return ready }
	k.kubelet = kubelet(t, &healthy)
	h := &chalkd.Health{Run: &tooltest.Runner{}, Layers: []layer.Health{s}, Chalkd: addr, NodeCertificate: cert}
	checkHealth(t, h, "")
	ready = false
	checkHealth(t, h, "the API server is not ready")
	ready, healthy = true, false
	checkHealth(t, h, "the kubelet is not healthy")
	healthy = true
	k.timing.EtcdTimeout = 2 * time.Second
	members["n1"].Stop()
	checkHealth(t, h, "the etcd member is not healthy")

	// A control plane waiting for its bootstrap is healthy once its identity was applied.
	s2, r := kubernetesNode(t, k8s.KindControlPlane, true)
	if bootstrapped, _ := knode.Bootstrapped(s2.k.paths); bootstrapped {
		t.Fatal("bootstrapped")
	}
	s2.k.kubelet = "http://127.0.0.1:1/healthz"
	checkHealth(t, &chalkd.Health{Run: r, Layers: []layer.Health{s2}, Chalkd: addr, NodeCertificate: cert}, "")
}
