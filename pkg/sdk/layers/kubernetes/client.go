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

// Status reads the state of the node's Kubernetes over conn. A node whose image has no Kubernetes
// layer fails it with CodeUnimplemented; NodeStatus checks the node's Info first.
func Status(ctx context.Context, conn *client.Conn) (*kubernetesv1.StatusResponse, error) {
	resp, err := Client(conn).Status(ctx, &kubernetesv1.StatusRequest{})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ErrNoKubernetes is the error of a node whose image does not carry the Kubernetes layer.
var ErrNoKubernetes = errors.New("the node's image has no Kubernetes layer")

// NodeStatus reads the state of the node's Kubernetes over conn after it checked the node's Info:
// ErrNoKubernetes when the Info names no Kubernetes layer.
func NodeStatus(ctx context.Context, conn *client.Conn) (*kubernetesv1.StatusResponse, error) {
	info, err := conn.Info(ctx, &nodev1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	if !slices.Contains(info.Layers, layers.Kubernetes) {
		return nil, ErrNoKubernetes
	}
	return Status(ctx, conn)
}
