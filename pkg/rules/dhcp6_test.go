package rules

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// The DHCPSubnet rules take an IPv6 subnet on one link with pools, a reservation, DNS servers, a
// domain and delegated prefixes, and one of delegated prefixes alone, and refuse an IPv6 subnet of
// more than 120 bits or over link-local addresses, one on two links, an IPv6 subnet's gateway and
// boot files, an IPv4 or IPv4-mapped DNS server of an IPv6 subnet, a pool or reservation of the
// subnet-router anycast address, delegated prefixes of an IPv4 subnet, of an IPv4 range, of
// ::/8, fe80::/10 or ff00::/8, of a length no longer than their range, and of a range that
// overlaps the subnet.
func TestDHCPSubnetIPv6Rules(t *testing.T) {
	set, doc := routerRules(t)
	subnet := func(name, spec string) config.Document { return doc("DHCPSubnet", name, spec) }
	office6 := func(more string) []config.Document {
		return []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64"`+more+`}`)}
	}
	delegate := func(prefix string, length int) []config.Document {
		return office6(fmt.Sprintf(`, "prefixDelegation": {"prefix": %q, "length": %d}`, prefix, length))
	}
	const at = `DHCPSubnet "office6" spec: `
	notDelegable := func(prefix string, length int) []string {
		return []string{fmt.Sprintf(`DHCPSubnet "office6" spec.prefixDelegation: prefixDelegation's prefix %s is no IPv6 range shorter than %d bits apart from ::/8, fe80::/10 and ff00::/8`, prefix, length)}
	}
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"an office", office6(`, "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}], "dns": ["2001:db8:1::1"], "domain": "lan.example.org",
			"reservations": [{"mac": "52:54:00:12:34:56", "address": "2001:db8:1::10"}], "prefixDelegation": {"prefix": "2001:db8:100::/56", "length": 64}`), nil},
		{"delegated prefixes alone", []config.Document{subnet("cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64", "prefixDelegation": {"prefix": "2001:db8:200::/48", "length": 56}}`)}, nil},
		{"an IPv6 subnet of 121 bits", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/121"}`)},
			[]string{`DHCPSubnet "office6" spec.subnet: the subnet 2001:db8:1::/121 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases`}},
		{"a link-local subnet", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "fe80::/64"}`)},
			[]string{`DHCPSubnet "office6" spec.subnet: the subnet fe80::/64 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases`}},
		// fe00::/7 starts outside fe80::/10 and ff00::/8 and holds both.
		{"a subnet over the link-local addresses", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "fe00::/7"}`)},
			[]string{`DHCPSubnet "office6" spec.subnet: the subnet fe00::/7 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases`}},
		{"an IPv4-mapped subnet", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "::ffff:192.0.2.0/120"}`)},
			[]string{`DHCPSubnet "office6" spec.subnet: the subnet ::ffff:192.0.2.0/120 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases`}},
		{"two links", []config.Document{subnet("office6", `{"interfaces": ["lan0", "lan1"], "subnet": "2001:db8:1::/64"}`)},
			[]string{at + "an IPv6 subnet is served on one link, the one Kea's subnet names; give each of lan0, lan1 a DHCPSubnet of its own"}},
		{"a gateway", office6(`, "gateway": "2001:db8:1::1"`), []string{at + "gateway is for IPv4 subnets; IPv6 clients learn their routers from router advertisements"}},
		{"boot files", office6(`, "boot": {"ipxe": "http://192.0.2.5/boot.ipxe"}`), []string{at + "boot is for IPv4 subnets"}},
		{"an IPv4 DNS server", office6(`, "dns": ["192.0.2.1"]`), []string{at + "the DNS server 192.0.2.1 is no IPv6 address"}},
		{"an IPv4-mapped DNS server", office6(`, "dns": ["::ffff:192.0.2.1"]`), []string{at + "the DNS server ::ffff:192.0.2.1 is no IPv6 address"}},
		{"a pool of the anycast address", office6(`, "pools": [{"first": "2001:db8:1::", "last": "2001:db8:1::ff"}]`),
			[]string{at + "the pool 2001:db8:1::-2001:db8:1::ff holds 2001:db8:1::, the subnet-router anycast address of the subnet 2001:db8:1::/64"}},
		{"a pool of the last address", office6(`, "pools": [{"first": "2001:db8:1::ff00", "last": "2001:db8:1:0:ffff:ffff:ffff:ffff"}]`), nil},
		{"the anycast address reserved", office6(`, "reservations": [{"mac": "52:54:00:12:34:56", "address": "2001:db8:1:0::"}]`),
			[]string{at + "the reservation of 52:54:00:12:34:56, 2001:db8:1:0::, is the subnet-router anycast address of the subnet 2001:db8:1::/64"}},
		{"a DNS server in a pool", office6(`, "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}], "dns": ["2001:db8:1::150"]`),
			[]string{at + "the pool 2001:db8:1::100-2001:db8:1::1ff holds 2001:db8:1::150, a DNS server"}},
		{"an IPv6 DNS server of an IPv4 subnet", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "dns": ["2001:db8::53"]}`)},
			[]string{`DHCPSubnet "office" spec: the DNS server 2001:db8::53 is no IPv4 address`}},
		{"delegation in IPv4", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "prefixDelegation": {"prefix": "2001:db8:100::/56", "length": 64}}`)},
			[]string{`DHCPSubnet "office" spec: prefixDelegation delegates IPv6 prefixes, which an IPv4 subnet has none of`}},
		{"delegation of an IPv4 range", delegate("198.51.100.0/24", 28), notDelegable("198.51.100.0/24", 28)},
		{"delegation of the range's length", delegate("2001:db8:100::/56", 56), notDelegable("2001:db8:100::/56", 56)},
		{"delegation from ::/8", delegate("::/48", 56), notDelegable("::/48", 56)},
		{"delegation of link-local prefixes", delegate("fe80:1::/48", 56), notDelegable("fe80:1::/48", 56)},
		{"delegation of multicast prefixes", delegate("ff05::/32", 48), notDelegable("ff05::/32", 48)},
		{"delegation of the subnet", delegate("2001:db8:1::/48", 64), []string{at + "prefixDelegation's prefix 2001:db8:1::/48 overlaps the subnet 2001:db8:1::/64"}},
		{"delegation within the subnet", delegate("2001:db8:1:0:8000::/65", 72), []string{at + "prefixDelegation's prefix 2001:db8:1:0:8000::/65 overlaps the subnet 2001:db8:1::/64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: tc.docs, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// The DHCPRelay rules take a relay of both families and refuse a server that is no unicast
// address, of either family.
func TestDHCPRelayRules(t *testing.T) {
	set, doc := routerRules(t)
	relay := func(servers string) config.Document {
		return doc("DHCPRelay", "guests", `{"interfaces": ["vlan20"], "servers": `+servers+`}`)
	}
	for _, tc := range []struct {
		name    string
		servers string
		want    []string
	}{
		{"both families", `["198.51.100.53", "2001:db8:ff::53"]`, nil},
		{"a broadcast", `["255.255.255.255"]`, []string{`DHCPRelay "guests" spec.servers: the server 255.255.255.255 is no unicast address a relay forwards to`}},
		{"All_DHCP_Servers", `["ff05::1:3"]`, []string{`DHCPRelay "guests" spec.servers: the server ff05::1:3 is no unicast address a relay forwards to`}},
		{"a link-local server", `["fe80::53"]`, []string{`DHCPRelay "guests" spec.servers: the server fe80::53 is no unicast address a relay forwards to`}},
		{"a loopback server", `["127.0.0.1"]`, []string{`DHCPRelay "guests" spec.servers: the server 127.0.0.1 is no unicast address a relay forwards to`}},
		{"a server in 0.0.0.0/8", `["0.1.2.3"]`, []string{`DHCPRelay "guests" spec.servers: the server 0.1.2.3 is no unicast address a relay forwards to`}},
		{"a server in 240.0.0.0/4", `["240.0.0.1"]`, []string{`DHCPRelay "guests" spec.servers: the server 240.0.0.1 is no unicast address a relay forwards to`}},
		{"a server in ::/8", `["::102:304"]`, []string{`DHCPRelay "guests" spec.servers: the server ::102:304 is no unicast address a relay forwards to`}},
		{"an IPv4-mapped server", `["::ffff:198.51.100.53"]`, []string{`DHCPRelay "guests" spec.servers: the server ::ffff:198.51.100.53 is no unicast address a relay forwards to`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: []config.Document{relay(tc.servers)}, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}
