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
		{
			Use:   "nat64 [stop]",
			Short: "Bring the NAT64 instances, their namespaces, links and routes to the output jool",
			Long: "Bring Jool's instances, their network namespaces and links and the routes to them to the files\n" +
				"of the output jool, after checking every file. With stop, or without the output, remove them all.",
			Args: cobra.MatchAll(cli.RangeArgs(0, 1), func(cmd *cobra.Command, args []string) error {
				if len(args) == 1 && args[0] != "stop" {
					return cli.Usagef("%s takes stop alone", cmd.CommandPath())
				}
				return nil
			}),
			RunE: func(cmd *cobra.Command, args []string) error {
				return p.reconcileNAT64(cmd.Context(), len(args) == 1, cmd.ErrOrStderr())
			},
		},
		{
			Use:   "prune-devices",
			Short: "Remove the VRFs and WireGuard devices that no networkd unit declares",
			Long: "Remove each VRF that no .netdev unit declares with its table, after flushing the unreachable\n" +
				"defaults networkd put in its table, and each WireGuard device that no .netdev unit declares.",
			Args: cli.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return p.pruneDevices(cmd.Context(), cmd.ErrOrStderr()) },
		},
		{
			Use:   "record-device SERVICE VRF",
			Short: "Record the index of a VRF's device for the VRF's instance of a per-VRF service",
			Long: "Write the index of the device of VRF to $RUNTIME_DIRECTORY/SERVICE-VRF.ifindex, and fail while\n" +
				"the VRF has no device, as the ExecStartPre= of the VRF's instance of a per-VRF service.",
			Args: cli.ExactArgs(2),
			RunE: func(_ *cobra.Command, args []string) error {
				return p.recordDevice(args[0], args[1], os.Getenv("RUNTIME_DIRECTORY"))
			},
		},
		{
			Use:   "resolver-vrfs-ready",
			Short: "Say whether the VRFs of the rendered resolvers exist with their tables",
			Long: "Exit with status 0 when the VRF of each rendered resolver of a VRF exists with the table its\n" +
				"rendered unit gives, and with status 75 while one does not.",
			Args: cli.NoArgs,
			RunE: func(*cobra.Command, []string) error { return p.resolverVRFsReady() },
		},
		{
			Use:   "load-firewall RULESET FALLBACK",
			Short: "Load the rendered ruleset, or the fallback ruleset without one or when nft refuses it",
			Long: "Load the nftables ruleset RULESET, or FALLBACK, which forwards nothing, when the node has no\n" +
				"rendered ruleset. A ruleset nft refuses leaves FALLBACK loaded and fails.",
			Args: cli.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return p.loadFirewall(cmd.Context(), args[0], args[1], cmd.ErrOrStderr())
			},
		},
		{
			Use:   "dhcp6-pd HOOK",
			Short: "Record the prefixes a DHCPv6 server delegates, as Kea's run_script hook",
			Long: "Record or remove the prefixes the leases of Kea's hook point HOOK delegate, from the hook's\n" +
				"variables. Kea's run_script hook runs it through a link named chalkd-router-dhcp6-pd.",
			Args: cli.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error { return p.dhcp6PD(args[0], os.Getenv) },
		},
		ntpClientsCommand(p),
		nat64SessionsCommand(p),
		dhcpLeasesCommand(p),
	}
}

// ntpClientsCommand is chalkd router ntp-clients [--chronyc PATH].
func ntpClientsCommand(p *Plugin) *cobra.Command {
	chronyc := "chronyc"
	cmd := &cobra.Command{
		Use:   "ntp-clients",
		Short: "Print the NTP clients the time server answered and its counts of requests",
		Long: "Print the NTP clients the node's chronyd answered, as chronyc -n clients prints them, an empty\n" +
			"line, and its counts of requests, as chronyc serverstats prints them.",
		Args: cli.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return p.ntpClients(cmd.Context(), chronyc, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&chronyc, "chronyc", chronyc, "chronyc to run")
	return cmd
}

// nat64SessionsCommand is chalkd router nat64-sessions [--jool PATH] INSTANCE.
func nat64SessionsCommand(p *Plugin) *cobra.Command {
	jool := "jool"
	cmd := &cobra.Command{
		Use:   "nat64-sessions INSTANCE",
		Short: "Print a NAT64 instance's sessions",
		Long: "Print the TCP, UDP and ICMP sessions of the Jool instance INSTANCE of the network namespace the\n" +
			"command runs in, as jool session display prints them.",
		Args: cli.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return p.nat64Sessions(cmd.Context(), jool, args[0], cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&jool, "jool", jool, "jool to run")
	return cmd
}

// dhcpLeasesCommand is chalkd router dhcp-leases [--subnet NAME] [--mac ADDRESS].
func dhcpLeasesCommand(p *Plugin) *cobra.Command {
	var subnet, mac string
	cmd := &cobra.Command{
		Use:   "dhcp-leases",
		Short: "Print the leases of the DHCP servers",
		Long: "Print the leases of the DHCP servers of every VRF, or of one subnet or MAC address, a line each of\n" +
			"the lease's VRF, subnet, address or delegated prefix, MAC address, host name and end.",
		Args: cli.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return dhcpLeases(filepath.Join(p.RunDir, "kea"), subnet, mac, keaAskTimeout, time.Now().Add(leasesBudget), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&subnet, "subnet", "", "name of the DHCPSubnet document whose leases to print")
	cmd.Flags().StringVar(&mac, "mac", "", "MAC address whose leases to print")
	return cmd
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
