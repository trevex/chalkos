package kubernetes

import (
	"context"
	"errors"
	"slices"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/layers"
)

// Client is the client of the node's Kubernetes layer over conn.
func Client(conn *client.Conn) kubernetesv1connect.KubernetesServiceClient {
	return kubernetesv1connect.NewKubernetesServiceClient(conn.RPC())
}

// Status reads the state of the node's Kubernetes over conn.
func Status(ctx context.Context, conn *client.Conn) (*kubernetesv1.StatusResponse, error) {
	resp, err := Client(conn).Status(ctx, &kubernetesv1.StatusRequest{})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// errNoKubernetes is the error of a node whose image does not carry the Kubernetes layer.
var errNoKubernetes = errors.New("the node's image has no Kubernetes layer")

// nodeStatus reads the state of the node's Kubernetes over conn; errNoKubernetes when its Info
// names no Kubernetes layer.
func nodeStatus(ctx context.Context, conn *client.Conn) (*kubernetesv1.StatusResponse, error) {
	info, err := conn.Info(ctx, &nodev1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	if !slices.Contains(info.Layers, layers.Kubernetes) {
		return nil, errNoKubernetes
	}
	return Status(ctx, conn)
}

// statusFunc reads the state of a node's Kubernetes.
type statusFunc func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error)

// nodeStatusOf reads the state of an installed node's Kubernetes through call, as nodeStatus does.
func nodeStatusOf(call func(name string, fn func(conn *client.Conn) error) error) statusFunc {
	return func(ctx context.Context, name string) (st *kubernetesv1.StatusResponse, err error) {
		err = call(name, func(conn *client.Conn) error {
			st, err = nodeStatus(ctx, conn)
			return err
		})
		return st, err
	}
}
