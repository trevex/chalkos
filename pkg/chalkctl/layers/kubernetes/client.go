package kubernetes

import (
	"context"
	"errors"
	"slices"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/layers"
)

// Client is the client of the node's Kubernetes layer over conn.
func Client(conn *client.Conn) kubernetesv1connect.KubernetesServiceClient {
	return kubernetesv1connect.NewKubernetesServiceClient(conn.HTTPClient(), conn.BaseURL())
}

// Status reads the state of the node's Kubernetes over conn.
func Status(ctx context.Context, conn *client.Conn) (*kubernetesv1.StatusResponse, error) {
	resp, err := Client(conn).Status(ctx, connect.NewRequest(&kubernetesv1.StatusRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// errNoKubernetes is the error of a node whose image does not carry the Kubernetes layer.
var errNoKubernetes = errors.New("the node's image has no Kubernetes layer")

// nodeStatus reads the state of the node's Kubernetes over conn; errNoKubernetes when its Info
// names no Kubernetes layer.
func nodeStatus(ctx context.Context, conn *client.Conn) (*kubernetesv1.StatusResponse, error) {
	info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		return nil, err
	}
	if !slices.Contains(info.Msg.Layers, layers.Kubernetes) {
		return nil, errNoKubernetes
	}
	st, err := Status(ctx, conn)
	return st, layer.ServiceError(err, layers.Kubernetes)
}

// nodeStatusOf reads the state of an installed node's Kubernetes through nodes, as nodeStatus does.
func nodeStatusOf(nodes layer.Nodes) func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error) {
	return func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error) {
		var st *kubernetesv1.StatusResponse
		err := nodes.Call(name, func(conn *client.Conn) error {
			var err error
			st, err = nodeStatus(ctx, conn)
			return err
		})
		return st, err
	}
}
