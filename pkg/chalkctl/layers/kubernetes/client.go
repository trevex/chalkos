package kubernetes

import (
	"context"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/client"
	sdkkubernetes "github.com/trevex/chalkos/pkg/sdk/layers/kubernetes"
)

// statusFunc reads the state of a node's Kubernetes.
type statusFunc func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error)

// nodeStatusOf reads the state of an installed node's Kubernetes through call, as
// sdkkubernetes.NodeStatus does.
func nodeStatusOf(call func(name string, fn func(conn *client.Conn) error) error) statusFunc {
	return func(ctx context.Context, name string) (st *kubernetesv1.StatusResponse, err error) {
		err = call(name, func(conn *client.Conn) error {
			st, err = sdkkubernetes.NodeStatus(ctx, conn)
			return err
		})
		return st, err
	}
}
