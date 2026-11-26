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
