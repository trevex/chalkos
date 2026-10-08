package nodeip

import (
	"encoding/binary"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
)

type rtattr struct {
	typ   uint16
	value []byte
}

// addrMessage builds an RTM_NEWADDR message as the kernel sends it.
func addrMessage(family, flags uint8, index uint32, attrs ...rtattr) syscall.NetlinkMessage {
	data := make([]byte, syscall.SizeofIfAddrmsg)
	data[0], data[1], data[2] = family, 24, flags
	binary.NativeEndian.PutUint32(data[4:], index)
	for _, a := range attrs {
		n := syscall.SizeofRtAttr + len(a.value)
		b := make([]byte, (n+3)&^3)
		binary.NativeEndian.PutUint16(b[0:], uint16(n))
		binary.NativeEndian.PutUint16(b[2:], a.typ)
		copy(b[syscall.SizeofRtAttr:], a.value)
		data = append(data, b...)
	}
	return syscall.NetlinkMessage{
		Header: syscall.NlMsghdr{Len: uint32(syscall.NLMSG_HDRLEN + len(data)), Type: syscall.RTM_NEWADDR},
		Data:   data,
	}
}

func ip(s string) []byte { return netip.MustParseAddr(s).AsSlice() }

func extendedFlags(flags uint32) rtattr {
	v := make([]byte, 4)
	binary.NativeEndian.PutUint32(v, flags)
	return rtattr{ifaFlags, v}
}

func TestParseAddresses(t *testing.T) {
	up := map[uint32]Link{1: {Name: "lo", Loopback: true}, 2: {Name: "eth0"}, 4: {Name: "ppp0"}}
	msgs := []syscall.NetlinkMessage{
		addrMessage(syscall.AF_INET, 0, 1, rtattr{syscall.IFA_ADDRESS, ip("127.0.0.1")}, rtattr{syscall.IFA_LOCAL, ip("127.0.0.1")}),
		addrMessage(syscall.AF_INET, 0, 2, rtattr{syscall.IFA_ADDRESS, ip("10.0.2.15")}, rtattr{syscall.IFA_LOCAL, ip("10.0.2.15")}),
		// A secondary IPv4 address carries the bit IPv6 uses for temporary addresses.
		addrMessage(syscall.AF_INET, syscall.IFA_F_SECONDARY, 2, rtattr{syscall.IFA_ADDRESS, ip("10.0.2.16")}, rtattr{syscall.IFA_LOCAL, ip("10.0.2.16")}),
		// The interface is down.
		addrMessage(syscall.AF_INET, 0, 3, rtattr{syscall.IFA_ADDRESS, ip("192.168.1.5")}, rtattr{syscall.IFA_LOCAL, ip("192.168.1.5")}),
		// Point to point: IFA_ADDRESS is the peer.
		addrMessage(syscall.AF_INET, 0, 4, rtattr{syscall.IFA_ADDRESS, ip("203.0.113.1")}, rtattr{syscall.IFA_LOCAL, ip("203.0.113.2")}),
		addrMessage(syscall.AF_INET6, syscall.IFA_F_PERMANENT, 2, rtattr{syscall.IFA_ADDRESS, ip("2001:db8::15")}),
		addrMessage(syscall.AF_INET6, syscall.IFA_F_TENTATIVE, 2, rtattr{syscall.IFA_ADDRESS, ip("2001:db8::16")}),
		addrMessage(syscall.AF_INET6, syscall.IFA_F_DADFAILED, 2, rtattr{syscall.IFA_ADDRESS, ip("2001:db8::17")}),
		addrMessage(syscall.AF_INET6, syscall.IFA_F_DEPRECATED, 2, rtattr{syscall.IFA_ADDRESS, ip("2001:db8::18")}),
		addrMessage(syscall.AF_INET6, syscall.IFA_F_TEMPORARY, 2, rtattr{syscall.IFA_ADDRESS, ip("2001:db8::19")}),
		// IFA_FLAGS holds all flags and overrides the header's.
		addrMessage(syscall.AF_INET6, 0, 2, rtattr{syscall.IFA_ADDRESS, ip("2001:db8::20")}, extendedFlags(syscall.IFA_F_TENTATIVE)),
		addrMessage(syscall.AF_INET6, syscall.IFA_F_TENTATIVE, 2, rtattr{syscall.IFA_ADDRESS, ip("2001:db8::21")}, extendedFlags(syscall.IFA_F_PERMANENT)),
		{Header: syscall.NlMsghdr{Type: syscall.NLMSG_DONE}},
		addrMessage(syscall.AF_INET, 0, 2, rtattr{syscall.IFA_ADDRESS, ip("10.0.2.99")}),
	}
	addrs, err := parseAddresses(msgs, up)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range addrs {
		got = append(got, a.String())
	}
	want := []string{"127.0.0.1 on lo", "10.0.2.15 on eth0", "10.0.2.16 on eth0", "203.0.113.2 on ppp0", "2001:db8::15 on eth0", "2001:db8::21 on eth0"}
	if !slices.Equal(got, want) {
		t.Errorf("addresses %v, want %v", got, want)
	}
	if !addrs[0].Link.Loopback || addrs[1].Link.Loopback {
		t.Errorf("links %+v, %+v", addrs[0].Link, addrs[1].Link)
	}

	short := syscall.NetlinkMessage{Header: syscall.NlMsghdr{Type: syscall.RTM_NEWADDR}, Data: []byte{2, 24}}
	if _, err := parseAddresses([]syscall.NetlinkMessage{short}, up); err == nil {
		t.Error("parsed a short message")
	}
}

