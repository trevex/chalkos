package router

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/trevex/chalkos/pkg/internal/netnstest"
)

// The devices of a namespace with their kinds and a VRF's table, the IPv4 addresses of all or one,
// and the routes of one protocol in every table, a route to one address with its length.
func TestLinksAddrsAndRoutesOfNamespace(t *testing.T) {
	if !netnstest.InNamespace(t) {
		return
	}
	for _, args := range [][]string{
		{"link", "set", "lo", "up"},
		{"link", "add", "guest", "type", "vrf", "table", "300"},
		{"link", "add", "n64-edge", "type", "veth", "peer", "name", "nat64"},
		{"link", "add", "eth1", "type", "dummy"},
		{"link", "set", "eth1", "up"},
		{"link", "set", "n64-edge", "up"},
		{"addr", "add", "192.0.2.10/24", "dev", "eth1"},
		{"addr", "add", "100.64.0.1/30", "dev", "n64-edge"},
		{"route", "add", "198.51.100.9/32", "dev", "eth1", "table", "300", "proto", "64"},
		{"route", "add", "198.51.100.0/24", "dev", "eth1", "proto", "64"},
		{"route", "add", "203.0.113.0/24", "dev", "eth1", "proto", "static"},
		{"-6", "route", "add", "unreachable", "64:ff9b::/96", "proto", "64"},
	} {
		netnstest.IP(t, args...)
	}

	links, err := Links()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Link{{Name: "lo"}, {Name: "guest", Kind: "vrf", Table: 300}, {Name: "n64-edge", Kind: "veth"}, {Name: "nat64", Kind: "veth"}, {Name: "eth1", Kind: "dummy"}} {
		if !slices.Contains(links, want) {
			t.Errorf("no link %+v in %+v", want, links)
		}
	}

	all, err := IPv4Addrs("")
	if err != nil {
		t.Fatal(err)
	}
	want := []Addr{
		{Link: "lo", Prefix: netip.MustParsePrefix("127.0.0.1/8")},
		{Link: "n64-edge", Prefix: netip.MustParsePrefix("100.64.0.1/30")},
		{Link: "eth1", Prefix: netip.MustParsePrefix("192.0.2.10/24")},
	}
	if !slices.Equal(all, want) {
		t.Errorf("addresses = %+v, want %+v", all, want)
	}
	if one, err := IPv4Addrs("n64-edge"); err != nil || !slices.Equal(one, want[1:2]) {
		t.Errorf("addresses of n64-edge = %+v, %v", one, err)
	}
	if _, err := IPv4Addrs("missing"); err == nil {
		t.Error("listed the addresses of a missing device")
	}

	v4, err := ProtocolRoutes(netlink.FAMILY_V4, 64)
	if err != nil {
		t.Fatal(err)
	}
	if want := []TableRoute{{300, netip.MustParsePrefix("198.51.100.9/32")}, {254, netip.MustParsePrefix("198.51.100.0/24")}}; !slices.Equal(v4, want) {
		t.Errorf("IPv4 routes = %+v, want %+v", v4, want)
	}
	v6, err := ProtocolRoutes(netlink.FAMILY_V6, 64)
	if err != nil {
		t.Fatal(err)
	}
	if want := []TableRoute{{254, netip.MustParsePrefix("64:ff9b::/96")}}; !slices.Equal(v6, want) {
		t.Errorf("IPv6 routes = %+v, want %+v", v6, want)
	}
}
