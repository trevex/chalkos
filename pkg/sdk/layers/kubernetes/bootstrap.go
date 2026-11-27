package kubernetes

import (
	"context"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
)

// Bootstrap initialises the cluster on the control-plane node over conn and returns once the
// node applied chalkos's add-ons and the objects of the Addon documents, or ctx ends. The node
// refuses when it is a worker, is an etcd member already or finds the cluster's API server
// answering. Only ctx bounds the call, not client.CallTimeout.
func Bootstrap(ctx context.Context, conn *client.Conn) (*kubernetesv1.BootstrapResponse, error) {
	return Client(conn).Bootstrap(client.Unbounded(ctx), &kubernetesv1.BootstrapRequest{})
}

// BootstrappedAlready reports whether err is a node's refusal of Bootstrap because it is an etcd
// member of a cluster already, by the reason BOOTSTRAPPED_ALREADY: it bootstrapped the cluster or
// joined it. A caller that bootstraps the first control plane once treats it as done.
func BootstrappedAlready(err error) bool {
	return client.Reason(err) == nodev1.ErrorReason_ERROR_REASON_BOOTSTRAPPED_ALREADY
}
