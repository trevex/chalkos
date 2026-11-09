package chalkctl

import (
	"context"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
)

// host is chalkctl's environment as its layer plugins use it.
type host struct{ a *app }

var _ layer.Host = host{}

func (h host) Stdout() io.Writer        { return h.a.stdout }
func (h host) Table() *tabwriter.Writer { return h.a.table() }

func (h host) Command(cmd *cobra.Command, run func(layer.Host, context.Context, []string) error) *cobra.Command {
	return h.a.command(cmd, func(a *app, ctx context.Context, args []string) error { return run(host{a}, ctx, args) })
}

func (h host) Cluster(ctx context.Context, f layer.ClusterFlags) (layer.Cluster, error) {
	c, err := loadCluster(ctx, f)
	if err != nil {
		return nil, err
	}
	return pluginCluster{c}, nil
}

// Target addresses the node with the secrets file or a client file.
func (h host) Target(ctx context.Context, f layer.NodeFlags, name string) (layer.Target, error) {
	t, err := h.a.clientTarget(ctx, nodeFlags{NodeFlags: f}, name)
	if err != nil {
		return nil, err
	}
	return pluginTarget{t}, nil
}

// Nodes reaches the nodes with the secrets file or a client file.
func (h host) Nodes(ctx context.Context, f layer.NodeFlags, via string) (layer.Nodes, error) {
	c, err := loadCluster(ctx, f.ClusterFlags)
	if err != nil {
		return nil, err
	}
	creds, err := h.a.loadCredentials(ctx, f.Secrets, f.Config)
	if err != nil {
		return nil, err
	}
	s := nodeSet{cluster: c, creds: creds}
	if via != "" {
		s.addresses = map[string]string{via: f.Address}
	}
	return pluginNodes{s}, nil
}

func (h host) Secrets(ctx context.Context, f layer.SecretsFlags) (clustersecrets.Secrets, error) {
	return h.a.loadSecrets(ctx, f)
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

func (c pluginCluster) Partial() bool                     { return c.c.partial() }
func (c pluginCluster) RequireNodes(command string) error { return c.c.requireNodes(command) }
func (c pluginCluster) NodeNames() []string               { return c.c.nodeNames() }

// Documents returns the documents the cluster holds for the node, not a copy: plugins only read
// them.
func (c pluginCluster) Documents(name string) ([]config.Document, error) {
	if c.c.partial() {
		return nil, nil
	}
	nc, err := c.c.config(name)
	if err != nil {
		return nil, err
	}
	return nc.Documents, nil
}

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

func (n pluginNodes) ViaFirst(names []string, next func(error) bool, fn func(conn *client.Conn) error) error {
	return n.s.viaFirst(names, next, fn)
}
