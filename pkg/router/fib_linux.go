package router

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// The route attributes the kernel's routes carry besides those of package syscall.
const (
	// rtaMultipath holds a route's next hops, each a struct rtnexthop with attributes of its own.
	rtaMultipath = 9
	// rtaTable holds a table above 255, which the header's byte cannot.
	rtaTable = 15
	// rtaVia is a gateway of another family than the route's: a family of 16 bits, then the
	// address, as for an IPv4 route over an IPv6 next hop.
	rtaVia = 18
)

// KernelRoutes lists the routes of every table of the kernel, both families, in the order of
// their tables, families and destinations. zebra installs routes with nexthop objects; their next
// hops are those the kernel adds to the dump while net.ipv4.nexthop_compat_mode is 1, its
// default, as RTA_NH_ID is not resolved here.
func KernelRoutes() ([]*KernelRoute, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list the node's interfaces: %w", err)
	}
	names := map[uint32]string{}
	for _, i := range ifaces {
		names[uint32(i.Index)] = i.Name
	}
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("list the node's routes: %w", err)
	}
	msgs, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return nil, fmt.Errorf("list the node's routes: %w", err)
	}
	routes, err := parseRoutes(msgs, names)
	if err != nil {
		return nil, err
	}
	sortKernel(routes)
	return routes, nil
}

// parseRoutes reads RTM_NEWROUTE messages of IPv4 and IPv6, naming interfaces by index.
func parseRoutes(msgs []syscall.NetlinkMessage, names map[uint32]string) ([]*KernelRoute, error) {
	var routes []*KernelRoute
	for _, m := range msgs {
		if m.Header.Type == syscall.NLMSG_DONE {
			break
		}
		if m.Header.Type != syscall.RTM_NEWROUTE {
			continue
		}
		if len(m.Data) < syscall.SizeofRtMsg {
			return nil, errors.New("list the node's routes: a short route message")
		}
		// struct rtmsg: family, destination length, source length, TOS, table, protocol, scope,
		// type, flags.
		family, bits := m.Data[0], int(m.Data[1])
		var dst netip.Addr
		switch family {
		case syscall.AF_INET:
			dst = netip.IPv4Unspecified()
		case syscall.AF_INET6:
			dst = netip.IPv6Unspecified()
		default:
			continue
		}
		r := &KernelRoute{Table: uint32(m.Data[4]), Protocol: protocolName(m.Data[5]), Type: typeName(m.Data[7])}
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			return nil, fmt.Errorf("list the node's routes: %w", err)
		}
		var hop NextHop
		for _, a := range attrs {
			switch a.Attr.Type {
			case syscall.RTA_DST:
				if ip, ok := netip.AddrFromSlice(a.Value); ok {
					dst = ip
				}
			case rtaTable:
				if len(a.Value) == 4 {
					r.Table = binary.NativeEndian.Uint32(a.Value)
				}
			case syscall.RTA_PRIORITY:
				if len(a.Value) == 4 {
					r.Metric = binary.NativeEndian.Uint32(a.Value)
				}
			case syscall.RTA_OIF:
				if len(a.Value) == 4 {
					hop.Interface = names[binary.NativeEndian.Uint32(a.Value)]
				}
			case syscall.RTA_GATEWAY:
				hop.Gateway = address(a.Value)
			case rtaVia:
				hop.Gateway = via(a.Value)
			case rtaMultipath:
				hops, err := nextHops(a.Value, names)
				if err != nil {
					return nil, err
				}
				r.NextHops = append(r.NextHops, hops...)
			}
		}
		if hop.Interface != "" || hop.Gateway != "" {
			r.NextHops = append(r.NextHops, &hop)
		}
		p, err := dst.Prefix(bits)
		if err != nil {
			return nil, fmt.Errorf("list the node's routes: a route to %v/%d: %w", dst, bits, err)
		}
		r.Destination = p.String()
		routes = append(routes, r)
	}
	return routes, nil
}

// address is an IPv4 or IPv6 address in netlink's bytes, or empty.
func address(b []byte) string {
	if ip, ok := netip.AddrFromSlice(b); ok {
		return ip.String()
	}
	return ""
}

// via is the address of an RTA_VIA attribute: a family of 16 bits, then the address.
func via(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	return address(b[2:])
}

// nextHops reads RTA_MULTIPATH: struct rtnexthop, a length of 16 bits, flags, hops, the
// interface's index of 32 bits, then attributes, each padded to 4 bytes. hops is the weight less
// one.
func nextHops(b []byte, names map[uint32]string) ([]*NextHop, error) {
	var hops []*NextHop
	for len(b) >= 8 {
		n := int(binary.NativeEndian.Uint16(b[0:2]))
		if n < 8 || n > len(b) {
			return nil, errors.New("list the node's routes: a malformed next hop")
		}
		h := &NextHop{Weight: uint32(b[3]) + 1, Interface: names[binary.NativeEndian.Uint32(b[4:8])]}
		for a := b[8:n]; len(a) >= syscall.SizeofRtAttr; {
			l := int(binary.NativeEndian.Uint16(a[0:2]))
			if l < syscall.SizeofRtAttr || l > len(a) {
				return nil, errors.New("list the node's routes: a malformed attribute of a next hop")
			}
			switch binary.NativeEndian.Uint16(a[2:4]) {
			case syscall.RTA_GATEWAY:
				h.Gateway = address(a[syscall.SizeofRtAttr:l])
			case rtaVia:
				h.Gateway = via(a[syscall.SizeofRtAttr:l])
			}
			a = a[min((l+3)&^3, len(a)):]
		}
		hops = append(hops, h)
		b = b[min((n+3)&^3, len(b)):]
	}
	return hops, nil
}
