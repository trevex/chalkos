package vip

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// inNamespace reports whether the test runs in its own user and network namespace, where it may
// add addresses and send raw frames. Called outside one, it runs the test again in a new one and
// returns false.
func inNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv("CHALKOS_TEST_NETNS") == "1" {
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "CHALKOS_TEST_NETNS=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	t.Logf("in a new network namespace:\n%s", out)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return false
}

func ip(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		t.Fatalf("ip %v: %v\n%s", args, err, out)
	}
}

// capture receives the frames that reach the interface.
func capture(t *testing.T, iface string) int {
	t.Helper()
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL), Ifindex: ifi.Index}); err != nil {
		t.Fatal(err)
	}
	tv := unix.NsecToTimeval((5 * time.Second).Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		t.Fatal(err)
	}
	return fd
}

// receive returns the first frame that match accepts.
func receive(t *testing.T, fd int, what string, match func(frame []byte) bool) []byte {
	t.Helper()
	buf := make([]byte, 2048)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if match(buf[:n]) {
			return slices.Clone(buf[:n])
		}
	}
}

func holds(t *testing.T, iface string, addr netip.Addr) bool {
	t.Helper()
	addrs, err := nodeip.SystemAddresses()
	if err != nil {
		t.Fatal(err)
	}
	return slices.Contains(addrs, nodeip.Address{Interface: iface, IP: addr})
}

// The address is added as a host address, announced to the neighbours and removed again.
func TestAddAnnounceRemove(t *testing.T) {
	if !inNamespace(t) {
		return
	}
	ip(t, "link", "add", "v0", "type", "veth", "peer", "name", "v1")
	ip(t, "link", "set", "v0", "up")
	ip(t, "link", "set", "v1", "up")
	// The neighbour advertisement goes out from a link-local address.
	ip(t, "-6", "addr", "add", "fe80::1/64", "dev", "v0", "nodad")
	v0, err := net.InterfaceByName("v0")
	if err != nil {
		t.Fatal(err)
	}
	fd := capture(t, "v1")

	v4 := Address{IP: netip.MustParseAddr("10.9.0.10"), Interface: "v0"}
	v6 := Address{IP: netip.MustParseAddr("fd00::10"), Interface: "v0"}
	for _, a := range []Address{v4, v6} {
		if err := Add(a); err != nil {
			t.Fatal(err)
		}
		// Adding it again changes nothing.
		if err := Add(a); err != nil {
			t.Fatal(err)
		}
		if a.IP.Is4() {
			if !holds(t, "v0", a.IP) {
				t.Errorf("v0 does not hold %s", a.IP)
			}
			continue
		}
		// Usable at once, without waiting for duplicate address detection, and deprecated, so the
		// holder's own connections never pick it as their source address.
		flags, cache, found := addrInfo(t, "v0", a.IP)
		if !found {
			t.Fatalf("v0 does not hold %s", a.IP)
		}
		if flags&unix.IFA_F_TENTATIVE != 0 || flags&unix.IFA_F_NODAD == 0 {
			t.Errorf("%s has flags %#x, want it without duplicate address detection", a.IP, flags)
		}
		if flags&unix.IFA_F_DEPRECATED == 0 || cache.Prefered != 0 || cache.Valid != infiniteLifetime {
			t.Errorf("%s has flags %#x, preferred lifetime %d and valid lifetime %d; want it deprecated and valid forever",
				a.IP, flags, cache.Prefered, cache.Valid)
		}
	}
	out, err := exec.Command("ip", "-o", "addr", "show", "dev", "v0").CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("10.9.0.10/32")) || !bytes.Contains(out, []byte("fd00::10/128")) {
		t.Errorf("addresses of v0, want host addresses: %v\n%s", err, out)
	}

	if err := Announce(v4); err != nil {
		t.Fatal(err)
	}
	arp := receive(t, fd, "the gratuitous ARP request", func(f []byte) bool {
		return len(f) >= 42 && binary.BigEndian.Uint16(f[12:]) == unix.ETH_P_ARP
	})
	broadcast := net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	payload := arp[14:42]
	if !bytes.Equal(arp[0:6], broadcast) || !bytes.Equal(arp[6:12], v0.HardwareAddr) ||
		binary.BigEndian.Uint16(payload[6:]) != 1 || !bytes.Equal(payload[8:14], v0.HardwareAddr) ||
		!bytes.Equal(payload[14:18], []byte{10, 9, 0, 10}) || !bytes.Equal(payload[24:28], []byte{10, 9, 0, 10}) {
		t.Errorf("ARP frame % x", arp)
	}

	if err := Announce(v6); err != nil {
		t.Fatal(err)
	}
	na := receive(t, fd, "the neighbour advertisement", func(f []byte) bool {
		// Ethernet, IPv6 with ICMPv6, a neighbour advertisement.
		return len(f) >= 14+40+32 && binary.BigEndian.Uint16(f[12:]) == unix.ETH_P_IPV6 && f[14+6] == unix.IPPROTO_ICMPV6 && f[14+40] == 136
	})
	icmp := na[14+40:]
	target := v6.IP.As16()
	if !bytes.Equal(na[0:6], []byte{0x33, 0x33, 0, 0, 0, 1}) || na[14+7] != 255 ||
		icmp[4] != 0x20 || !bytes.Equal(icmp[8:24], target[:]) || icmp[24] != 2 || !bytes.Equal(icmp[26:32], v0.HardwareAddr) {
		t.Errorf("neighbour advertisement % x", na)
	}

	for _, a := range []Address{v4, v6} {
		if err := Remove(a); err != nil {
			t.Fatal(err)
		}
		if _, _, found := addrInfo(t, "v0", a.IP); found {
			t.Errorf("v0 still holds %s", a.IP)
		}
		// Removing it again, or from an interface that is gone, is no error.
		if err := Remove(a); err != nil {
			t.Errorf("removing %s again: %v", a.IP, err)
		}
	}
	if err := Remove(Address{IP: v4.IP, Interface: "gone0"}); err != nil {
		t.Errorf("removing from a missing interface: %v", err)
	}
	if err := Add(Address{IP: v4.IP, Interface: "gone0"}); err == nil {
		t.Error("added an address to a missing interface")
	}
}

// addrInfo reads the flags and lifetimes of the address on the interface; found is false when
// the interface does not have it.
func addrInfo(t *testing.T, iface string, addr netip.Addr) (flags uint32, cache unix.IfaCacheinfo, found bool) {
	t.Helper()
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETADDR, syscall.AF_UNSPEC)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWADDR || len(m.Data) < syscall.SizeofIfAddrmsg ||
			binary.NativeEndian.Uint32(m.Data[4:8]) != uint32(ifi.Index) {
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			t.Fatal(err)
		}
		flags, cache, found = uint32(m.Data[2]), unix.IfaCacheinfo{}, false
		for _, a := range attrs {
			switch a.Attr.Type {
			case unix.IFA_LOCAL, unix.IFA_ADDRESS:
				if ip, ok := netip.AddrFromSlice(a.Value); ok && ip == addr {
					found = true
				}
			case unix.IFA_FLAGS:
				flags = binary.NativeEndian.Uint32(a.Value)
			case unix.IFA_CACHEINFO:
				cache.Prefered = binary.NativeEndian.Uint32(a.Value[0:])
				cache.Valid = binary.NativeEndian.Uint32(a.Value[4:])
			}
		}
		if found {
			return flags, cache, true
		}
	}
	return 0, unix.IfaCacheinfo{}, false
}
