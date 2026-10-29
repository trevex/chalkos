// Package layer is what chalkctl's layers plug in with. A layer is a Plugin compiled into
// chalkctl: it adds to chalkctl through the optional interfaces below, which chalkctl finds by
// type assertion, and reaches the cluster and its nodes through the Host chalkctl gives it.
// chalkctl asks every compiled plugin for the hooks the inventory and the secrets file drive, and
// only the plugins of the layers a node's Info names for what it reads from that node's layer
// services; each hook says which plugins chalkctl asks.
package layer

import (
	"context"
	"errors"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
)

// Plugin is a layer of chalkctl.
type Plugin interface {
	// Name is the layer, as a node's Info names it.
	Name() string
}

// Commands is a plugin that adds commands. chalkctl adds those of every compiled plugin.
type Commands interface {
	// Commands are the layer's commands by the group they join: "" for chalkctl itself, or a
	// group's name, such as "node".
	Commands(h Host) map[string][]*cobra.Command
}

// Status is a plugin that adds its section to a node's status. chalkctl asks the plugins of the
// layers the node's Info names.
type Status interface {
	// PrintStatus prints the layer's section of the status of the node conn reaches, whose image
	// carries the layer.
	PrintStatus(ctx context.Context, h Host, conn *client.Conn) error
}

// ImageFacts is a plugin whose layer's images carry facts config is translated with. chalkctl
// asks every compiled plugin.
type ImageFacts interface {
	// ContractFacts reads the facts of an image's contract: read takes a file name within the
	// image's directory, as its contract layer holds it. They are nil for an image without them.
	ContractFacts(read func(name string) ([]byte, error)) (*anypb.Any, error)
	// Target sets on t what the facts give config's translation; claimed is false for facts of
	// another layer.
	Target(facts *anypb.Any, t *config.Target) (claimed bool, err error)
}

// Delivery is a plugin whose layer takes payloads that chalkctl issues a node from the secrets
// file, and which admits a node to them. chalkctl asks every compiled plugin.
type Delivery interface {
	// Payloads are the layer's payloads for t, issued from its secrets at now; none for a node
	// the layer gives nothing.
	Payloads(t Target, now time.Time) ([]*anypb.Any, error)
	// ApplyFlags registers apply-config's flags that ask for the layer's payloads; chalkctl
	// registers --force, which delivers them without Admit.
	ApplyFlags(fs *pflag.FlagSet) ApplyFlags
	// Admit refuses to deliver t what the secrets file issues it, which does says.
	Admit(ctx context.Context, t Target, does string) error
	// AdmitInstall refuses to install t.
	AdmitInstall(ctx context.Context, t Target) error
}

// ApplyFlags are apply-config's flags of a layer, as Delivery registered them.
type ApplyFlags interface {
	// Check refuses flags that do not go together; staged is --staged.
	Check(staged bool) error
	// Flag is the flag that asks for the layer's payloads, such as --kubernetes-share.
	Flag() string
	// Requested reports whether the flags ask for the layer's payloads.
	Requested() bool
	// Does says what apply-config does to the node with them, for Admit.
	Does() string
	// None is the error of a node the layer gives no payloads.
	None(name string) error
}

// Upgrade is a plugin whose layer takes part in chalkctl upgrade. chalkctl asks, for a node, the
// plugins of the layers its Info names.
type Upgrade interface {
	// UpgradeRole reads the node's part in the layer's cluster from its service on conn.
	UpgradeRole(ctx context.Context, conn *client.Conn) (UpgradeRole, error)
	// NoCoordinator is the error of a run that must reach the layer's coordinators for the node,
	// a member, while the cluster has none to ask.
	NoCoordinator(name string) error
	// BeforeInstall runs before a node that reboots is sent its image.
	BeforeInstall(ctx context.Context, u Upgrading, name string, role UpgradeRole) error
	// BeforeReboot runs once the node has its image, before it reboots.
	BeforeReboot(ctx context.Context, u Upgrading, name string, role UpgradeRole) error
	// Ready checks, from the layer's service on conn, that the node serves its cluster again.
	Ready(ctx context.Context, conn *client.Conn, role UpgradeRole) error
	// After releases the node once it runs its image, found healthy.
	After(ctx context.Context, u Upgrading, name string, role UpgradeRole) error
}

// Coordinators is a plugin whose layer has nodes that act for the others, such as the nodes that
// drain the others. chalkctl asks every compiled plugin; upgrade and rotate take those nodes
// first.
type Coordinators interface {
	// ListedCoordinator reports whether the inventory lists the node as one that acts for the
	// others.
	ListedCoordinator(c Cluster, name string) (bool, error)
}

// ErrNoCoordinator is ViaCoordinators' error while the cluster has no coordinator to ask.
var ErrNoCoordinator = errors.New("the cluster has no coordinator to ask; pass --inventory")

// UpgradeRole is a node's part in a layer's cluster, as an upgrade treats it.
type UpgradeRole struct {
	// Coordinator is set for a node that acts for the others; coordinators upgrade one at a time,
	// before the others.
	Coordinator bool
	// Member is set for a node that serves the cluster, which an upgrade releases only once it
	// does again, and which stays held after it fell back.
	Member bool
}

