package kubernetes

import (
	"slices"
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
