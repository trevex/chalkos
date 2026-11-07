package nodeip

import (
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/internal/netnstest"
)

func TestUsable(t *testing.T) {
	for _, c := range []struct {
		ipv6  bool
		flags uint32
		want  bool
	}{
		{false, 0, true},
		{true, unix.IFA_F_PERMANENT, true},
		{true, unix.IFA_F_NODAD, true},
		{true, unix.IFA_F_TENTATIVE, false},
		{true, unix.IFA_F_DADFAILED, false},
		{true, unix.IFA_F_DEPRECATED, false},
		{false, unix.IFA_F_DEPRECATED, false},
		{true, unix.IFA_F_TEMPORARY, false},
		// A secondary IPv4 address carries the bit IPv6 uses for temporary addresses.
		{false, unix.IFA_F_SECONDARY, true},
	} {
		if got := usable(c.ipv6, c.flags); got != c.want {
			t.Errorf("usable(%v, %#x) = %v, want %v", c.ipv6, c.flags, got, c.want)
		}
	}
}

// The system's addresses leave out those of interfaces that are down and those that are
// tentative or deprecated, keep secondary IPv4 addresses, and give the node's own end of a
// point-to-point address.
func TestSystemAddressesOfNamespace(t *testing.T) {
	if !netnstest.InNamespace(t) {
		return
	}
	for _, args := range [][]string{
		{"link", "set", "lo", "up"},
		{"link", "add", "eth0", "type", "dummy"},
		{"link", "set", "eth0", "addrgenmode", "none"},
		{"addr", "add", "10.0.2.15/24", "dev", "eth0"},
		{"addr", "add", "10.0.2.16/24", "dev", "eth0"},
		{"addr", "add", "10.0.2.17/32", "peer", "203.0.113.1/32", "dev", "eth0"},
		{"addr", "add", "2001:db8::15/64", "dev", "eth0", "nodad"},
		{"addr", "add", "2001:db8::18/64", "dev", "eth0", "nodad", "preferred_lft", "0"},
		{"link", "set", "eth0", "up"},
		{"link", "add", "down0", "type", "dummy"},
		{"addr", "add", "192.168.1.5/24", "dev", "down0"},
		// Without a carrier, duplicate address detection waits, and the address stays tentative.
		{"link", "add", "v0", "type", "veth", "peer", "name", "v1"},
		{"link", "set", "v0", "up"},
		{"addr", "add", "2001:db8:1::16/64", "dev", "v0"},
	} {
		netnstest.IP(t, args...)
	}
	addrs, err := SystemAddresses()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range addrs {
		got = append(got, a.String())
	}
	want := []string{"127.0.0.1 on lo", "::1 on lo", "10.0.2.15 on eth0", "10.0.2.16 on eth0", "10.0.2.17 on eth0", "2001:db8::15 on eth0"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("addresses %v, want %v", got, want)
	}
}

// The links of the system's addresses say which interface is the loopback one and which is a
// dummy, with its MTU.
func TestSystemAddressLinks(t *testing.T) {
	if !netnstest.InNamespace(t) {
		return
	}
	for _, args := range [][]string{
		{"link", "set", "lo", "up"},
		{"link", "add", "bgp0", "mtu", "1450", "type", "dummy"},
		{"addr", "add", "192.168.200.12/32", "dev", "bgp0"},
		{"addr", "add", "fd00:200::12/128", "dev", "bgp0", "nodad"},
		{"link", "set", "bgp0", "up"},
	} {
		netnstest.IP(t, args...)
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
