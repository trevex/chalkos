// Package kubernetes is chalkctl's Kubernetes layer: the commands that run the cluster's
// Kubernetes and its etcd and that revoke nodes by name, the Kubernetes section of a node's
// status, the facts of its images that config is translated with, the share chalkctl delivers a
// node once no control plane lists it as revoked, an upgrade's checks of etcd's quorum, its
// drains and its uncordons, and the rotations of the Kubernetes CAs, the service-account key and
// the encryption key.
//
// It also holds the cluster to Kubernetes's version skew policy. A delivery of config that changes
// the API server's version is checked against the API servers and kubelets every node runs, and
// goes to the control planes first, one at a time: each after etcd's quorum check, on trial, and
// confirmed once its static pods run the new images and are Ready. chalkctl upgrade refuses an
// image whose kubelet would be newer than the oldest API server the control planes run.
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
)

// New returns chalkctl's Kubernetes layer.
func New() *Plugin { return &Plugin{} }

// Name is the Kubernetes layer's.
func (*Plugin) Name() string { return layers.Kubernetes }

// Commands are bootstrap, kubeconfig and etcd, and node revoke and node unrevoke.
func (*Plugin) Commands(h layer.Host) map[string][]*cobra.Command {
	return map[string][]*cobra.Command{
		"":     {bootstrapCommand(h), kubeconfigCommand(h), etcdCommand(h)},
		"node": {nodeRevokeCommand(h), nodeUnrevokeCommand(h)},
	}
}
