package chalkctl

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
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
	f.node.registerClient(cmd.Flags())
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
		if p.set() {
			conn, err = p.dial(t.addr, t.source, t.creds.cert)
		} else {
			conn, err = dialInstalled(t)
		}
		if err != nil {
			return err
		}
	case len(pos) == 0 && n.address != "":
		// A node not in the inventory yet: only maintenance mode, with a certificate when a secrets
		// file or a client file is given, for an image with an OS CA.
		var cert *tls.Certificate
		if n.secrets.path != "" || n.config != "" {
			creds, err := a.loadCredentials(ctx, n.secrets, n.config)
			if err != nil {
				return err
			}
			cert = creds.cert
		}
		if conn, err = p.dial(n.address, "--address", cert); err != nil {
			return err
		}
	default:
		return cli.Usagef("disks needs a node or --address")
	}
	defer conn.Close()
	resp, err := conn.Disks(ctx, connect.NewRequest(&nodev1.DisksRequest{}))
	if err != nil {
		return err
	}
	if p.insecure {
		fmt.Fprintf(a.stderr, "chalkctl: the node's certificate fingerprint is %s\n", conn.Fingerprint())
	}
	w := a.table()
	fmt.Fprintln(w, "DEVICE\tSIZE\tTYPE\tMODEL\tSERIAL\tWWN\tUSE")
	for _, d := range resp.Msg.Disks {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.Device, size(d.Size), d.Type, d.Model, d.Serial, d.Wwn, d.Usage)
		for _, part := range d.Partitions {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t\t\t\n", part.Device, size(part.Size), part.Label, part.Content)
		}
	}
	return w.Flush()
}

func size(bytes uint64) string {
	units := []string{"B", "K", "M", "G", "T", "P"}
	v := float64(bytes)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + units[i]
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
	f.node.registerClient(fs)
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

func (a *app) rebootCommand() *cobra.Command {
	var f nodePinFlags
	cmd := a.command(&cobra.Command{
		Use:     "reboot <node>",
		Short:   "Reboot a node",
		Long:    rebootLong,
		Example: rebootExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.reboot(ctx, f, pos) })
	f.node.registerClient(cmd.Flags())
	f.pin.register(cmd.Flags())
	return cmd
}

func (a *app) reboot(ctx context.Context, f nodePinFlags, pos []string) error {
	t, err := a.clientTarget(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	conn, err := a.dialEither(t, f.pin)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Reboot(ctx, connect.NewRequest(&nodev1.RebootRequest{})); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "%s reboots\n", t.name)
	return nil
}
