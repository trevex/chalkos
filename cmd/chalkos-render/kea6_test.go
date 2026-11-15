package main

import (
	"maps"
	"net/netip"
	"slices"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
)

// An IPv6 subnet renders a DHCPv6 server of its VRF, kea-dhcp6.json or kea-dhcp6-<vrf>.json,
// beside the DHCPv4 servers: the subnet names its link, which the server answers on, its clients
// prefer their addresses for the whole lease, and a server whose subnets delegate prefixes runs
// the hook that records them. The flags of router advertisements read as networkd reads them:
// the last value that parses as a boolean.
func TestKea6PerVRF(t *testing.T) {
	files, err := unitFiles(t, []string{"kea", testPDHook}, derivetest.Kea6Docs(t,
		derivetest.SubnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64", "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}], "leaseTime": "1h"}`),
		derivetest.SubnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64", "prefixDelegation": {"prefix": "2001:db8:100::/40", "length": 56}}`),
		derivetest.SubnetDoc(t, "guests6", `{"interfaces": ["vlan30"], "subnet": "2001:db8:1::/64", "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}]}`),
	))
	if err != nil {
		t.Fatal(err)
	}
	if names := slices.Sorted(maps.Keys(files)); !slices.Equal(names, []string{"kea-dhcp6-guest.json", "kea-dhcp6.json"}) {
		t.Fatalf("files = %v", names)
	}
	def := decodeKea(t, files["kea-dhcp6.json"]).Dhcp6
	if def == nil || !slices.Equal(def.InterfacesConfig.Interfaces, []string{"lan0", "lan1"}) || def.ControlSockets[0].SocketName != "/run/kea/dhcp6.sock" || def.LeaseDatabase.Name != "/var/lib/kea/dhcp6.leases" ||
		len(def.HooksLibraries) != 2 || def.HooksLibraries[1].Library != "libdhcp_run_script.so" || def.HooksLibraries[1].Parameters.Name != testPDHook || len(def.Subnet6) != 2 {
		t.Fatalf("kea-dhcp6.json = %s", files["kea-dhcp6.json"])
	}
	if s := def.Subnet6[0]; s.Interface != "lan0" || s.ValidLifetime != 3600 || s.PreferredLifetime != 3600 || s.ID != subnetID6(netip.MustParsePrefix("2001:db8:1::/64")) {
		t.Errorf("office6 = %+v", s)
	}
	if s := def.Subnet6[1]; s.Interface != "lan1" || len(s.PDPools) != 1 || s.PDPools[0].Prefix != netip.MustParseAddr("2001:db8:100::") || s.PDPools[0].PrefixLen != 40 || s.PDPools[0].DelegatedLen != 56 {
		t.Errorf("cpe = %+v", s)
	}
	guest := decodeKea(t, files["kea-dhcp6-guest.json"]).Dhcp6
	if guest == nil || guest.ControlSockets[0].SocketName != "/run/kea/dhcp6-guest.sock" || guest.LeaseDatabase.Name != "/var/lib/kea/dhcp6-guest.leases" || len(guest.HooksLibraries) != 1 ||
		len(guest.Subnet6) != 1 || guest.Subnet6[0].Interface != "vlan30" {
		t.Errorf("kea-dhcp6-guest.json = %s", files["kea-dhcp6-guest.json"])
	}
}

// Two subnets of one server whose ranges lie apart but whose numbers in Kea are the same, as two
// hashes may be, are refused.
func TestKeaSameNumber(t *testing.T) {
	a := servedSubnet{Subnet: derive.Subnet{Name: "office6", VRF: derive.DefaultVRF, Prefix: netip.MustParsePrefix("2001:db8:1::/64")}, id: 7}
	b := servedSubnet{Subnet: derive.Subnet{Name: "lab6", VRF: derive.DefaultVRF, Prefix: netip.MustParsePrefix("2001:db8:2::/64")}, id: 7}
	want := "DHCPSubnet/lab6: spec.subnet: DHCPSubnet office6 and DHCPSubnet lab6 get the same number 7 in Kea; change one subnet's range"
	if err := numberedApart(a, b); err == nil || err.Error() != want {
		t.Errorf("numberedApart = %v; want %s", err, want)
	}
	b.id = 8
	if err := numberedApart(a, b); err != nil {
		t.Errorf("numberedApart of different numbers = %v", err)
	}
}
