package router

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	routing "github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/services"
	"github.com/trevex/chalkos/pkg/tool"
)

var _ layer.Commands = (*Plugin)(nil)

// Commands are the router layer's subcommands of chalkd.
func (p *Plugin) Commands() []*cobra.Command {
	return []*cobra.Command{
		{
			Use:   "dhcp6-routes",
			Short: "Route each prefix the DHCPv6 servers delegated to the link-local address that asked for it",
			Long: "Route each prefix the DHCPv6 servers delegated to the link-local address that asked for it,\n" +
				"until its lease ends, and remove the other routes of delegated prefixes.",
			Args: cli.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				return p.dhcp6Routes(ctx, time.Now())
			},
		},
		{
			Use:   "services kea|unbound|relay [stop]",
			Short: "Run the instances of a per-VRF service from its rendered configs",
			Long: "Start the instances of the rendered configs of a per-VRF service once their devices exist,\n" +
				"reload or restart those that run and stop the others. With stop, stop them all.",
			Args: cobra.MatchAll(cli.RangeArgs(1, 2), func(cmd *cobra.Command, args []string) error {
				if _, ok := p.perVRFServices()[args[0]]; !ok {
					return cli.Usagef("%s runs no service %q", cmd.CommandPath(), args[0])
				}
				if len(args) == 2 && args[1] != "stop" {
					return cli.Usagef("%s takes kea, unbound or relay, and optionally stop", cmd.CommandPath())
				}
				return nil
			}),
			RunE: func(_ *cobra.Command, args []string) error {
				return p.runServices(context.Background(), p.perVRFServices()[args[0]], len(args) == 2)
			},
		},
	}
}

// dhcp6Routes routes each prefix Kea's hook recorded as delegated, on a link a rendered config
// delegates on, to the client that asked for it in the link's VRF until its lease ends, and
// removes other dhcp-pd routes. The hook removes the records; ended ones route nothing. With more
// records than it reads it removes no route, and without the output kea, as in maintenance mode,
// it changes nothing: the kernel drops the routes as their leases end. A route ip fails to change
// is logged to standard error and left to the next run, so a link down at boot does not fail the
// boot's health check.
func (p *Plugin) dhcp6Routes(ctx context.Context, now time.Time) error {
	var pools []routing.PDPool
	if _, err := os.Stat(p.KeaDir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	for _, vrf := range renderedVRFs(p.KeaDir, services.Kea6) {
		f := filepath.Join(p.KeaDir, services.Kea6.File(vrf))
		data, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		p, err := routing.ParseKea6Pools(vrf, data)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		pools = append(pools, p...)
	}
	routes, err := routing.ReadPDRecords(p.PDDir, pools, now)
	truncated := errors.Is(err, routing.ErrTooManyPDRecords)
	if truncated {
		fmt.Fprintf(os.Stderr, "chalkd router dhcp6-routes: %v; routing the first %d and removing no route\n", err, routing.MaxPDRecords)
	} else if err != nil {
		return err
	}
	kernel, err := p.KernelRoutes()
	if err != nil {
		return err
	}
	for _, k := range kernel {
		// Only IPv6 routes are of delegated prefixes, and one of several next hops is of none.
		if k.Protocol != "dhcp-pd" || !strings.Contains(k.Destination, ":") {
			continue
		}
		kept := truncated
		for _, rt := range routes {
			kept = kept || (len(k.NextHops) == 1 && rt.Prefix.String() == k.Destination &&
				rt.Via.String() == k.NextHops[0].Gateway && rt.Link == k.NextHops[0].Interface)
		}
		if kept {
			continue
		}
		if _, err := tool.Run(ctx, p.run, "ip", "-6", "route", "del", k.Destination, "table", strconv.FormatUint(uint64(k.Table), 10), "proto", strconv.Itoa(routing.PDProtocol)); err != nil {
			fmt.Fprintf(os.Stderr, "chalkd router dhcp6-routes: remove the route of %s: %v\n", k.Destination, err)
		}
	}
	for _, rt := range routes {
		args := []string{"-6", "route", "replace", rt.Prefix.String(), "via", rt.Via.String(), "dev", rt.Link, "proto", strconv.Itoa(routing.PDProtocol)}
		if rt.VRF != derive.DefaultVRF {
			args = append(args, "vrf", rt.VRF)
		}
		// The kernel removes the route when the lease ends, should no renewal come.
		args = append(args, "expires", strconv.FormatInt(int64(max(rt.Ends.Sub(now)/time.Second, 1)), 10))
		if _, err := tool.Run(ctx, p.run, "ip", args...); err != nil {
			fmt.Fprintf(os.Stderr, "chalkd router dhcp6-routes: route %s to %s on %s: %v\n", rt.Prefix, rt.Via, rt.Link, err)
		}
	}
	return nil
}
