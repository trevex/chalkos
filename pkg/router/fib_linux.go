package router

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// KernelRoutes lists the routes of every table of the kernel, both families, in the order of
// their tables, families and destinations. zebra installs routes with nexthop objects; their next
// hops are those the kernel adds to the dump while net.ipv4.nexthop_compat_mode is 1, its
// default, as RTA_NH_ID is not resolved here. A dump the kernel interrupts because the routes
// changed meanwhile is listed as it came.
func KernelRoutes() ([]*KernelRoute, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list the node's interfaces: %w", err)
	}
	names := map[int]string{}
	for _, i := range ifaces {
		names[i.Index] = i.Name
	}
	var routes []*KernelRoute
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		var convErr error
		err := netlink.RouteListFilteredIter(family, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE, func(r netlink.Route) bool {
			k, err := kernelRoute(r, names)
			if err != nil {
				convErr = err
				return false
			}
			routes = append(routes, k)
			return true
		})
		if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
			return nil, fmt.Errorf("list the node's routes: %w", err)
		}
		if convErr != nil {
			return nil, convErr
		}
	}
	sortKernel(routes)
	return routes, nil
}

// kernelRoute converts a route of netlink, naming interfaces by index.
func kernelRoute(r netlink.Route, names map[int]string) (*KernelRoute, error) {
	k := &KernelRoute{Table: uint32(r.Table), Metric: uint32(r.Priority), Protocol: protocolName(uint8(r.Protocol)), Type: typeName(uint8(r.Type))}
	// netlink gives a route without RTA_DST, a default route, the destination of all addresses of
	// its family, as iproute2 does, for IPv4 and IPv6 alike; a route without one would be of
	// another family, which Routes does not list. The check keeps a change of netlink from
	// panicking.
	if r.Dst == nil {
		return nil, errors.New("list the node's routes: a route without a destination")
	}
	dst, ok := netip.AddrFromSlice(r.Dst.IP)
	if !ok {
		return nil, fmt.Errorf("list the node's routes: a route to %v", r.Dst)
	}
	// netlink gives a default route of IPv4 the 16 bytes of net.IPv4zero.
	if r.Family == netlink.FAMILY_V4 {
		dst = dst.Unmap()
	}
	bits, _ := r.Dst.Mask.Size()
	p, err := dst.Prefix(bits)
	if err != nil {
		return nil, fmt.Errorf("list the node's routes: a route to %v: %w", r.Dst, err)
	}
	k.Destination = p.String()
	for _, h := range r.MultiPath {
		k.NextHops = append(k.NextHops, &NextHop{Gateway: gateway(h.Gw, h.Via), Interface: names[h.LinkIndex], Weight: uint32(h.Hops) + 1})
	}
	if hop := (NextHop{Gateway: gateway(r.Gw, r.Via), Interface: names[r.LinkIndex]}); hop.Interface != "" || hop.Gateway != "" {
		k.NextHops = append(k.NextHops, &hop)
	}
	return k, nil
}

// gateway is a next hop's gateway, of the route's family or of another one (RTA_VIA), as for an
// IPv4 route over an IPv6 next hop; empty for a route on the link alone.
func gateway(gw net.IP, via netlink.Destination) string {
	if v, ok := via.(*netlink.Via); ok {
		gw = v.Addr
	}
	if ip, ok := netip.AddrFromSlice(gw); ok {
		return ip.String()
	}
	return ""
}
