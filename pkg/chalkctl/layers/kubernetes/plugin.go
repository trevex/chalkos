// Package kubernetes is chalkctl's Kubernetes layer: the bootstrap, kubeconfig, etcd and node
// revocation commands, and the Kubernetes hooks of status, inventory checks, delivery, upgrade,
// rollout and rotation. Its Rollout hooks hold the cluster to Kubernetes's version skew policy.
package kubernetes

import (
	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/layers"
)

// Plugin is chalkctl's Kubernetes layer.
type Plugin struct{}

var (
	_ layer.Plugin        = (*Plugin)(nil)
	_ layer.Commands      = (*Plugin)(nil)
	_ layer.Status        = (*Plugin)(nil)
	_ layer.ImageFacts    = (*Plugin)(nil)
	_ layer.Delivery      = (*Plugin)(nil)
	_ layer.Upgrade       = (*Plugin)(nil)
	_ layer.UpgradeFlags  = (*Plugin)(nil)
	_ layer.Coordinators  = (*Plugin)(nil)
	_ layer.Rotation      = (*Plugin)(nil)
	_ layer.RotationNodes = (*Plugin)(nil)
	_ layer.Rollout       = (*Plugin)(nil)
	_ layer.Inventory     = (*Plugin)(nil)
)

// Name is the Kubernetes layer's.
func (*Plugin) Name() string { return layers.Kubernetes }

// Commands are bootstrap, kubeconfig and etcd, and node revoke and node unrevoke.
func (*Plugin) Commands(h layer.Host) map[string][]*cobra.Command {
	return map[string][]*cobra.Command{
		"":     {bootstrapCommand(h), kubeconfigCommand(h), etcdCommand(h)},
		"node": {nodeRevokeCommand(h), nodeUnrevokeCommand(h)},
	}
}