// Upgrading is a run of chalkctl upgrade, as a plugin's hooks use it.
type Upgrading interface {
	// Say prints a line of the run's progress.
	Say(format string, args ...any)
	// ViaCoordinators calls fn on a coordinator: prefer first when it is one, then the others,
	// moving on past those whose error next accepts. It fails with ErrNoCoordinator while there
	// is none.
	ViaCoordinators(prefer string, next func(error) bool, fn func(conn *client.Conn) error) error
	// AllowDowntime is --allow-downtime.
	AllowDowntime() bool
	// DeleteEmptyDir is --delete-emptydir-data.
	DeleteEmptyDir() bool
	// DrainTimeout is how long a node's drain may take.
	DrainTimeout() time.Duration
	// QuorumWait is how long the run waits for a cluster to keep its quorum while a node reboots.
	QuorumWait() time.Duration
	// Poll is the time between two looks at what the run waits for.
	Poll() time.Duration
}

// Host is what chalkctl gives its plugins' commands.
type Host interface {
	// Stdout is the standard output of the command that runs.
	Stdout() io.Writer
	// Table writes aligned columns, separated by tabs, to standard output once flushed.
	Table() *tabwriter.Writer
	// Command makes cmd run run with the environment and streams of the command that runs.
	Command(cmd *cobra.Command, run func(h Host, ctx context.Context, args []string) error) *cobra.Command
	// NodeFlags registers the flags of a command that reaches one node: --inventory, the secrets
	// file's, --address and --config, as a client file may stand in for the secrets file.
	NodeFlags(fs *pflag.FlagSet) NodeFlags
	// SecretsFlags registers --inventory and the secrets file's flags.
	SecretsFlags(fs *pflag.FlagSet) SecretsFlags
	// Cluster loads the cluster the flags name.
	Cluster(ctx context.Context, f ClusterFlags) (Cluster, error)
	// Target addresses the node with the flags' credentials, at --address or else at the
	// inventory's address.
	Target(ctx context.Context, f NodeFlags, name string) (Target, error)
	// Nodes reaches the cluster's nodes with the flags' credentials: the node via at --address,
	// the others at the inventory's addresses.
	Nodes(ctx context.Context, f NodeFlags, via string) (Nodes, error)
	// Secrets reads the secrets file the flags name.
	Secrets(ctx context.Context, f SecretsFlags) (clustersecrets.Secrets, error)
}

// ClusterFlags are a command's flags that name the cluster, as Host registered them. A plugin
// holds them as a handle it passes back to Host, which reads them; the methods give the values of
// single flags, and tell the kinds of handle apart.
type ClusterFlags interface {
	// Inventory is --inventory.
	Inventory() string
}

// NodeFlags are a command's flags that reach a node, as Host registered them.
type NodeFlags interface {
	ClusterFlags
	// Address is --address.
	Address() string
}

// SecretsFlags are a command's flags that read the cluster and its secrets file, as Host
// registered them. Unlike NodeFlags, they always name a secrets file, which Host.Secrets reads.
type SecretsFlags interface {
	ClusterFlags
	// SecretsFile is --secrets.
	SecretsFile() string
}

// Cluster is what chalkctl knows of the cluster from its inventory.
type Cluster interface {
	// Name is the cluster's name in messages.
	Name() string
	// InventoryName is the inventory's metadata.name.
	InventoryName() string
	// Endpoint is the URL the Cluster document names; "" without one.
	Endpoint() string
	// Partial reports whether the cluster comes without an inventory.
	Partial() bool
	// RequireNodes refuses a partial cluster for the command, which needs the nodes' config.
	RequireNodes(command string) error
	// NodeNames are the inventory's nodes in name order; none on a partial cluster.
	NodeNames() []string
	// Documents are the node's config documents as the inventory gives them; none on a partial
	// cluster. They are the inventory's own, which callers read and never change.
	Documents(name string) ([]config.Document, error)
}

// Target is a node of the cluster with the credentials chalkctl reaches it with.
type Target interface {
	Name() string
	Cluster() Cluster
	// Dial connects to the installed node, verified by the OS CA and its name.
	Dial() (*client.Conn, error)
	// Call runs fn on a connection to the installed node; fn's error carries the node's name in
	// place of <node>.
	Call(fn func(conn *client.Conn) error) error
	// Secrets are those of the secrets file chalkctl reaches the node with; empty with a client
	// file.
	Secrets() clustersecrets.Secrets
	// Nodes reaches the cluster's nodes at the inventory's addresses with the target's
	// credentials.
	Nodes() Nodes
}

// Nodes reaches the nodes of a cluster with one set of credentials.
type Nodes interface {
	Cluster() Cluster
	// Call runs fn on a connection to the installed node; fn's error carries the node's name in
	// place of <node>.
	Call(name string, fn func(conn *client.Conn) error) error
	// ViaFirst calls fn on the first of names that answers, moving on past those whose error next
	// accepts; the error names the node that gave it.
	ViaFirst(names []string, next func(error) bool, fn func(conn *client.Conn) error) error
}
