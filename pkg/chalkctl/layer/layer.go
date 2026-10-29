// Package layer is what chalkctl's layers plug in with. A layer is a Plugin compiled into
// chalkctl: it adds to chalkctl through the optional interfaces below, which chalkctl finds by
// type assertion, and reaches the cluster and its nodes through the Host chalkctl gives it.
package layer

import (
	"context"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
)

// Plugin is a layer of chalkctl.
type Plugin interface {
	// Name is the layer, as a node's Info names it.
	Name() string
}

// Commands is a plugin that adds commands.
type Commands interface {
	// Commands are the layer's commands by the group they join: "" for chalkctl itself, or a
	// group's name, such as "node".
	Commands(h Host) map[string][]*cobra.Command
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

// ClusterFlags are a command's flags that name the cluster, as Host registered them.
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
// registered them.
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
	// Kind is the type the node's Node document names; "" without one and on a partial cluster.
	Kind(name string) (string, error)
	// NodesOf lists the nodes of a Node type but skip, in name order; "" lists those without one.
	NodesOf(kind, skip string) ([]string, error)
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
}

// Nodes reaches the nodes of a cluster with one set of credentials.
type Nodes interface {
	Cluster() Cluster
	// Call runs fn on a connection to the installed node; fn's error carries the node's name in
	// place of <node>.
	Call(name string, fn func(conn *client.Conn) error) error
	// ViaControlPlanes calls fn on the first of names that answers, moving on past those whose
	// error next accepts; the error names the node that gave it.
	ViaControlPlanes(names []string, next func(error) bool, fn func(conn *client.Conn) error) error
}
