package chalkctl

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/statusview"
)

func (a *app) disksCommand() *cobra.Command {
	var f nodePinFlags
	cmd := a.command(&cobra.Command{
		Use:     "disks [<node>]",
		Short:   "List a node's disks",
		Long:    disksLong,
		Example: disksExample,
		Args:    cli.RangeArgs(0, 1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.disks(ctx, f, pos) })
	f.node.RegisterClient(cmd.Flags())
	f.pin.register(cmd.Flags())
	return cmd
}

func (a *app) disks(ctx context.Context, f nodePinFlags, pos []string) error {
	n, p := f.node, f.pin
	var conn *client.Conn
	var err error
	switch {
	case len(pos) == 1:
		t, err := a.clientTarget(ctx, n, pos[0])
		if err != nil {
			return err
		}
		if conn, err = a.dialEither(t, p); err != nil {
			return err
		}
	case len(pos) == 0 && n.Address.Named():
		return cli.Usagef("disks without a node reaches the one --address ADDR names; NODE=ADDR goes with a node")
	case len(pos) == 0 && n.Address.Bare() != "":
		// A node not in the inventory yet: only maintenance mode, with a certificate when a secrets
		// file or a client file is given, for an image with an OS CA.
		var cert *tls.Certificate
		if n.Secrets.Path != "" || n.Config != "" {
			creds, err := a.loadCredentials(ctx, n.Secrets, n.Config)
			if err != nil {
				return err
			}
			cert = creds.cert
		}
		if conn, err = p.dial(n.Address.Bare(), "--address", cert); err != nil {
			return err
		}
	default:
		return cli.Usagef("disks needs a node or --address ADDR")
	}
	defer conn.Close()
	resp, err := conn.Disks(ctx, connect.NewRequest(&nodev1.DisksRequest{}))
	if err != nil {
		return err
	}
	if p.insecure {
		fmt.Fprintf(a.stderr, "chalkctl: the node's certificate fingerprint is %s\n", conn.Fingerprint())
	}
	return statusview.WriteDisks(a.stdout, resp.Msg.Disks)
}

type logsFlags struct {
	nodePinFlags
	follow bool
	unit   string
}

func (a *app) logsCommand() *cobra.Command {
	var f logsFlags
	cmd := a.command(&cobra.Command{
		Use:     "logs <node>",
		Short:   "Show a node's journal",
		Long:    logsLong,
		Example: logsExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.logs(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.RegisterClient(fs)
	f.pin.register(fs)
	fs.BoolVarP(&f.follow, "follow", "f", false, "keep printing new entries")
	fs.StringVar(&f.unit, "unit", "", "unit whose entries alone are printed")
	return cmd
}

func (a *app) logs(ctx context.Context, f logsFlags, pos []string) error {
	t, err := a.clientTarget(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	conn, err := a.dialEither(t, f.pin)
	if err != nil {
		return err
	}
	defer conn.Close()
	stream, err := conn.Logs(ctx, connect.NewRequest(&nodev1.LogsRequest{Unit: f.unit, Follow: f.follow}))
	if err != nil {
		return err
	}
	out := bufio.NewWriter(a.stdout)
	defer out.Flush()
	for stream.Receive() {
		fmt.Fprintln(out, stream.Msg().Line)
		if f.follow {
			out.Flush()
		}
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
