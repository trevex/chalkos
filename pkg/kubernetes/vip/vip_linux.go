package vip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
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
// node that held it before, and is deprecated, so the node never uses it as the source address of
// its own connections. added is false when the interface had the address already; its settings
// are renewed then.
func Add(a Address) (added bool, err error) {
	ifi, err := net.InterfaceByName(a.Interface)
	if err != nil {
		return false, fmt.Errorf("add %s: %w", a, err)
	}
	addr := hostAddr(a.IP, ifi.Index)
	if a.IP.Is6() {
		// Deprecated but valid: the address answers, yet source address selection skips it, so the
		// holder's own connections never depend on an address that moves to another node.
		addr.Flags = unix.IFA_F_NODAD
		addr.PreferedLft, addr.ValidLft = 0, infiniteLifetime
	}
	err = netlink.AddrAdd(nil, addr)
	if errors.Is(err, unix.EEXIST) {
		if err := netlink.AddrReplace(nil, addr); err != nil {
			return false, fmt.Errorf("add %s: %w", a, err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("add %s: %w", a, err)
	}
	return true, nil
}

// Remove removes the address from its interface. An address that is not there, also on an
// interface that is gone, is no error.
func Remove(a Address) error {
	ifi, err := net.InterfaceByName(a.Interface)
	if err != nil {
		return nil
	}
	err = netlink.AddrDel(nil, hostAddr(a.IP, ifi.Index))
	if errors.Is(err, unix.EADDRNOTAVAIL) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove %s: %w", a, err)
	}
	return nil
}

// hostAddr is ip as a host address, /32 or /128, on the interface with the index.
func hostAddr(ip netip.Addr, index int) *netlink.Addr {
	bits := ip.BitLen()
	return &netlink.Addr{IPNet: &net.IPNet{IP: ip.AsSlice(), Mask: net.CIDRMask(bits, bits)}, LinkIndex: index}
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

// infiniteLifetime is the lifetime of an address that never expires.
const infiniteLifetime = 0xffffffff
