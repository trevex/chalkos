package nodeip

import (
	"encoding/binary"
	"net/netip"
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
	up := map[uint32]string{1: "lo", 2: "eth0", 4: "ppp0"}
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

	short := syscall.NetlinkMessage{Header: syscall.NlMsghdr{Type: syscall.RTM_NEWADDR}, Data: []byte{2, 24}}
	if _, err := parseAddresses([]syscall.NetlinkMessage{short}, up); err == nil {
		t.Error("parsed a short message")
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
