package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
)

// revokedNodesOf counts a control plane whose image has no Kubernetes layer among those not
// bootstrapped: alone it leaves the list empty, beside one that gives the list it is passed over.
func TestRevokedNodesOfWithoutTheKubernetesLayer(t *testing.T) {
	read := &kubernetesv1.StatusResponse{State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, RevokedNodesRead: true,
		RevokedNodes: []*kubernetesv1.RevokedNode{{Name: "w1"}}}
	status := func(_ context.Context, name string) (*kubernetesv1.StatusResponse, error) {
		switch name {
		case "read":
			return read, nil
		case "down":
			return nil, errors.New("connection refused")
		}
		return nil, fmt.Errorf("%s: %w", name, errNoKubernetes)
	}
	if list, err := revokedNodesOf(context.Background(), []string{"router"}, status); err != nil || len(list) != 0 {
		t.Errorf("without the layer: %v, %v; want no revoked node", list, err)
	}
	if list, err := revokedNodesOf(context.Background(), []string{"router", "read"}, status); err != nil || list["w1"] == nil {
		t.Errorf("beside a control plane that reads them: %v, %v; want w1", list, err)
	}
	_, err := revokedNodesOf(context.Background(), []string{"router", "down"}, status)
	if err == nil || !strings.Contains(err.Error(), "router: is not bootstrapped: no Kubernetes") || !strings.Contains(err.Error(), "down: connection refused") {
		t.Errorf("beside one that is down: %v, want a refusal naming both", err)
	}
}
