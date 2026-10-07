package nodeip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// ifaFlags is the attribute holding an address's flags beyond the eight its header has room
// for (IFA_FLAGS).
const ifaFlags = 8

// SystemAddresses lists the addresses of the node's interfaces that are up. It leaves out
// addresses the node cannot be reached at or should not be registered with: tentative ones,
// whose duplicate address detection has not finished or failed, deprecated ones, and temporary
// IPv6 privacy addresses. net.Interfaces does not report these flags, so the addresses come
// from netlink.
func SystemAddresses() ([]Address, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list the node's interfaces: %w", err)
	}
	up := map[uint32]string{}
	for _, i := range ifaces {
		if i.Flags&net.FlagUp != 0 {
			up[uint32(i.Index)] = i.Name
		}
	}
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETADDR, syscall.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("list the node's addresses: %w", err)
	}
	msgs, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return nil, fmt.Errorf("list the node's addresses: %w", err)
	}
	return parseAddresses(msgs, up)
}

// parseAddresses reads RTM_NEWADDR messages of the interfaces in up, by index.
func parseAddresses(msgs []syscall.NetlinkMessage, up map[uint32]string) ([]Address, error) {
	var addrs []Address
	for _, m := range msgs {
		if m.Header.Type == syscall.NLMSG_DONE {
			break
		}
		if m.Header.Type != syscall.RTM_NEWADDR {
			continue
		}
		if len(m.Data) < syscall.SizeofIfAddrmsg {
			return nil, errors.New("list the node's addresses: a short address message")
		}
		// struct ifaddrmsg: family, prefix length, flags, scope, interface index.
		family, flags := m.Data[0], uint32(m.Data[2])
		name, ok := up[binary.NativeEndian.Uint32(m.Data[4:8])]
		if !ok {
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			return nil, fmt.Errorf("list the node's addresses: %w", err)
		}
		var address, local []byte
		for _, a := range attrs {
			switch a.Attr.Type {
			case syscall.IFA_ADDRESS:
				address = a.Value
			case syscall.IFA_LOCAL:
				local = a.Value
			case ifaFlags:
				if len(a.Value) == 4 {
					flags = binary.NativeEndian.Uint32(a.Value)
				}
			}
		}
		unusable := uint32(syscall.IFA_F_TENTATIVE | syscall.IFA_F_DADFAILED | syscall.IFA_F_DEPRECATED)
		// The same bit marks secondary IPv4 addresses, which are the node's own.
		if family == syscall.AF_INET6 {
			unusable |= syscall.IFA_F_TEMPORARY
		}
		if flags&unusable != 0 {
			continue
		}
		// On point-to-point links IFA_ADDRESS is the peer's address; IFA_LOCAL is the node's.
		raw := address
		if local != nil {
			raw = local
		}
		if ip, ok := netip.AddrFromSlice(raw); ok {
			addrs = append(addrs, Address{Interface: name, IP: ip.Unmap()})
		}
	}
	return addrs, nil
}
