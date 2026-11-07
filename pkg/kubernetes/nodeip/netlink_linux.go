package nodeip

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// SystemAddresses lists the addresses of the node's interfaces that are up. It leaves out
// addresses the node cannot be reached at or should not be registered with (see usable).
// net.Interfaces does not report these flags, so the addresses come from netlink, and so do the
// kinds of the interfaces. A dump the kernel interrupts because the interfaces changed meanwhile
// is taken as it came.
func SystemAddresses() ([]Address, error) {
	links, err := netlink.LinkList()
	if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
		return nil, fmt.Errorf("list the node's interfaces: %w", err)
	}
	up := map[int]Link{}
	for _, l := range links {
		a := l.Attrs()
		if a.Flags&net.FlagUp == 0 {
			continue
		}
		// The kind is the link's type as netlink names it, which is IFLA_INFO_KIND for most links;
		// netlink derives some, such as the gre and vti kinds, from a tunnel's local address
		// instead. A link without a kind, a physical one or the loopback, is a Device, and a tun is a
		// Tuntap.
		kind := l.Type()
		switch l.(type) {
		case *netlink.Device:
			kind = ""
		case *netlink.Tuntap:
			kind = "tun"
		}
		up[a.Index] = Link{Name: a.Name, Loopback: a.Flags&net.FlagLoopback != 0, Kind: kind, MTU: a.MTU}
	}
	addrs, err := netlink.AddrList(nil, netlink.FAMILY_ALL)
	if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
		return nil, fmt.Errorf("list the node's addresses: %w", err)
	}
	var found []Address
	for _, a := range addrs {
		link, ok := up[a.LinkIndex]
		if !ok || a.IPNet == nil {
			continue
		}
		// On point-to-point links the address is IFA_LOCAL, the node's, and Peer the other end's.
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok || !usable(len(a.IP) == net.IPv6len, uint32(a.Flags)) {
			continue
		}
		found = append(found, Address{Interface: link.Name, IP: ip.Unmap(), Link: link})
	}
	return found, nil
}

// usable reports whether an address of the flags may be registered: not tentative, whose
// duplicate address detection has not finished or failed, not deprecated, and, for IPv6, not a
// temporary privacy address.
func usable(ipv6 bool, flags uint32) bool {
	unusable := uint32(unix.IFA_F_TENTATIVE | unix.IFA_F_DADFAILED | unix.IFA_F_DEPRECATED)
	// The same bit marks secondary IPv4 addresses, which are the node's own.
	if ipv6 {
		unusable |= unix.IFA_F_TEMPORARY
	}
	return flags&unusable == 0
}
