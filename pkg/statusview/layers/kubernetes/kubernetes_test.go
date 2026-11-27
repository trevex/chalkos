package kubernetes

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
)

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

func TestRevokedLines(t *testing.T) {
	if got := revokedLines(&kubernetesv1.StatusResponse{Kind: "worker"}); got != nil {
		t.Errorf("a worker's lines = %q", got)
	}
	got := revokedLines(&kubernetesv1.StatusResponse{RevokedNodesError: "read the revoked nodes: connection refused"})
	if want := []string{"revoked nodes unknown: read the revoked nodes: connection refused"}; !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// The revoked nodes' fill is warned about past 80%, naming the prune and the CA rotations that make room.
func TestRevokedFill(t *testing.T) {
	k := &kubernetesv1.StatusResponse{
		RevokedNodes:        []*kubernetesv1.RevokedNode{{Name: "w1"}},
		RevokedListLength:   72_001,
		RevokedListCapacity: 90_000,
	}
	if p, warn := RevokedFill(k.RevokedListLength, k.RevokedListCapacity); p != 80 || !warn {
		t.Errorf("RevokedFill = %d, %v; want 80 and a warning past 80%%", p, warn)
	}
	want := []string{
		"revoked nodes:",
		"  w1\tat an unknown time",
		"  the list is 80% full; " + MakeRevokedRoom,
	}
	if got := revokedLines(k); !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	var b bytes.Buffer
	if err := Write(&b, k); err != nil {
		t.Fatal(err)
	}
	if want := "revoked nodes:\n  w1  at an unknown time\n  the list is 80% full; " + MakeRevokedRoom + "\n"; !strings.Contains(b.String(), want) {
		t.Errorf("no %q in\n%s", want, b.String())
	}
	if !strings.HasPrefix(MakeRevokedRoom, "drop the expired revocations with chalkctl node prune-revoked") ||
		!strings.Contains(MakeRevokedRoom, "chalkctl rotate os-ca and chalkctl rotate kubernetes-ca, each to its finish") {
		t.Errorf("MakeRevokedRoom = %q", MakeRevokedRoom)
	}
	if p := Problems(k); !slices.Equal(p, []string{"revoked nodes list 80% full"}) {
		t.Errorf("problems %q", p)
	}
	k.RevokedListLength = 72_000
	if _, warn := RevokedFill(k.RevokedListLength, k.RevokedListCapacity); warn {
		t.Error("exactly 80% warns")
	}
	if len(Problems(k)) != 0 || len(revokedLines(k)) != 2 {
		t.Errorf("at 80%%: problems %q, lines %q", Problems(k), revokedLines(k))
	}
	if p, warn := RevokedFill(0, 0); p != 0 || warn {
		t.Error("a worker's status warns")
	}
}

func TestVersionsLine(t *testing.T) {
	for want, k := range map[string]*kubernetesv1.StatusResponse{
		"kubernetes versions: API server 1.37.2, kubelet 1.37.1": {Kind: "worker", ApiServerVersion: "1.37.2", KubeletVersion: "1.37.1"},
		"kubernetes versions: API server 1.37.2, kubelet 1.37.1, control-plane pods kube-apiserver is not Ready": {
			Kind: "controlplane", ApiServerVersion: "1.37.2", KubeletVersion: "1.37.1", ControlPlanePods: "kube-apiserver is not Ready",
		},
	} {
		if got := versionsLine(k); got != want {
			t.Errorf("versionsLine(%v) = %q, want %q", k, got, want)
		}
	}
}

// TestPodsAndReadyReason checks that the section names the pods that do not run, or why they
// could not be read, and the reason of a Ready condition that is not True.
func TestPodsAndReadyReason(t *testing.T) {
	k := &kubernetesv1.StatusResponse{Kind: "worker", State: "joined", NodeReady: "False", NodeReadyReason: "KubeletNotReady",
		NodeReadyMessage: "container runtime network not ready",
		PodsNotRunning: []*kubernetesv1.NodePod{
			{Namespace: "web", Name: "crash", Phase: "Running", Reason: "CrashLoopBackOff"},
			{Namespace: "web", Name: "pending-pod", Phase: "Pending"},
		}, PodsNotRunningTotal: 3}
	var b bytes.Buffer
	if err := Write(&b, k); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kubernetes worker: joined, node ready: False (KubeletNotReady: container runtime network not ready)\n",
		"pods not running:\n  web/crash        Running  CrashLoopBackOff\n  web/pending-pod  Pending  -\n  and 1 more\n",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("no %q in\n%s", want, b.String())
		}
	}
	b.Reset()
	if err := Write(&b, &kubernetesv1.StatusResponse{Kind: "worker", NodeReady: "True", NodeReadyReason: "KubeletReady", PodsError: "forbidden"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "KubeletReady") || !strings.Contains(b.String(), "pods unknown: forbidden\n") {
		t.Errorf("section:\n%s", b.String())
	}
}

// A failed preparation and a control plane that does not serve the node's certificates are
// problems; a worker's empty control-plane state is not.
func TestProblems(t *testing.T) {
	for _, tc := range []struct {
		k    *kubernetesv1.StatusResponse
		want []string
	}{
		{&kubernetesv1.StatusResponse{Kind: "worker", State: "joined", Phase: kubernetesv1.Phase_PHASE_JOINED, NodeReady: "True"}, nil},
		{&kubernetesv1.StatusResponse{Kind: "worker", State: "preparing", Phase: kubernetesv1.Phase_PHASE_PREPARING}, nil},
		{&kubernetesv1.StatusResponse{Kind: "worker", State: "preparation failed: no node address", Phase: kubernetesv1.Phase_PHASE_PREPARATION_FAILED}, []string{"Kubernetes preparation failed"}},
		{&kubernetesv1.StatusResponse{Kind: "control-plane", Phase: kubernetesv1.Phase_PHASE_ETCD_DATA_MISSING}, []string{"etcd data missing"}},
		{&kubernetesv1.StatusResponse{
			Kind: "controlplane", State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, NodeReady: "True", ControlPlanePods: "current",
			ControlPlane: "the API server serves certificates the node replaced",
		}, []string{"control plane not current"}},
		{&kubernetesv1.StatusResponse{
			Kind: "controlplane", State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, NodeReady: "True", ControlPlanePods: "current",
			ControlPlane: "current", ControlPlaneCurrent: true,
		}, nil},
	} {
		if got := Problems(tc.k); !slices.Equal(got, tc.want) {
			t.Errorf("Problems(%v) = %q, want %q", tc.k, got, tc.want)
		}
	}
}

// A node whose config gives no cluster has one line, and no problem.
func TestNoCluster(t *testing.T) {
	k := &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_NO_CLUSTER, State: "no cluster: the node's config gives none for this image"}
	if got, want := StatusLines(k), []string{"kubernetes: no cluster: the node's config gives none for this image"}; !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	if p := Problems(k); len(p) != 0 {
		t.Errorf("problems = %q", p)
	}
}
