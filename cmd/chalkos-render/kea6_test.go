package main

import (
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
	"testing"
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

// The renderer refuses an IPv6 subnet of more than 120 bits or of link-local addresses, on two
// links, with a gateway or boot files, an IPv4 DNS server, a pool or reservation outside the
// subnet, at the subnet routers' anycast address or of the router's own, a pool backwards,
// delegated prefixes of the subnet, of the router's address on a link of the VRF or of a length
// no longer than their range, an IPv4 subnet that delegates prefixes, two IPv6 subnets of one
// link or that overlap, the subnet of one and the delegated prefixes of another, and links whose
// Network sends no router advertisements with the flag that tells clients to ask: Managed for
// addresses, Managed or OtherInformation for delegated prefixes and options alone.
func TestKea6Refusals(t *testing.T) {
	office := func(more string) any {
		return subnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64"`+more+`}`)
	}
	pool := `, "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}]`
	for name, c := range map[string]struct {
		subnets []any
		want    string
	}{
		"121 bits":                    {[]any{subnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/121"}`)}, "DHCPSubnet office6: the subnet 2001:db8:1::/121 is no IPv6 range of 120 bits at most outside ::/8, fe80::/10 and ff00::/8"},
		"link-local":                  {[]any{subnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "fe80::/64"}`)}, "the subnet fe80::/64 is no IPv6 range of 120 bits at most"},
		"two links":                   {[]any{subnetDoc(t, "office6", `{"interfaces": ["lan0", "lan1"], "subnet": "2001:db8:1::/64"}`)}, "an IPv6 subnet is served on one link, the one Kea's subnet names; give each of lan0, lan1 a DHCPSubnet of its own"},
		"a gateway":                   {[]any{office(`, "gateway": "2001:db8:1::1"`)}, "gateway is for IPv4 subnets; IPv6 clients learn their routers from router advertisements"},
		"boot files":                  {[]any{office(`, "boot": {"server": null, "bios": null, "uefi": null, "ipxe": "http://192.0.2.5/boot.ipxe"}`)}, "boot is for IPv4 subnets"},
		"an IPv4 DNS server":          {[]any{office(`, "dns": ["192.0.2.53"]`)}, `the DNS server "192.0.2.53" is no IPv6 address`},
		"a pool outside":              {[]any{office(`, "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:2::1"}]`)}, `the pool's last address, "2001:db8:2::1", is no address of the subnet 2001:db8:1::/64`},
		"the anycast address":         {[]any{office(`, "pools": [{"first": "2001:db8:1::", "last": "2001:db8:1::ff"}]`)}, "the pool's first address, 2001:db8:1::, is the subnet routers' anycast address of the subnet 2001:db8:1::/64"},
		"the router's address":        {[]any{office(`, "pools": [{"first": "2001:db8:1::1", "last": "2001:db8:1::ff"}]`)}, "the pool's first address, 2001:db8:1::1, is the router's own address on the subnet's link"},
		"a pool backwards":            {[]any{office(`, "pools": [{"first": "2001:db8:1::1ff", "last": "2001:db8:1::100"}]`)}, "the pool 2001:db8:1::1ff-2001:db8:1::100 ends before it starts"},
		"a reservation in a pool":     {[]any{office(pool + `, "reservations": [{"mac": "52:54:00:12:34:56", "address": "2001:db8:1::120", "hostname": null}]`)}, "the reservation of 52:54:00:12:34:56, 2001:db8:1::120, lies in the pool 2001:db8:1::100-2001:db8:1::1ff"},
		"delegating the subnet":       {[]any{office(`, "prefixDelegation": {"prefix": "2001:db8:1::/48", "length": 64}`)}, "prefixDelegation's prefix 2001:db8:1::/48 overlaps the subnet 2001:db8:1::/64"},
		"delegating short prefixes":   {[]any{office(`, "prefixDelegation": {"prefix": "2001:db8:100::/56", "length": 56}`)}, "prefixDelegation's length 56 is not longer than its prefix 2001:db8:100::/56, nor 128 at most"},
		"delegating IPv4":             {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "prefixDelegation": {"prefix": "2001:db8:100::/56", "length": 64}}`)}, "DHCPSubnet office: prefixDelegation delegates IPv6 prefixes, which an IPv4 subnet has none of"},
		"two subnets of a link":       {[]any{office(""), subnetDoc(t, "other", `{"interfaces": ["lan0"], "subnet": "2001:db8:5::/64"}`)}, "DHCPSubnet office6 and DHCPSubnet other both serve lan0; a link takes one subnet of each family"},
		"overlapping subnets":         {[]any{office(""), subnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8::/32", "prefixDelegation": {"prefix": "2001:db9::/48", "length": 56}}`)}, "DHCPSubnet office6 and DHCPSubnet cpe overlap in VRF default: 2001:db8:1::/64 and 2001:db8::/32"},
		"delegating another's":        {[]any{office(""), subnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64", "prefixDelegation": {"prefix": "2001:db8:1:0:8000::/65", "length": 72}}`)}, "DHCPSubnet office6 and DHCPSubnet cpe overlap in VRF default: 2001:db8:1::/64 and 2001:db8:1:0:8000::/65"},
		"delegating a link's address": {[]any{subnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64", "prefixDelegation": {"prefix": "2001:db8:4::/48", "length": 56}}`)}, "DHCPSubnet cpe: prefixDelegation's prefix 2001:db8:4::/48 holds 2001:db8:4::1, the address Network 13-lan3 gives a link of VRF default"},
		"no advertisements":           {[]any{subnetDoc(t, "lab", `{"interfaces": ["lan2"], "subnet": "2001:db8:3::/64"`+strings.ReplaceAll(pool, ":1::", ":3::")+`}`)}, "Network 12-lan2 sends no router advertisements on lan2 with Managed=yes, which tell its clients to ask the DHCPv6 server for addresses; give it ipv6SendRA: true in network and managed: true in ipv6SendRA"},
		"addresses without Managed":   {[]any{subnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64"`+strings.ReplaceAll(pool, ":1::", ":2::")+`}`)}, "Network 11-lan1 sends no router advertisements on lan1 with Managed=yes"},
		"options without a flag":      {[]any{subnetDoc(t, "lab", `{"interfaces": ["lan3"], "subnet": "2001:db8:4::/64", "dns": ["2001:db8:4::1"]}`)}, "Network 13-lan3 sends no router advertisements on lan3 with Managed=yes or OtherInformation=yes, which tell its clients to ask the DHCPv6 server; give it ipv6SendRA: true in network and otherInformation: true in ipv6SendRA"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"kea", testPDHook}, kea6Docs(t, c.subnets...), c.want) })
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

// systemd reads a unit's booleans in any case, with spaces around them, as numbers or words, and
// ignores a value that is none.
func TestSystemdBool(t *testing.T) {
	for v, want := range map[any][2]bool{
		true: {true, true}, "Yes": {true, true}, " on\t": {true, true}, "1": {true, true}, "t": {true, true},
		false: {false, true}, "OFF": {false, true}, "0": {false, true}, "n": {false, true},
		"maybe": {false, false}, "": {false, false}, nil: {false, false}, 2.0: {false, false},
	} {
		if b, ok := systemdBool(v); b != want[0] || ok != want[1] {
			t.Errorf("systemdBool(%#v) = %v, %v; want %v, %v", v, b, ok, want[0], want[1])
		}
	}
	if b, ok := systemdBool(json.Number("1")); !b || !ok {
		t.Error("the number 1 is no true")
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

// networkd reads router advertisements under their former names as well, IPv6PrefixDelegation=
// in [Network] and the section [IPv6PrefixDelegation], and the unit lists them before the
// current names, whose valid values win.
func TestKea6FormerAdvertisementNames(t *testing.T) {
	office := subnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64", "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}]}`)
	for name, c := range map[string]struct {
		network string
		served  bool
	}{
		"former names":                   {`"ipv6PrefixDelegation": "yes"}, "ipv6PrefixDelegation": {"managed": true}`, true},
		"former and current names":       {`"ipv6PrefixDelegation": true}, "ipv6SendRA": {"managed": true}`, true},
		"current names win":              {`"ipv6PrefixDelegation": true, "ipv6SendRA": false}, "ipv6PrefixDelegation": {"managed": true}`, false},
		"current flags win":              {`"ipv6SendRA": true}, "ipv6PrefixDelegation": {"managed": true}, "ipv6SendRA": {"managed": "no"}`, false},
		"invalid current values ignored": {`"ipv6PrefixDelegation": true, "ipv6SendRA": "maybe"}, "ipv6PrefixDelegation": {"managed": true}, "ipv6SendRA": {"managed": ""}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			docs := map[string][]any{
				"Network":    {document(t, "Network", "10-lan0", `{"match": {"name": "lan0"}, "network": {"address": ["2001:db8:1::1/64"], `+c.network+`}`)},
				"DHCPSubnet": {office},
			}
			if c.served {
				if _, err := unitFiles(t, []string{"kea", testPDHook}, docs); err != nil {
					t.Fatal(err)
				}
				return
			}
			refused(t, []string{"kea", testPDHook}, docs, "Network 10-lan0 sends no router advertisements on lan0 with Managed=yes")
		})
	}
}

// A delegated range may hold the router's addresses on links of other VRFs.
func TestKea6DelegatesAddressesOfOtherVRFs(t *testing.T) {
	if _, err := unitFiles(t, []string{"kea", testPDHook}, kea6Docs(t,
		subnetDoc(t, "guests6", `{"interfaces": ["vlan30"], "subnet": "2001:db8:1::/64", "prefixDelegation": {"prefix": "2001:db8:4::/48", "length": 56}}`),
	)); err != nil {
		t.Fatal(err)
	}
}
