package main

import (
	"context"
	"slices"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/console/layers/router"
	"github.com/trevex/chalkos/pkg/layers"
)

// layerTabs are the tabs of the layers compiled into the console, in the order of their names,
// which reach the layers' APIs over conn. A tab shows on images with its layer.
func layerTabs(conn *client.Conn) []console.Tab {
	return []console.Tab{
		kubernetes.New(kubernetesv1connect.NewKubernetesServiceClient(conn.RPC())),
		router.New(routerv1connect.NewRouterServiceClient(conn.RPC())),
	}
}

// networkBGP reads the Kubernetes layer's speaker. The router shows its sessions in its own tab.
func networkBGP(conn *client.Conn) func(context.Context, *nodev1.InfoResponse) (*nodev1.BGPStatus, error) {
	client := kubernetesv1connect.NewKubernetesServiceClient(conn.RPC())
	return func(ctx context.Context, info *nodev1.InfoResponse) (*nodev1.BGPStatus, error) {
		if !slices.Contains(info.GetLayers(), layers.Kubernetes) {
			return nil, nil
		}
		resp, err := client.BGPStatus(ctx, &kubernetesv1.BGPStatusRequest{})
		if err != nil {
			return nil, err
		}
		return resp.Bgp, nil
	}
}
