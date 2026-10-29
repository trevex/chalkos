package kubernetes

import (
	"context"
	"fmt"
	"time"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
)

// PrintStatus prints the node's part in its cluster and the revoked nodes a control plane lists.
func (p *Plugin) PrintStatus(ctx context.Context, h layer.Host, conn *client.Conn) error {
	k, err := Status(ctx, conn)
	if err != nil {
		return err
	}
	fmt.Fprintln(h.Stdout(), kubernetesLine(k))
	w := h.Table()
	for _, line := range revokedLines(k) {
		fmt.Fprintln(w, line)
	}
	return w.Flush()
}

// kubernetesLine is the status line of the node's Kubernetes side.
func kubernetesLine(k *kubernetesv1.StatusResponse) string {
	line := fmt.Sprintf("kubernetes %s: %s", k.Kind, k.State)
	if k.NodeReady != "" {
		line += ", node ready: " + k.NodeReady
	}
	if k.Vip != "" {
		line += ", vip " + k.Vip
	}
	if k.ControlPlane != "" {
		line += ", control plane " + k.ControlPlane
	}
	return line
}

// revokedLines are the status lines of the nodes a control plane lists as revoked, or of why
// it could not read them.
func revokedLines(k *kubernetesv1.StatusResponse) []string {
	if k.RevokedNodesError != "" {
		return []string{"revoked nodes unknown: " + k.RevokedNodesError}
	}
	if len(k.RevokedNodes) == 0 {
		return nil
	}
	lines := []string{"revoked nodes:"}
	for _, n := range k.RevokedNodes {
		lines = append(lines, "  "+n.Name+"\t"+revokedSince(n))
	}
	return lines
}

// revokedSince says when a node was revoked.
func revokedSince(n *kubernetesv1.RevokedNode) string {
	if n.RevokedAt == nil {
		return "at an unknown time"
	}
	return "since " + n.RevokedAt.AsTime().UTC().Format(time.RFC3339)
}
