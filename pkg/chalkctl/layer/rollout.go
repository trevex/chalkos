package layer

import (
	"context"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/client"
)

// Rollout is a plugin whose layer's cluster bounds how far apart the versions its nodes run may
// be, as Kubernetes's version skew policy bounds the API server, the kubelets and the other
// components. chalkctl apply-config and chalkctl upgrade read the nodes' versions through it and
// have it refuse what would break the bounds. When a delivery of config changes the versions it
// bounds, apply-config orders the delivery: the coordinators first, one at a time, each held by
// BeforeDelivery and delivered on trial, which chalkctl confirms only once Delivered passes; then
// the other nodes. chalkctl asks, for a node, the plugins of the layers its Info names; for a node
// it cannot reach, and for RolloutFlags, every compiled plugin.
type Rollout interface {
	// RolloutFlags registers the layer's flags of apply-config on fs and returns where their values
	// go, which Upgrading.Flags gives the layer's hooks during a delivery.
	RolloutFlags(fs *pflag.FlagSet) any
	// Versions reads, from the layer's service on conn, the versions the node runs that the bounds
	// compare. chalkctl passes them back to the layer's other hooks unread.
	Versions(ctx context.Context, conn *client.Conn) (any, error)
	// Changes reports whether delivering the inventory's config to the nodes changes the versions
	// the bounds compare, against what each of them runs. apply-config then reads every node's
	// versions, has CheckConfig check them, and orders the delivery.
	Changes(c Cluster, nodes []NodeVersions) (bool, error)
	// CheckConfig refuses to deliver the inventory's config to the nodes named, which changes the
	// versions the bounds compare, when it would break them against what the cluster's nodes run,
	// as all reports it, or deliver it in an order the bounds do not allow.
	CheckConfig(u Upgrading, c Cluster, named []string, all []NodeVersions) error
	// CheckImages refuses to install on each node of images the image whose layer facts it gives,
	// when the versions the facts name would break the bounds against what the cluster's nodes
	// run, as all reports it.
	CheckImages(c Cluster, images map[string][]*anypb.Any, all []NodeVersions) error
	// BeforeDelivery holds a coordinator before an ordered delivery changes what it runs, as
	// Kubernetes waits for etcd to keep its quorum.
	BeforeDelivery(ctx context.Context, u Upgrading, name string, role UpgradeRole) error
	// Delivered checks, from the layer's service on conn, that the node serves the layer's cluster
	// with the config it runs, as a control plane's components run the images its config names.
	// chalkctl confirms a config's trial only once it passes, and asks again until then.
	Delivered(ctx context.Context, conn *client.Conn) error
	// Summary says, from all, which nodes run which versions, as a delivery that stopped prints it.
	Summary(all []NodeVersions) []string
}

// NodeVersions are the versions a node runs, as a Rollout layer's Versions read them.
type NodeVersions struct {
	Name string
	// Versions are what Versions returned; nil with Err.
	Versions any
	// Err is why chalkctl could not read them, as from a node it cannot reach.
	Err error
}
