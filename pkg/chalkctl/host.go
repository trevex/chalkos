package chalkctl

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
)

// host is chalkctl's environment as its layer plugins use it.
type host struct{ a *app }

var _ layer.Host = host{}

func (h host) Stdout() io.Writer        { return h.a.stdout }
func (h host) Table() *tabwriter.Writer { return h.a.table() }

func (h host) Command(cmd *cobra.Command, run func(layer.Host, context.Context, []string) error) *cobra.Command {
	return h.a.command(cmd, func(a *app, ctx context.Context, args []string) error { return run(host{a}, ctx, args) })
}

// pluginNodeFlags are nodeFlags as a plugin holds them.
type pluginNodeFlags struct{ f *nodeFlags }

func (f pluginNodeFlags) Inventory() string { return f.f.cluster.inventory }
func (f pluginNodeFlags) Address() string   { return f.f.address }

// NodeFlags registers the flags of a command that a client file may run.
func (h host) NodeFlags(fs *pflag.FlagSet) layer.NodeFlags {
	f := &nodeFlags{}
	f.registerClient(fs)
	return pluginNodeFlags{f}
}

// pluginSecretsFlags are the cluster's and the secrets file's flags as a plugin holds them.
type pluginSecretsFlags struct {
	cluster *clusterFlags
	secrets *secretFlags
}

func (f pluginSecretsFlags) Inventory() string   { return f.cluster.inventory }
func (f pluginSecretsFlags) SecretsFile() string { return f.secrets.path }

func (h host) SecretsFlags(fs *pflag.FlagSet) layer.SecretsFlags {
	f := pluginSecretsFlags{&clusterFlags{}, &secretFlags{}}
	f.cluster.register(fs)
	f.secrets.register(fs)
	return f
}

func (h host) Cluster(ctx context.Context, f layer.ClusterFlags) (layer.Cluster, error) {
	var cf clusterFlags
	switch f := f.(type) {
	case pluginNodeFlags:
		cf = f.f.cluster
	case pluginSecretsFlags:
		cf = *f.cluster
	default:
		return nil, fmt.Errorf("flags %T were not registered by chalkctl", f)
	}
	c, err := loadCluster(ctx, cf)
	if err != nil {
		return nil, err
	}
	return pluginCluster{c}, nil
}

// Target addresses the node with the secrets file or a client file.
func (h host) Target(ctx context.Context, f layer.NodeFlags, name string) (layer.Target, error) {
	n, err := nodeFlagsOf(f)
	if err != nil {
		return nil, err
	}
	t, err := h.a.clientTarget(ctx, *n, name)
	if err != nil {
		return nil, err
	}
	return pluginTarget{t}, nil
}

// Nodes reaches the nodes with the secrets file or a client file.
func (h host) Nodes(ctx context.Context, f layer.NodeFlags, via string) (layer.Nodes, error) {
	n, err := nodeFlagsOf(f)
	if err != nil {
		return nil, err
	}
	c, err := loadCluster(ctx, n.cluster)
	if err != nil {
		return nil, err
	}
	creds, err := h.a.loadCredentials(ctx, n.secrets, n.config)
	if err != nil {
		return nil, err
	}
	s := nodeSet{cluster: c, creds: creds}
	if via != "" {
		s.addresses = map[string]string{via: n.address}
	}
	return pluginNodes{s}, nil
}

func (h host) Secrets(ctx context.Context, f layer.SecretsFlags) (clustersecrets.Secrets, error) {
	s, ok := f.(pluginSecretsFlags)
	if !ok {
		return clustersecrets.Secrets{}, fmt.Errorf("flags %T were not registered by chalkctl", f)
	}
	return h.a.loadSecrets(ctx, *s.secrets)
}

// nodeFlagsOf are the nodeFlags behind flags that Host registered.
func nodeFlagsOf(f layer.NodeFlags) (*nodeFlags, error) {
	n, ok := f.(pluginNodeFlags)
	if !ok {
		return nil, fmt.Errorf("flags %T were not registered by chalkctl", f)
	}
	return n.f, nil
}

// pluginCluster is a cluster as a plugin sees it.
type pluginCluster struct{ c *cluster }

func (c pluginCluster) Name() string { return c.c.name }

func (c pluginCluster) InventoryName() string {
	if c.c.partial() {
		return ""
	}
	return c.c.inv.Name
}

func (c pluginCluster) Endpoint() string                            { return c.c.endpoint }
func (c pluginCluster) Partial() bool                               { return c.c.partial() }
func (c pluginCluster) RequireNodes(command string) error           { return c.c.requireNodes(command) }
func (c pluginCluster) Kind(name string) (string, error)            { return c.c.kind(name) }
func (c pluginCluster) NodesOf(kind, skip string) ([]string, error) { return c.c.nodesOf(kind, skip) }

// pluginTarget is a target as a plugin sees it.
type pluginTarget struct{ t *target }

func (t pluginTarget) Name() string                                { return t.t.name }
func (t pluginTarget) Cluster() layer.Cluster                      { return pluginCluster{t.t.cluster} }
func (t pluginTarget) Dial() (*client.Conn, error)                 { return dialInstalled(t.t) }
func (t pluginTarget) Call(fn func(conn *client.Conn) error) error { return callNode(t.t, fn) }
func (t pluginTarget) Secrets() clustersecrets.Secrets             { return t.t.secrets }

func (t pluginTarget) Nodes() layer.Nodes {
	return pluginNodes{nodeSet{cluster: t.t.cluster, creds: t.t.creds}}
}

// pluginNodes is a nodeSet as a plugin sees it.
type pluginNodes struct{ s nodeSet }

func (n pluginNodes) Cluster() layer.Cluster { return pluginCluster{n.s.cluster} }

func (n pluginNodes) Call(name string, fn func(conn *client.Conn) error) error {
	return n.s.call(name, fn)
}

func (n pluginNodes) ViaControlPlanes(names []string, next func(error) bool, fn func(conn *client.Conn) error) error {
	return n.s.viaControlPlanes(names, next, fn)
}
