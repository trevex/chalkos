package main

import (
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/layers/kubernetes"
)

// layerTabs are the tabs of the layers compiled into the console, in the order of their names,
// which reach the layers' APIs over conn. A tab shows on images with its layer.
func layerTabs(conn *client.Conn) []console.Tab {
	return []console.Tab{
		kubernetes.New(kubernetesv1connect.NewKubernetesServiceClient(conn.HTTPClient(), conn.BaseURL())),
	}
}
