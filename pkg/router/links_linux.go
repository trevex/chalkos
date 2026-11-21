package router

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Link is a network device of the node's network namespace.
type Link struct {
	Name string
	// Kind is the device's rtnetlink kind, such as veth, vrf or wireguard; empty for a device of
	// none, such as a physical port.
	Kind string
	// Table is a VRF's routing table, 0 for other kinds.
	Table uint32
}

// Links lists the network devices of the node's network namespace. A dump the kernel interrupts
// because the devices changed is listed as it came.
func Links() ([]Link, error) {
	links, err := netlink.LinkList()
	if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
		return nil, fmt.Errorf("list the node's network devices: %w", err)
	}
	out := make([]Link, 0, len(links))
	for _, l := range links {
		k := Link{Name: l.Attrs().Name, Kind: l.Type()}
		switch v := l.(type) {
		case *netlink.Vrf:
			k.Table = v.Table
		case *netlink.Device:
			// netlink's type of a device without a kind.
			k.Kind = ""
		}
		out = append(out, k)
	}
	return out, nil
}

// Addr is an address of a network device.
type Addr struct {
	// Link is the device's name.
	Link string
	// Prefix is the address with the length of its subnet, as ip addr shows it.
	Prefix netip.Prefix
}

// IPv4Addrs lists the IPv4 addresses of the device link, or of every device of the node's network
// namespace when link is empty, in the kernel's order.
func IPv4Addrs(link string) ([]Addr, error) {
	var dev netlink.Link
	if link != "" {
		var err error
		if dev, err = netlink.LinkByName(link); err != nil {
			return nil, fmt.Errorf("find the network device %s: %w", link, err)
		}
	}
	addrs, err := netlink.AddrList(dev, netlink.FAMILY_V4)
	if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
		return nil, fmt.Errorf("list the node's IPv4 addresses: %w", err)
	}
	names := map[int]string{}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, i := range ifaces {
			names[i.Index] = i.Name
		}
	} else {
		return nil, fmt.Errorf("list the node's interfaces: %w", err)
	}
	out := make([]Addr, 0, len(addrs))
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			return nil, fmt.Errorf("list the node's IPv4 addresses: an address %v", a.IP)
		}
		bits, _ := a.Mask.Size()
		out = append(out, Addr{Link: names[a.LinkIndex], Prefix: netip.PrefixFrom(ip.Unmap(), bits)})
	}
	return out, nil
}

// TableRoute is a route's table and destination.
type TableRoute struct {
	Table       uint32
	Destination netip.Prefix
}

// ProtocolRoutes lists the routes of protocol proto of family, netlink.FAMILY_V4 or FAMILY_V6, in
// every table.
func ProtocolRoutes(family, proto int) ([]TableRoute, error) {
	var out []TableRoute
	var convErr error
	err := netlink.RouteListFilteredIter(family, &netlink.Route{Table: unix.RT_TABLE_UNSPEC, Protocol: netlink.RouteProtocol(proto)}, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL, func(r netlink.Route) bool {
		if r.Dst == nil {
			convErr = errors.New("list the node's routes: a route without a destination")
			return false
		}
		dst, ok := netip.AddrFromSlice(r.Dst.IP)
		if !ok {
			convErr = fmt.Errorf("list the node's routes: a route to %v", r.Dst)
			return false
		}
		if family == netlink.FAMILY_V4 {
			dst = dst.Unmap()
		}
		bits, _ := r.Dst.Mask.Size()
		p, err := dst.Prefix(bits)
		if err != nil {
			convErr = fmt.Errorf("list the node's routes: a route to %v: %w", r.Dst, err)
			return false
		}
		out = append(out, TableRoute{Table: uint32(r.Table), Destination: p})
		return true
	})
	if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
		return nil, fmt.Errorf("list the node's routes: %w", err)
	}
	return out, convErr
}
