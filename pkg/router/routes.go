package router

import (
	"cmp"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
)

// Tables are the routing tables the kernel names, by name.
var Tables = map[string]uint32{"default": 253, "main": 254, "local": 255}

// protocols are the route protocols the kernel names, as iproute2 does, and nat64, the protocol of
// the routes of the router's NAT64 prefixes.
var protocols = map[uint8]string{
	1: "redirect", 2: "kernel", 3: "boot", 4: "static", 8: "gated", 9: "ra", 10: "mrt", 11: "zebra", 12: "bird",
	13: "dnrouted", 14: "xorp", 15: "ntk", 16: "dhcp", 17: "mrouted", 18: "keepalived", 42: "babel", 64: "nat64",
	99: "openr", 186: "bgp", 187: "isis", 188: "ospf", 189: "rip", 192: "eigrp",
}

// routeTypes are the kernel's route types, by number.
var routeTypes = []string{"unspec", "unicast", "local", "broadcast", "anycast", "multicast", "blackhole", "unreachable", "prohibit", "throw"}

// protocolName is the name of a route protocol, or its number.
func protocolName(p uint8) string {
	if name, ok := protocols[p]; ok {
		return name
	}
	return strconv.Itoa(int(p))
}

// typeName is the name of a route type, or its number.
func typeName(t uint8) string {
	if int(t) < len(routeTypes) {
		return routeTypes[t]
	}
	return strconv.Itoa(int(t))
}

// Filter is what a Routes request keeps: routes within a prefix, the kernel's of one protocol and
// one table, or of every table.
type Filter struct {
	prefix    netip.Prefix
	protocol  string
	table     uint32
	allTables bool
}

// NewFilter reads a Routes request's prefix, protocol and table; empty ones keep every prefix,
// every protocol and the main table.
func NewFilter(prefix, protocol, table string) (Filter, error) {
	f := Filter{protocol: protocol, table: Tables["main"]}
	if prefix != "" {
		p, err := netip.ParsePrefix(prefix)
		if err != nil {
			return f, fmt.Errorf("prefix %q is no address range, such as 2001:db8::/32", prefix)
		}
		f.prefix = p.Masked()
	}
	if protocol != "" && !slices.Contains(slices.Collect(maps.Values(protocols)), protocol) {
		n, err := strconv.ParseUint(protocol, 10, 8)
		if err != nil {
			return f, fmt.Errorf("protocol %q is none the kernel names, such as kernel, static, ra, dhcp or bgp, nor a number from 0 to 255", protocol)
		}
		// The kernel's routes carry the name of a protocol that has one.
		f.protocol = protocolName(uint8(n))
	}
	switch {
	case table == "" || table == "main":
	case table == "all":
		f.allTables = true
	default:
		if n, ok := Tables[table]; ok {
			f.table = n
			break
		}
		n, err := strconv.ParseUint(table, 10, 32)
		if err != nil {
			return f, fmt.Errorf("table %q is none of main, local, default and all, nor a table's number", table)
		}
		f.table = uint32(n)
	}
	return f, nil
}

// within reports whether the address range d lies within the filter's prefix.
func (f Filter) within(d string) bool {
	if !f.prefix.IsValid() {
		return true
	}
	p, err := netip.ParsePrefix(d)
	return err == nil && p.Addr().Is4() == f.prefix.Addr().Is4() && p.Bits() >= f.prefix.Bits() && f.prefix.Contains(p.Addr())
}

// Kernel keeps the kernel routes the filter keeps.
func (f Filter) Kernel(routes []*KernelRoute) []*KernelRoute {
	var kept []*KernelRoute
	for _, r := range routes {
		if (f.allTables || r.Table == f.table) && (f.protocol == "" || r.Protocol == f.protocol) && f.within(r.Destination) {
			kept = append(kept, r)
		}
	}
	return kept
}

// WantsBGP reports whether the filter keeps BGP's paths: for the protocol bgp, or for any.
func (f Filter) WantsBGP() bool {
	return f.protocol == "" || f.protocol == "bgp"
}

// BGP keeps the BGP paths within the filter's prefix.
func (f Filter) BGP(routes []*BGPRoute) []*BGPRoute {
	var kept []*BGPRoute
	for _, r := range routes {
		if f.within(r.Prefix) {
			kept = append(kept, r)
		}
	}
	return kept
}

// sortKernel orders kernel routes by table, family, destination and metric.
func sortKernel(routes []*KernelRoute) {
	slices.SortStableFunc(routes, func(a, b *KernelRoute) int {
		if c := cmp.Compare(a.Table, b.Table); c != 0 {
			return c
		}
		if c := comparePrefixes(a.Destination, b.Destination); c != 0 {
			return c
		}
		return cmp.Compare(a.Metric, b.Metric)
	})
}
