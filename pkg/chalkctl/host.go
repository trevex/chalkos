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
	return c, nil
}

// Target addresses the node with the secrets file or a client file.
func (h host) Target(ctx context.Context, f layer.NodeFlags, name string) (layer.Target, error) {
	t, err := h.a.clientTarget(ctx, nodeFlags{NodeFlags: f}, name)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// Nodes reaches the nodes with the secrets file or a client file.
func (h host) Nodes(ctx context.Context, f layer.NodeFlags, via string) (layer.Nodes, error) {
	return h.a.reach(ctx, nodeFlags{NodeFlags: f}, via, true)
}

func (h host) Secrets(ctx context.Context, f layer.SecretsFlags) (clustersecrets.Secrets, error) {
	return h.a.loadSecrets(ctx, f)
}

// The methods below make a cluster, a target and a nodeSet what plugins see of them.
var (
	_ layer.Cluster = (*cluster)(nil)
	_ layer.Target  = (*target)(nil)
	_ layer.Nodes   = nodeSet{}
)

func (c *cluster) Name() string { return c.name }

func (c *cluster) InventoryName() string {
	if c.Partial() {
		return ""
	}
	return c.inv.Name
}

// Documents returns the documents the cluster holds for the node, not a copy: plugins only read
// them.
func (c *cluster) Documents(name string) ([]config.Document, error) {
	if c.Partial() {
		return nil, nil
	}
	nc, err := c.config(name)
	if err != nil {
		return nil, err
	}
	return nc.Documents, nil
}

func (t *target) Name() string                                { return t.name }
func (t *target) Cluster() layer.Cluster                      { return t.cluster }
func (t *target) Dial() (*client.Conn, error)                 { return dialInstalled(t) }
func (t *target) Call(fn func(conn *client.Conn) error) error { return callNode(t, fn) }
func (t *target) Secrets() clustersecrets.Secrets             { return t.secrets }
func (t *target) Nodes() layer.Nodes                          { return nodeSet{cluster: t.cluster, creds: t.creds} }

func (s nodeSet) Cluster() layer.Cluster { return s.cluster }
