package router

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"time"

	"connectrpc.com/connect"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
)

// PrintStatus prints the router's BGP sessions, its NAT64 instances and its resolver.
func (p *Plugin) PrintStatus(ctx context.Context, h layer.Host, conn *client.Conn) error {
	resp, err := Client(conn).Status(ctx, connect.NewRequest(&routerv1.StatusRequest{}))
	if err != nil {
		return err
	}
	return routerStatus(h, resp.Msg)
}

// routerStatus prints a router's BGP sessions, its NAT64 instances and its resolver, or why their
// state is unknown. A router without NAT64 instances or a resolver prints no line of them.
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
	return nil
}

// bgpStatus prints a router's BGP sessions, or why their state is unknown.
func bgpStatus(h layer.Host, r *routerv1.StatusResponse) error {
	switch {
	case r.BgpError != "":
		fmt.Fprintf(h.Stdout(), "bgp: unknown: %s\n", r.BgpError)
		return nil
	case len(r.Peers) == 0:
		fmt.Fprintln(h.Stdout(), "bgp: no sessions")
		return nil
	}
	fmt.Fprintln(h.Stdout(), "bgp:")
	w := h.Table()
	fmt.Fprintln(w, "  PEER\tNEIGHBOR\tSTATE\tUPTIME\tRECEIVED\tSENT\tBFD")
	for _, p := range r.Peers {
		uptime := "-"
		if p.State == "Established" {
			uptime = (time.Duration(p.UptimeSeconds) * time.Second).String()
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%d\t%d\t%s\n", p.Name, p.Neighbor, p.State, uptime, p.PrefixesReceived, p.PrefixesSent, cmp.Or(p.Bfd, "-"))
	}
	return w.Flush()
}
