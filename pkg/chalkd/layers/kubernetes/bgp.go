package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"
	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	frrstatus "github.com/trevex/chalkos/pkg/frr/status"
	"github.com/trevex/chalkos/pkg/tool"
)

func (p *Plugin) BGPStatus(ctx context.Context, _ *connect.Request[kubernetesv1.BGPStatusRequest]) (*connect.Response[kubernetesv1.BGPStatusResponse], error) {
	bgp := localBGPStatus(ctx, p.node.Runner(), "/etc/frr/frr.conf", "/run/chalkos/config/kubernetes.chalkos.dev/BGPSpeaker")
	return connect.NewResponse(&kubernetesv1.BGPStatusResponse{Bgp: bgp}), nil
}

// localBGPStatus is absent without FRR or a running speaker document. It reads neither the
// control plane nor frr.conf, which may contain peer passwords.
func localBGPStatus(ctx context.Context, run tool.Runner, frrConfig, speakers string) *nodev1.BGPStatus {
	if _, err := os.Lstat(frrConfig); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return &nodev1.BGPStatus{Error: fmt.Sprintf("find FRR's config link: %v", err)}
	}
	docs, err := os.ReadDir(speakers)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &nodev1.BGPStatus{Error: fmt.Sprintf("read BGPSpeaker documents: %v", err)}
	}
	for _, doc := range docs {
		if doc.Type().IsRegular() && strings.HasSuffix(doc.Name(), ".json") {
			peers, err := frrstatus.ReadPeers(ctx, run)
			if err != nil {
				return &nodev1.BGPStatus{Error: err.Error()}
			}
			return &nodev1.BGPStatus{Peers: peers}
		}
	}
	return nil
}
