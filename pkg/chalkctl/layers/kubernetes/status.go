package kubernetes

import (
	"context"
	"io"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	sdkkubernetes "github.com/trevex/chalkos/pkg/sdk/layers/kubernetes"
	kubernetesview "github.com/trevex/chalkos/pkg/statusview/layers/kubernetes"
)

// ReadStatus reads the node's part in its cluster and the revoked nodes a control plane lists.
func (p *Plugin) ReadStatus(ctx context.Context, conn *client.Conn) (layer.StatusSection, error) {
	k, err := sdkkubernetes.Status(ctx, conn)
	if err != nil {
		return layer.StatusSection{}, err
	}
	return layer.StatusSection{
		Message:  k,
		Write:    func(w io.Writer) error { return kubernetesview.Write(w, k) },
		Problems: kubernetesview.Problems(k),
	}, nil
}
