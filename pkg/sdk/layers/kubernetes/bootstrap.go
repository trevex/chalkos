package kubernetes

import (
	"context"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/client"
)

// Bootstrap initialises the cluster on the control-plane node over conn and returns once the
// node applied chalkos's add-ons and the objects of the Addon documents, or ctx ends. The node
// refuses when it is a worker, is an etcd member already or finds the cluster's API server
// answering. Only ctx bounds the call, not client.CallTimeout.
func Bootstrap(ctx context.Context, conn *client.Conn) (*kubernetesv1.BootstrapResponse, error) {
	return Client(conn).Bootstrap(client.Unbounded(ctx), &kubernetesv1.BootstrapRequest{})
}
