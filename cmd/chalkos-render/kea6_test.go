package main

import (
	"encoding/json"
	"net/netip"
	"slices"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive"
)

// kea6Docs are a LAN router's documents with the DHCPSubnet documents given: lan0 holds
// 2001:db8:1::1/64 and 2001:db8:5::1/64 and sends router advertisements with the Managed flag, lan1 2001:db8:2::1/64
// with the OtherInformation flag, lan2 2001:db8:3::1/64 without router advertisements, lan3
// 2001:db8:4::1/64 with them but without either flag, and vlan30 in the VRF guest
// 2001:db8:1::1/64 with the Managed flag as well.
func kea6Docs(t *testing.T, subnets ...any) map[string][]any {
	t.Helper()
	ra := func(name, link, addresses, flags string) any {
		return document(t, "Network", name, `{"match": {"name": "`+link+`"}, "network": {"address": [`+addresses+`], "ipv6SendRA": "yes"}, "ipv6SendRA": `+flags+`}`)
	}
	return map[string][]any{
		"VRF": {routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["vlan30"]}`)},
		"Network": {
			ra("10-lan0", "lan0", `"2001:db8:1::1/64", "2001:db8:5::1/64"`, `{"managed": true}`),
			ra("11-lan1", "lan1", `"2001:db8:2::1/64"`, `[{"managed": "no"}, {"otherInformation": ["off", " 1 "]}]`),
			document(t, "Network", "12-lan2", `{"match": {"name": "lan2"}, "network": {"address": ["2001:db8:3::1/64"]}, "ipv6SendRA": {"managed": true}}`),
			ra("13-lan3", "lan3", `"2001:db8:4::1/64"`, `{"managed": "maybe"}`),
			ra("30-vlan30", "vlan30", `"2001:db8:1::1/64"`, `{"managed": 1}`),
		},
		"DHCPSubnet": subnets,
	}
}

// An IPv6 subnet renders a DHCPv6 server of its VRF, kea-dhcp6.json or kea-dhcp6-<vrf>.json,
// beside the DHCPv4 servers: the subnet names its link, which the server answers on, its clients
// prefer their addresses for the whole lease, and a server whose subnets delegate prefixes runs
// the hook that records them. The flags of router advertisements read as networkd reads them:
// the last value that parses as a boolean.
func TestKea6PerVRF(t *testing.T) {
	files, err := unitFiles(t, []string{"kea", testPDHook}, kea6Docs(t,
		subnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64", "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}], "leaseTime": "1h"}`),
		subnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64", "prefixDelegation": {"prefix": "2001:db8:100::/40", "length": 56}}`),
		subnetDoc(t, "guests6", `{"interfaces": ["vlan30"], "subnet": "2001:db8:1::/64", "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}]}`),
	))
	if err != nil {
		t.Fatal(err)
	}
	if names := slicesSorted(files); !slices.Equal(names, []string{"kea-dhcp6-guest.json", "kea-dhcp6.json"}) {
		t.Fatalf("files = %v", names)
	}
	type server struct {
		Dhcp6 struct {
			Interfaces struct {
				Interfaces []string `json:"interfaces"`
			} `json:"interfaces-config"`
			Sockets []struct {
				Name string `json:"socket-name"`
			} `json:"control-sockets"`
			Leases struct {
				Name string `json:"name"`
			} `json:"lease-database"`
			Hooks []struct {
				Library    string `json:"library"`
				Parameters struct {
					Name string `json:"name"`
				} `json:"parameters"`
			} `json:"hooks-libraries"`
			Subnets []struct {
				ID        uint32 `json:"id"`
				Subnet    string `json:"subnet"`
				Interface string `json:"interface"`
				Valid     uint64 `json:"valid-lifetime"`
				Preferred uint64 `json:"preferred-lifetime"`
				PD        []struct {
					Prefix    string `json:"prefix"`
					Length    int    `json:"prefix-len"`
					Delegated int    `json:"delegated-len"`
				} `json:"pd-pools"`
			} `json:"subnet6"`
		} `json:"Dhcp6"`
	}
	read := func(file string) server {
		var s server
		if err := json.Unmarshal([]byte(files[file]), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	def := read("kea-dhcp6.json").Dhcp6
	if !slices.Equal(def.Interfaces.Interfaces, []string{"lan0", "lan1"}) || def.Sockets[0].Name != "/run/kea/dhcp6.sock" || def.Leases.Name != "/var/lib/kea/dhcp6.leases" ||
		len(def.Hooks) != 2 || def.Hooks[1].Library != "libdhcp_run_script.so" || def.Hooks[1].Parameters.Name != testPDHook || len(def.Subnets) != 2 {
		t.Fatalf("kea-dhcp6.json = %s", files["kea-dhcp6.json"])
	}
	if s := def.Subnets[0]; s.Interface != "lan0" || s.Valid != 3600 || s.Preferred != 3600 || s.ID != subnetID6(mustPrefix("2001:db8:1::/64")) {
		t.Errorf("office6 = %+v", s)
	}
	if s := def.Subnets[1]; s.Interface != "lan1" || len(s.PD) != 1 || s.PD[0].Prefix != "2001:db8:100::" || s.PD[0].Length != 40 || s.PD[0].Delegated != 56 {
		t.Errorf("cpe = %+v", s)
	}
	guest := read("kea-dhcp6-guest.json").Dhcp6
	if guest.Sockets[0].Name != "/run/kea/dhcp6-guest.sock" || guest.Leases.Name != "/var/lib/kea/dhcp6-guest.leases" || len(guest.Hooks) != 1 ||
		len(guest.Subnets) != 1 || guest.Subnets[0].Interface != "vlan30" {
		t.Errorf("kea-dhcp6-guest.json = %s", files["kea-dhcp6-guest.json"])
	}
}

// mustPrefix parses a range of a test.
func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// Kea numbers subnets from 1 to 4294967294, so a hash of 0 or 4294967295 takes the nearest.
func TestKeaSubnetID(t *testing.T) {
	for sum, want := range map[uint32]uint32{0: 1, 1: 1, 3221225984: 3221225984, 4294967294: 4294967294, 4294967295: 4294967294} {
		if got := keaSubnetID(sum); got != want {
			t.Errorf("keaSubnetID(%d) = %d; want %d", sum, got, want)
		}
	}
}

// Two subnets of one server whose ranges lie apart but whose numbers in Kea are the same, as two
// hashes may be, are refused.
func TestKeaSameNumber(t *testing.T) {
	a := servedSubnet{Subnet: derive.Subnet{Name: "office6", VRF: derive.DefaultVRF, Prefix: mustPrefix("2001:db8:1::/64")}, id: 7}
	b := servedSubnet{Subnet: derive.Subnet{Name: "lab6", VRF: derive.DefaultVRF, Prefix: mustPrefix("2001:db8:2::/64")}, id: 7}
	want := "DHCPSubnet/lab6: spec.subnet: DHCPSubnet office6 and DHCPSubnet lab6 get the same number 7 in Kea; change one subnet's range"
	if err := numberedApart(a, b); err == nil || err.Error() != want {
		t.Errorf("numberedApart = %v; want %s", err, want)
	}
	b.id = 8
	if err := numberedApart(a, b); err != nil {
		t.Errorf("numberedApart of different numbers = %v", err)
	}
}