// linkMessage builds an RTM_NEWLINK message whose IFLA_LINKINFO names the kind, if any.
func linkMessage(index uint32, kind string) syscall.NetlinkMessage {
	data := make([]byte, syscall.SizeofIfInfomsg)
	binary.NativeEndian.PutUint32(data[4:], index)
	attr := func(typ uint16, value []byte) []byte {
		n := syscall.SizeofRtAttr + len(value)
		b := make([]byte, (n+3)&^3)
		binary.NativeEndian.PutUint16(b[0:], uint16(n))
		binary.NativeEndian.PutUint16(b[2:], typ)
		copy(b[syscall.SizeofRtAttr:], value)
		return b
	}
	data = append(data, attr(syscall.IFLA_IFNAME, []byte("x\x00"))...)
	if kind != "" {
		// IFLA_INFO_DATA follows the kind, as for a vlan.
		data = append(data, attr(syscall.IFLA_LINKINFO, append(attr(iflaInfoKind, []byte(kind+"\x00")), attr(2, []byte{1, 0, 0, 0})...))...)
	}
	return syscall.NetlinkMessage{
		Header: syscall.NlMsghdr{Len: uint32(syscall.NLMSG_HDRLEN + len(data)), Type: syscall.RTM_NEWLINK},
		Data:   data,
	}
}

func TestParseLinkKinds(t *testing.T) {
	kinds, err := parseLinkKinds([]syscall.NetlinkMessage{linkMessage(1, ""), linkMessage(2, "dummy"), linkMessage(3, "vlan")})
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kinds[2] != "dummy" || kinds[3] != "vlan" {
		t.Errorf("kinds %v", kinds)
	}
	broken := linkMessage(2, "dummy")
	binary.NativeEndian.PutUint16(broken.Data[syscall.SizeofIfInfomsg+8+syscall.SizeofRtAttr:], 200)
	if _, err := parseLinkKinds([]syscall.NetlinkMessage{broken}); err == nil {
		t.Error("parsed a malformed link info attribute")
	}
}

// inNamespace reports whether the test runs in its own user and network namespace, where it may
// add interfaces. Called outside one, it runs the test again in a new one and returns false.
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

// The links of the system's addresses say which interface is the loopback one and which is a
// dummy, with its MTU.
func TestSystemAddressLinks(t *testing.T) {
	if !inNamespace(t) {
		return
	}
	for _, args := range [][]string{
		{"link", "set", "lo", "up"},
		{"link", "add", "bgp0", "mtu", "1450", "type", "dummy"},
		{"addr", "add", "192.168.200.12/32", "dev", "bgp0"},
		{"addr", "add", "fd00:200::12/128", "dev", "bgp0", "nodad"},
		{"link", "set", "bgp0", "up"},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v\n%s", args, err, out)
		}
	}
	addrs, err := SystemAddresses()
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]Link{}
	for _, a := range addrs {
		links[a.IP.String()] = a.Link
	}
	for ip, want := range map[string]Link{
		"127.0.0.1":      {Name: "lo", Loopback: true, MTU: 65536},
		"192.168.200.12": {Name: "bgp0", Kind: "dummy", MTU: 1450},
		"fd00:200::12":   {Name: "bgp0", Kind: "dummy", MTU: 1450},
	} {
		if links[ip] != want {
			t.Errorf("link of %s %+v, want %+v", ip, links[ip], want)
		}
	}
}

func TestSystemAddresses(t *testing.T) {
	addrs, err := SystemAddresses()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if a.Interface == "" || !a.IP.IsValid() {
			t.Errorf("address %+v", a)
		}
	}
}
