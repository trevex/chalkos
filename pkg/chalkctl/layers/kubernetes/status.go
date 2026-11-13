package kubernetes

import (
	"context"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	kubernetesview "github.com/trevex/chalkos/pkg/statusview/layers/kubernetes"
)

// PrintStatus prints the node's part in its cluster and the revoked nodes a control plane lists.
func (p *Plugin) PrintStatus(ctx context.Context, h layer.Host, conn *client.Conn) error {
	k, err := Status(ctx, conn)
	if err != nil {
		return err
	}
	return kubernetesview.Write(h.Stdout(), k)
}
