// Package vip puts the cluster's virtual IPs on a node: it adds them to an interface and
// announces them to the neighbours, so traffic for them reaches this node, and removes them.
package vip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// Address is a virtual IP on an interface.
type Address struct {
	IP        netip.Addr
	Interface string
}

func (a Address) String() string { return a.IP.String() + " on " + a.Interface }

// Add adds the address to its interface as a host address, /32 or /128. An IPv6 address skips
// duplicate address detection, which would hold it back while the neighbours still reach the
// node that held it before.
func Add(a Address) error {
	ifi, err := net.InterfaceByName(a.Interface)
	if err != nil {
		return fmt.Errorf("add %s: %w", a, err)
	}
	flags := uint16(unix.NLM_F_CREATE | unix.NLM_F_REPLACE)
	if err := addrRequest(unix.RTM_NEWADDR, flags, a.IP, ifi.Index); err != nil {
		return fmt.Errorf("add %s: %w", a, err)
	}
	return nil
}

// Remove removes the address from its interface. An address that is not there, also on an
// interface that is gone, is no error.
func Remove(a Address) error {
	ifi, err := net.InterfaceByName(a.Interface)
	if err != nil {
		return nil
	}
	err = addrRequest(unix.RTM_DELADDR, 0, a.IP, ifi.Index)
	if errors.Is(err, unix.EADDRNOTAVAIL) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove %s: %w", a, err)
	}
	return nil
}

// Announce tells the neighbours on the interface that the address is here now: with a
// gratuitous ARP request for an IPv4 address, an unsolicited neighbour advertisement for an IPv6
// address.
func Announce(a Address) error {
	ifi, err := net.InterfaceByName(a.Interface)
	if err != nil {
		return fmt.Errorf("announce %s: %w", a, err)
	}
	if len(ifi.HardwareAddr) != 6 {
		return fmt.Errorf("announce %s: the interface has no Ethernet address", a)
	}
	if a.IP.Is4() {
		err = sendARP(ifi, a.IP)
	} else {
		err = sendNA(ifi, a.IP)
	}
	if err != nil {
		return fmt.Errorf("announce %s: %w", a, err)
	}
	return nil
}

// addrRequest sends an RTM_NEWADDR or RTM_DELADDR request for ip as a host address on the
// interface with the index, and waits for the kernel's answer.
func addrRequest(typ uint16, flags uint16, ip netip.Addr, index int) error {
	family, bits := unix.AF_INET, 32
	var ifaFlags uint32
	if ip.Is6() {
		family, bits, ifaFlags = unix.AF_INET6, 128, unix.IFA_F_NODAD
	}
	raw := ip.AsSlice()
	// struct ifaddrmsg: family, prefix length, flags, scope, interface index.
	body := make([]byte, unix.SizeofIfAddrmsg)
	body[0], body[1], body[3] = byte(family), byte(bits), unix.RT_SCOPE_UNIVERSE
	binary.NativeEndian.PutUint32(body[4:], uint32(index))
	body = appendAttr(body, unix.IFA_LOCAL, raw)
	body = appendAttr(body, unix.IFA_ADDRESS, raw)
	flagValue := make([]byte, 4)
	binary.NativeEndian.PutUint32(flagValue, ifaFlags)
	body = appendAttr(body, unix.IFA_FLAGS, flagValue)

	msg := make([]byte, unix.NLMSG_HDRLEN, unix.NLMSG_HDRLEN+len(body))
	binary.NativeEndian.PutUint32(msg[0:], uint32(unix.NLMSG_HDRLEN+len(body)))
	binary.NativeEndian.PutUint16(msg[4:], typ)
	binary.NativeEndian.PutUint16(msg[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK|flags)
	binary.NativeEndian.PutUint32(msg[8:], 1)
	msg = append(msg, body...)

	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return err
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m.Header.Type != unix.NLMSG_ERROR || m.Header.Seq != 1 {
				continue
			}
			if len(m.Data) < 4 {
				return errors.New("a short netlink answer")
			}
			// The answer to a request is an error message whose error is 0 on success.
			if errno := int32(binary.NativeEndian.Uint32(m.Data)); errno != 0 {
				return unix.Errno(-errno)
			}
			return nil
		}
	}
}

func appendAttr(b []byte, typ uint16, value []byte) []byte {
	n := unix.SizeofRtAttr + len(value)
	attr := make([]byte, (n+3)&^3)
	binary.NativeEndian.PutUint16(attr[0:], uint16(n))
	binary.NativeEndian.PutUint16(attr[2:], typ)
	copy(attr[unix.SizeofRtAttr:], value)
	return append(b, attr...)
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// arpRequest is a gratuitous ARP request: the sender and the target are the address.
func arpRequest(mac net.HardwareAddr, ip netip.Addr) []byte {
	b := make([]byte, 28)
	binary.BigEndian.PutUint16(b[0:], 1)      // Ethernet
	binary.BigEndian.PutUint16(b[2:], 0x0800) // IPv4
	b[4], b[5] = 6, 4
	binary.BigEndian.PutUint16(b[6:], 1) // request
	copy(b[8:], mac)
	ip4 := ip.As4()
	copy(b[14:], ip4[:])
	copy(b[24:], ip4[:])
	return b
}

func sendARP(ifi *net.Interface, ip netip.Addr) error {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ARP)))
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	to := &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ARP), Ifindex: ifi.Index, Halen: 6}
	copy(to.Addr[:], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	return unix.Sendto(fd, arpRequest(ifi.HardwareAddr, ip), 0, to)
}

// neighbourAdvertisement is an unsolicited neighbour advertisement for the address, with the
// override flag and the interface's Ethernet address. The kernel fills in the checksum.
func neighbourAdvertisement(mac net.HardwareAddr, ip netip.Addr) []byte {
	b := make([]byte, 32)
	b[0] = 136  // neighbour advertisement
	b[4] = 0x20 // override
	ip16 := ip.As16()
	copy(b[8:], ip16[:])
	b[24], b[25] = 2, 1 // target link-layer address, 8 bytes
	copy(b[26:], mac)
	return b
}

func sendNA(ifi *net.Interface, ip netip.Addr) error {
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.IPPROTO_ICMPV6)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// Neighbours accept neighbour discovery only with a hop limit of 255.
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_HOPS, 255); err != nil {
		return err
	}
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_IF, ifi.Index); err != nil {
		return err
	}
	allNodes := &unix.SockaddrInet6{Addr: netip.MustParseAddr("ff02::1").As16(), ZoneId: uint32(ifi.Index)}
	return unix.Sendto(fd, neighbourAdvertisement(ifi.HardwareAddr, ip), 0, allNodes)
}
