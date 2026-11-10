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
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/schema"
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

// Validators is a plugin whose layer links validators of its kinds: the Go code its images run as
// validator programs, which chalkctl runs in-process on a node's config wherever it checks the
// config against the node's image, as validate and config apply do. The image runs its own
// programs again, which decide; chalkctl's copies refuse a config before it reaches the node.
// chalkctl asks every compiled plugin.
type Validators interface {
	// Validators are the layer's validators that an image of the schemas runs, by the names the
	// image gives them; none for an image without the layer's kinds.
	Validators(s *schema.Schemas) []outputs.LinkedValidator
}

// ImageFacts is a plugin whose layer's images carry facts config is translated with. chalkctl
// asks every compiled plugin.
type ImageFacts interface {
	// ContractFacts reads the facts of an image's contract: read takes a file name within the
	// image's directory, as its contract layer holds it. They are nil for an image without them.
	ContractFacts(read func(name string) ([]byte, error)) (*anypb.Any, error)
	// Target adds to t's translators the layer's translation of an image with the facts; claimed
	// is false for facts of another layer.
	Target(facts *anypb.Any, t *config.Target) (claimed bool, err error)
}

// Inventory is a plugin whose layer filters each node's documents and checks an inventory's nodes
// together. chalkctl asks every compiled plugin.
type Inventory interface {
	// NodeDocuments are the documents of a node's files that the node gets, as an
	// inventory.Filter: docs or a new slice, with no document changed.
	NodeDocuments(docs []config.Document) []config.Document
	// CheckInventory refuses the cluster's nodes when their documents do not go together, such as
	// nodes of one cluster that describe it differently. chalkctl checks every inventory it loads.
	CheckInventory(c Cluster) error
}

// Delivery is a plugin whose layer takes payloads that chalkctl issues a node from the secrets
// file, and which admits a node to them. chalkctl asks every compiled plugin.
type Delivery interface {
	// Payloads are the layer's payloads for t, issued from its secrets at now; none for a node
	// the layer gives nothing.
	Payloads(t Target, now time.Time) ([]*anypb.Any, error)
	// ApplyFlags registers config apply's flags that ask for the layer's payloads; chalkctl
	// registers --force, which delivers them without Admit.
	ApplyFlags(fs *pflag.FlagSet) ApplyFlags
	// Admit refuses to deliver t what the secrets file issues it, which does says.
	Admit(ctx context.Context, t Target, does string) error
	// AdmitInstall refuses to install t.
	AdmitInstall(ctx context.Context, t Target) error
}

// ApplyFlags are config apply's flags of a layer, as Delivery registered them.
type ApplyFlags interface {
	// Check refuses flags that do not go together; staged is --staged.
	Check(staged bool) error
	// Flag is the flag that asks for the layer's payloads, such as --kubernetes-share.
	Flag() string
	// Requested reports whether the flags ask for the layer's payloads.
	Requested() bool
	// Does says what config apply does to the node with them, for Admit.
	Does() string
	// None is the error of a node the layer gives no payloads.
	None(name string) error
}

// Upgrade is a plugin whose layer takes part in the reboot of a node that chalkctl upgrade and
// chalkctl node reboot share. chalkctl asks, for a node, the plugins of the layers its Info names.
type Upgrade interface {
	// UpgradeRole reads the node's part in the layer's cluster from its service on conn.
	UpgradeRole(ctx context.Context, conn *client.Conn) (UpgradeRole, error)
	// NoCoordinator is the error of a run that must reach the layer's coordinators for the node,
	// a member, while the cluster has none to ask.
	NoCoordinator(name string) error
	// BeforeReboot holds the node before it reboots, as into the image an upgrade activated.
	BeforeReboot(ctx context.Context, u Upgrading, name string, role UpgradeRole) error
	// Ready checks, from the layer's service on conn, that the node serves its cluster again.
	Ready(ctx context.Context, conn *client.Conn, role UpgradeRole) error
	// After releases the node once it runs its image, found healthy.
	After(ctx context.Context, u Upgrading, name string, role UpgradeRole) error
}

// UpgradeFlags is a plugin that adds flags to chalkctl upgrade and chalkctl node reboot, which its
// Upgrade hooks read. chalkctl asks every compiled plugin.
type UpgradeFlags interface {
	// UpgradeFlags registers the layer's flags of the command on fs and returns where their values
	// go, which Upgrading.Flags gives the layer's hooks during a run.
	UpgradeFlags(fs *pflag.FlagSet) any
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

// Upgrading is a run of chalkctl upgrade or chalkctl node reboot, as a plugin's hooks use it.
type Upgrading interface {
	// Say prints a line of the run's progress.
	Say(format string, args ...any)
	// ViaCoordinators calls fn on a coordinator: prefer first when it is one, then the others,
	// moving on past those whose error next accepts. It fails with ErrNoCoordinator while there
	// is none.
	ViaCoordinators(prefer string, next func(error) bool, fn func(conn *client.Conn) error) error
	// Flags is what the layer's UpgradeFlags returned for the run's command, by the layer's name;
	// nil for a layer without flags.
	Flags(layer string) any
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
	// Cluster loads the cluster the flags name.
	Cluster(ctx context.Context, f ClusterFlags) (Cluster, error)
	// Target addresses the node with the flags' credentials: the client file or the secrets file,
	// at --address or else at the inventory's address.
	Target(ctx context.Context, f NodeFlags, name string) (Target, error)
	// Nodes reaches the cluster's nodes with the flags' credentials, each at the address --address
	// gives for it or else at the inventory's. A bare --address ADDR is via's; without via, Nodes
	// refuses one.
	Nodes(ctx context.Context, f NodeFlags, via string) (Nodes, error)
	// Secrets reads the secrets file the flags name.
	Secrets(ctx context.Context, f SecretsFlags) (clustersecrets.Secrets, error)
}

// Cluster is what chalkctl knows of the cluster from its inventory.
type Cluster interface {
	// Name is the cluster's name in messages.
	Name() string
	// InventoryName is the inventory's metadata.name.
	InventoryName() string
	// Partial reports whether the cluster comes without an inventory.
	Partial() bool
	// RequireNodes refuses a partial cluster for the command, which needs the nodes' config.
	RequireNodes(command string) error
	// NodeNames are the inventory's nodes in name order; none on a partial cluster.
	NodeNames() []string
	// Documents are the node's config documents as the inventory gives them, after every
	// Inventory plugin's NodeDocuments; none on a partial cluster. They are the inventory's own,
	// which callers read and never change.
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
	// Nodes reaches the cluster's nodes with the target's credentials, each at the address
	// --address gave for it or else at the inventory's.
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
