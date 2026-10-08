package nodeip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
)

// ifaFlags is the attribute holding an address's flags beyond the eight its header has room
// for (IFA_FLAGS).
const ifaFlags = 8

// iflaInfoKind is the attribute of IFLA_LINKINFO that names a virtual interface's kind
// (IFLA_INFO_KIND).
const iflaInfoKind = 1

// SystemAddresses lists the addresses of the node's interfaces that are up. It leaves out
// addresses the node cannot be reached at or should not be registered with: tentative ones,
// whose duplicate address detection has not finished or failed, deprecated ones, and temporary
// IPv6 privacy addresses. net.Interfaces does not report these flags, so the addresses come
// from netlink, and so do the kinds of the interfaces.
func SystemAddresses() ([]Address, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list the node's interfaces: %w", err)
	}
	kinds, err := linkKinds()
	if err != nil {
		return nil, err
	}
	up := map[uint32]Link{}
	for _, i := range ifaces {
		if i.Flags&net.FlagUp != 0 {
			up[uint32(i.Index)] = Link{Name: i.Name, Loopback: i.Flags&net.FlagLoopback != 0, Kind: kinds[uint32(i.Index)], MTU: i.MTU}
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

// linkKinds returns the kinds of the node's virtual interfaces by index.
func linkKinds() (map[uint32]string, error) {
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETLINK, syscall.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("list the node's interfaces: %w", err)
	}
	msgs, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return nil, fmt.Errorf("list the node's interfaces: %w", err)
	}
	return parseLinkKinds(msgs)
}

// parseLinkKinds reads the kinds in the IFLA_LINKINFO attributes of RTM_NEWLINK messages.
func parseLinkKinds(msgs []syscall.NetlinkMessage) (map[uint32]string, error) {
	kinds := map[uint32]string{}
	for _, m := range msgs {
		if m.Header.Type == syscall.NLMSG_DONE {
			break
		}
		if m.Header.Type != syscall.RTM_NEWLINK {
			continue
		}
		if len(m.Data) < syscall.SizeofIfInfomsg {
			return nil, errors.New("list the node's interfaces: a short interface message")
		}
		// struct ifinfomsg: family, padding, type, index, flags, change.
		index := binary.NativeEndian.Uint32(m.Data[4:8])
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			return nil, fmt.Errorf("list the node's interfaces: %w", err)
		}
		for _, a := range attrs {
			if a.Attr.Type != syscall.IFLA_LINKINFO {
				continue
			}
			// IFLA_LINKINFO nests attributes: a length and a type of 16 bits each, the value,
			// padding to 4 bytes.
			for b := a.Value; len(b) >= syscall.SizeofRtAttr; {
				n := int(binary.NativeEndian.Uint16(b[0:2]))
				if n < syscall.SizeofRtAttr || n > len(b) {
					return nil, errors.New("list the node's interfaces: a malformed link info attribute")
				}
				if binary.NativeEndian.Uint16(b[2:4]) == iflaInfoKind {
					kinds[index] = strings.TrimRight(string(b[syscall.SizeofRtAttr:n]), "\x00")
				}
				b = b[min((n+3)&^3, len(b)):]
			}
		}
	}
	return kinds, nil
}

// parseAddresses reads RTM_NEWADDR messages of the interfaces in up, by index.
func parseAddresses(msgs []syscall.NetlinkMessage, up map[uint32]Link) ([]Address, error) {
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
		link, ok := up[binary.NativeEndian.Uint32(m.Data[4:8])]
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
			addrs = append(addrs, Address{Interface: link.Name, IP: ip.Unmap(), Link: link})
		}
	}
	return addrs, nil
}
