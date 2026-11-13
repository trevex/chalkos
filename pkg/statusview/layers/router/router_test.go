package router

import (
	"bytes"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/name, or writes it there with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the output differs from %s; run go test ./pkg/statusview/layers/router -run %s -update and review the diff:\n%s", path, t.Name(), got)
	}
}

// TestWrite writes a router's BGP sessions, its NAT64 instance, its resolvers, its DHCP servers
// and its relays, running or not.
func TestWrite(t *testing.T) {
	var b bytes.Buffer
	err := Write(&b, &routerv1.StatusResponse{
		Peers: []*routerv1.BGPPeerStatus{
			{Name: "fabric", Neighbor: "eth1", State: "Established", UptimeSeconds: 3723, PrefixesReceived: 1, PrefixesSent: 2, Bfd: "up"},
			{Vrf: "tenant", Name: "transit", Neighbor: "203.0.113.254", State: "Active"},
		},
		Nat64:        []*routerv1.NAT64Status{{Name: "edge", Sessions: 3, Bindings: 2, Pools: []*routerv1.NAT64PoolStatus{{Protocol: "tcp", Size: 4535}, {Protocol: "udp", Size: 4535}, {Protocol: "icmp"}}}},
		Resolver:     &routerv1.ResolverStatus{Running: true},
		VrfResolvers: []*routerv1.ResolverStatus{{Vrf: "guest", Error: "unbound@guest.service is inactive"}},
		Dhcp: []*routerv1.DHCPServerStatus{
			{Vrf: "default", Family: "ipv4", Running: true, Subnets: []*routerv1.DHCPSubnetStatus{{Name: "office", Subnet: "192.0.2.0/24", Leased: 2, Pool: 100}}},
			{Vrf: "default", Family: "ipv6", Running: true, Subnets: []*routerv1.DHCPSubnetStatus{{Name: "office6", Subnet: "2001:db8:1::/64", Leased: 1, Pool: math.MaxUint64, Delegated: 1, Delegable: 256}}},
			{Vrf: "guest", Family: "ipv4", Error: "dial unix /run/kea/dhcp4-guest.sock: connect: no such file or directory"},
		},
		DhcpRelays: []*routerv1.DHCPRelayStatus{{Vrf: "default", Running: true}, {Vrf: "guest", Error: "dhcrelay@guest.service is failed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "router.golden", b.String())
}
