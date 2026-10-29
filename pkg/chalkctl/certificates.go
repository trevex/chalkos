package chalkctl

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/pki"
)

type nodeRenewFlags struct {
	node  nodeFlags
	force bool
}

// nodeRenewCommand issues a node a new node certificate from the node CA and delivers it. The node is
// reached even when its certificate expired: its chain is verified without dates, which only
// this command does. The node still verifies chalkctl's certificate as usual.
func (a *app) nodeRenewCommand() *cobra.Command {
	// The certificate names the node's addresses, which only its config gives.
	f := nodeRenewFlags{node: nodeFlags{needs: "node renew"}}
	cmd := a.command(&cobra.Command{
		Use:     "renew <node>",
		Short:   "Issue a node a new node certificate, also once its own expired",
		Long:    nodeRenewLong,
		Example: nodeRenewExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeRenew(ctx, f, pos) })
	f.node.register(cmd.Flags())
	cmd.Flags().BoolVar(&f.force, "force", false, "renew without asking the control planes whether the node is revoked")
	return cmd
}

func (a *app) nodeRenew(ctx context.Context, f nodeRenewFlags, pos []string) error {
	t, err := a.target(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	if !f.force {
		for _, d := range compiled[layer.Delivery]() {
			if err := d.Admit(ctx, pluginTarget{t}, "node renew issues it a node certificate"); err != nil {
				return err
			}
		}
	}
	names, err := t.cluster.certNames(t.name)
	if err != nil {
		return err
	}
	conn, err := dialNode(t, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	cert, err := pki.IssueNode(t.secrets.NodeCA, names, time.Now())
	if err != nil {
		return err
	}
	resp, err := conn.ApplyConfig(ctx, connect.NewRequest(&nodev1.ApplyConfigRequest{
		NodeCertificate: []byte(cert.Certificate),
		NodeKey:         []byte(cert.Key),
	}))
	if err != nil {
		return fmt.Errorf("deliver the node certificate of %s: %w", t.name, nodeError(err, t.name))
	}
	a.warnDelivery(t.name, resp.Msg)
	leaf, err := pki.ParseCertificate([]byte(cert.Certificate))
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "%s serves a new node certificate; it expires %s\n", t.name, leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}
