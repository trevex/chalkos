package router

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"connectrpc.com/connect"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
)

// PrintStatus prints the router's BGP sessions, its NAT64 instances, its resolvers, its DHCP
// servers and its DHCP relays.
func (p *Plugin) PrintStatus(ctx context.Context, h layer.Host, conn *client.Conn) error {
	resp, err := Client(conn).Status(ctx, connect.NewRequest(&routerv1.StatusRequest{}))
	if err != nil {
		return layer.ServiceError(err, p.Name())
	}
	return routerStatus(h, resp.Msg)
}

// routerStatus prints a router's BGP sessions, its NAT64 instances, its resolvers and its DHCP
// servers with their subnets' leases and delegated prefixes, and its DHCP relays, the default
// VRF's first, or why their state is unknown. A router whose image carries no BGP prints no line
// of it, and one without NAT64 instances, resolvers, DHCP servers or relays none of them.
func routerStatus(h layer.Host, r *routerv1.StatusResponse) error {
	if err := bgpStatus(h, r); err != nil {
		return err
	}
	switch {
	case r.Nat64Error != "":
		fmt.Fprintf(h.Stdout(), "nat64: unknown: %s\n", r.Nat64Error)
	case len(r.Nat64) > 0:
		fmt.Fprintln(h.Stdout(), "nat64:")
		w := h.Table()
		fmt.Fprintln(w, "  INSTANCE\tSESSIONS\tBINDINGS\tPOOL TCP\tPOOL UDP\tPOOL ICMP")
		for _, n := range r.Nat64 {
			pools := map[string]string{}
			for _, p := range n.Pools {
				if p.Size > 0 {
					pools[p.Protocol] = strconv.FormatUint(p.Size, 10)
				}
			}
			fmt.Fprintf(w, "  %s\t%d\t%d\t%s\t%s\t%s\n", n.Name, n.Sessions, n.Bindings, cmp.Or(pools["tcp"], "-"), cmp.Or(pools["udp"], "-"), cmp.Or(pools["icmp"], "-"))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	switch {
	case r.Resolver == nil:
	case r.Resolver.Running:
		fmt.Fprintln(h.Stdout(), "resolver: running")
	default:
		fmt.Fprintf(h.Stdout(), "resolver: not running: %s\n", r.Resolver.Error)
	}
	for _, v := range r.VrfResolvers {
		if v.Running {
			fmt.Fprintf(h.Stdout(), "resolver %s: running\n", v.Vrf)
		} else {
			fmt.Fprintf(h.Stdout(), "resolver %s: not running: %s\n", v.Vrf, v.Error)
		}
	}
	for _, d := range r.Dhcp {
		// A chalkd of DHCPv4 alone gives no family.
		name := "dhcp4"
		if d.Family == "ipv6" {
			name = "dhcp6"
		}
		if !d.Running {
			fmt.Fprintf(h.Stdout(), "%s %s: not running: %s\n", name, d.Vrf, d.Error)
			continue
		}
		fmt.Fprintf(h.Stdout(), "%s %s: running\n", name, d.Vrf)
		if len(d.Subnets) == 0 {
			continue
		}
		w := h.Table()
		if name == "dhcp6" {
			fmt.Fprintln(w, "  SUBNET\tRANGE\tLEASED\tPOOL\tDELEGATED\tDELEGABLE")
		} else {
			fmt.Fprintln(w, "  SUBNET\tRANGE\tLEASED\tPOOL")
		}
		for _, s := range d.Subnets {
			if name == "dhcp6" {
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Subnet, dhcpCount(s.Leased), dhcpCount(s.Pool), dhcpCount(s.Delegated), dhcpCount(s.Delegable))
			} else {
				fmt.Fprintf(w, "  %s\t%s\t%d\t%d\n", s.Name, s.Subnet, s.Leased, s.Pool)
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	for _, d := range r.DhcpRelays {
		if d.Running {
			fmt.Fprintf(h.Stdout(), "dhcp-relay %s: running\n", d.Vrf)
		} else {
			fmt.Fprintf(h.Stdout(), "dhcp-relay %s: not running: %s\n", d.Vrf, d.Error)
		}
	}
	return nil
}

// bgpStatus prints a router's BGP sessions, in chalkd's order, by VRF, or why their state is
// unknown.
func bgpStatus(h layer.Host, r *routerv1.StatusResponse) error {
	switch {
	case r.BgpAbsent:
		return nil
	case r.BgpError != "":
		fmt.Fprintf(h.Stdout(), "bgp: unknown: %s\n", r.BgpError)
		return nil
	case len(r.Peers) == 0:
		fmt.Fprintln(h.Stdout(), "bgp: no sessions")
		return nil
	}
	fmt.Fprintln(h.Stdout(), "bgp:")
	w := h.Table()
	fmt.Fprintln(w, "  VRF\tPEER\tNEIGHBOR\tSTATE\tUPTIME\tRECEIVED\tSENT\tBFD")
	for _, p := range r.Peers {
		uptime := "-"
		if p.State == "Established" {
			uptime = (time.Duration(p.UptimeSeconds) * time.Second).String()
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n", cmp.Or(p.Vrf, "default"), p.Name, p.Neighbor, p.State, uptime, p.PrefixesReceived, p.PrefixesSent, cmp.Or(p.Bfd, "-"))
	}
	return w.Flush()
}

// dhcpCount is a count of a DHCP server's status, which chalkd saturates at the largest number
// of 64 bits when Kea counts more, as of the addresses of an IPv6 pool of /64.
func dhcpCount(n uint64) string {
	if n == math.MaxUint64 {
		return ">=2^64"
	}
	return strconv.FormatUint(n, 10)
}
