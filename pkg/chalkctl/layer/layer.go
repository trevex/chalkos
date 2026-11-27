// Package layer defines how chalkctl's layers plug in. A Plugin is compiled into chalkctl, adds to
// it through the optional interfaces below, which chalkctl finds by type assertion, and reaches
// nodes through Host. Each interface says whether chalkctl asks every compiled plugin or only the
// layers a node's Info names. A plugin that is also an sdk.Layer adds the SDK's hooks too:
// image facts, linked validators, the filter of a node's documents and the payloads of a
// delivery. chalkctl passes the SDK every compiled plugin.
package layer

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/sdk"
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
	// Title titles the group of the layer's commands at chalkctl's root in its help, such as
	// Kubernetes.
	Title() string
}

// Status is a plugin that adds its section to a node's status. chalkctl asks the plugins of the
// layers the node's Info names.
type Status interface {
	// ReadStatus reads the layer's part of the status of the node conn reaches, whose image carries
	// the layer.
	ReadStatus(ctx context.Context, conn *client.Conn) (StatusSection, error)
}

// StatusSection is a layer's part of a node's status.
type StatusSection struct {
	// Message is the layer's status as its service answers it, which status -o json prints under
	// the layer's name.
	Message proto.Message
	// Write writes the section as chalkctl status prints it.
	Write func(w io.Writer) error
	// Problems are what the section shows wrong, each in a few words, which the summary of the
	// node's status counts.
	Problems []string
}

// Inventory is a plugin whose layer checks an inventory's nodes together. chalkctl asks every
// compiled plugin.
type Inventory interface {
	// CheckInventory refuses the cluster's nodes when their documents do not go together, such as
	// nodes of one cluster that describe it differently. chalkctl checks every inventory it loads.
	CheckInventory(c Cluster) error
}

// InventoryWarnings is a plugin whose layer warns about an inventory's valid config. chalkctl
// validate asks every compiled plugin.
type InventoryWarnings interface {
	// InventoryWarnings are what the documents of the cluster's nodes together warrant a warning
	// for, such as a setting whose default leaves the cluster open.
	InventoryWarnings(c Cluster) []string
}

// Delivery is a plugin whose layer takes payloads that chalkctl issues a node from the secrets
// file, and which admits a node to them. chalkctl asks every compiled plugin.
type Delivery interface {
	// Payloads issues the layer's payloads.
	sdk.Payloads
	// ApplyFlags registers config apply's flags that ask for the layer's payloads; chalkctl
	// registers --ignore-revoked, which delivers them without Admit.
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
	// Held is how the layer holds a member, completing "it stays", such as cordoned; empty says it
	// stays held by its layers.
	Held string
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
	// Stderr is the standard error of the command that runs.
	Stderr() io.Writer
	// Table writes aligned columns, separated by tabs, to standard output once flushed.
	Table() *tabwriter.Writer
	// Command makes cmd run run with the environment and streams of the command that runs.
	Command(cmd *cobra.Command, run func(h Host, ctx context.Context, args []string) error) *cobra.Command
	// Confirm asks on the terminal before a command that declares Meta.Confirms does what, a
	// clause such as "registry delete deletes the tags 1.0 and latest with the digest", and fails
	// unless the answer agrees: y, or typed when it is not empty, such as a node's name. With --yes
	// it goes ahead without asking; without a terminal it refuses and names --yes.
	Confirm(ctx context.Context, what, typed string) error
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
	// ClientTLS is the TLS config of the flags' credentials, the client file or the secrets file,
	// for a service of the nodes other than chalkd: their client certificate, and the OS CAs as
	// the roots it verifies the nodes' certificates by. role is the role the credentials grant.
	ClientTLS(ctx context.Context, f NodeFlags) (config *tls.Config, role pki.Role, err error)
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
	// compiled layer's sdk.Documents filter; none on a partial cluster. They are the inventory's
	// own, which callers read and never change.
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

// Fetch returns a callback for Call and its kin that runs rpc and keeps its response's message in
// *msg.
func Fetch[T any](msg **T, rpc func(conn *client.Conn) (*T, error)) func(conn *client.Conn) error {
	return func(conn *client.Conn) error {
		res, err := rpc(conn)
		if err == nil {
			*msg = res
		}
		return err
	}
}
