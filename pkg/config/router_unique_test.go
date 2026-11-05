package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/schema"
)

// routerImageSchemas are a router image's schemas: the built-in kinds' and the router kinds'.
func routerImageSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	dir := t.TempDir()
	for _, from := range []string{builtinSchemas, "../../test/fixtures/kinds/router"} {
		entries, err := os.ReadDir(from)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(from, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	s, err := schema.LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// uniqueCase is a config whose values repeat where a kind declares them unique, and NewSet's
// refusal of it.
type uniqueCase struct {
	name, yaml, want string
}

// checkUniqueCases builds a set of a node's minimal config and each case's documents against a
// router image's schemas, and wants NewSet to refuse it with the case's line alone.
func checkUniqueCases(t *testing.T, cases []uniqueCase) {
	t.Helper()
	s := routerImageSchemas(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			docs, err := Parse(strings.NewReader(minimal + c.yaml))
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewSet(docs, s)
			if err == nil || err.Error() != c.want {
				t.Errorf("err =\n%v\nwant\n%s", err, c.want)
			}
		})
	}
}

// A VRF's table and conntrack zone are its own, a VRF without a zone taking none, and an
// interface or glob is in one VRF, once.
func TestVRFUnique(t *testing.T) {
	checkUniqueCases(t, []uniqueCase{
		{"two VRFs of one table",
			routerDoc("VRF", "a", `{table: 100}`) + routerDoc("VRF", "b", `{table: 100}`),
			`VRF/a, VRF/b: spec.table 100 is not unique; give each VRF a table of its own`},
		{"two VRFs of one conntrack zone",
			routerDoc("VRF", "a", `{table: 100, conntrackZone: 1}`) + routerDoc("VRF", "b", `{table: 200, conntrackZone: 1}`) +
				routerDoc("VRF", "c", `{table: 300}`) + routerDoc("VRF", "d", `{table: 400}`),
			`VRF/a, VRF/b: spec.conntrackZone 1 is not unique; give each VRF a zone of its own`},
		{"an interface in two VRFs",
			routerDoc("VRF", "a", `{table: 100, interfaces: [eth1, eth2]}`) + routerDoc("VRF", "b", `{table: 200, interfaces: [eth2]}`),
			`VRF/a: spec.interfaces[1], VRF/b: spec.interfaces[0]: "eth2" is not unique; give each interface one VRF`},
		{"an interface twice in one VRF",
			routerDoc("VRF", "a", `{table: 100, interfaces: ["swp*", "swp*"]}`),
			`VRF/a: spec.interfaces[0], spec.interfaces[1]: "swp*" is not unique; give each interface one VRF`},
	})
}

// An interface or glob is in one FirewallZone, once, as is a VRF, and one FirewallPolicy at most
// goes from a zone to another, local included.
func TestFirewallUnique(t *testing.T) {
	checkUniqueCases(t, []uniqueCase{
		{"an interface in two zones",
			routerDoc("FirewallZone", "lan", `{interfaces: [eth1]}`) + routerDoc("FirewallZone", "wan", `{interfaces: [eth1]}`),
			`FirewallZone/lan, FirewallZone/wan: spec.interfaces[0] "eth1" is not unique; give each interface one zone`},
		{"an interface twice in one zone",
			routerDoc("FirewallZone", "lan", `{interfaces: [eth1, "vlan*", eth1]}`),
			`FirewallZone/lan: spec.interfaces[0], spec.interfaces[2]: "eth1" is not unique; give each interface one zone`},
		{"a VRF twice in one zone",
			routerDoc("VRF", "tenant", `{table: 100}`) + routerDoc("FirewallZone", "lan", `{vrfs: [tenant, tenant]}`),
			`FirewallZone/lan: spec.vrfs[0], spec.vrfs[1]: "tenant" is not unique; give each VRF one zone`},
		{"two policies between one pair",
			routerDoc("FirewallZone", "wan", `{interfaces: [eth0]}`) +
				routerDoc("FirewallPolicy", "a", `{from: wan, to: local}`) + routerDoc("FirewallPolicy", "b", `{from: wan, to: local}`),
			`FirewallPolicy/a, FirewallPolicy/b: (spec.from, spec.to) ("wan", "local") is not unique; give each pair of zones one policy`},
	})
}

// A VRF has one BGP instance, an import names each VRF once, a BGP instance announces a range
// once in whatever spelling, a PrefixList's entries match apart, a null ge or le equal to
// another's, and a CommunityList names each community once.
func TestBGPUnique(t *testing.T) {
	bgp := func(name, spec string) string {
		return routerDoc("BGP", name, `{asn: 64512, routerID: 192.0.2.1`+spec+`}`)
	}
	checkUniqueCases(t, []uniqueCase{
		{"two instances of the default VRF",
			bgp("a", "") + bgp("b", ""),
			`BGP/a, BGP/b: spec.vrf "default" is not unique; a VRF has one BGP instance`},
		{"an import of one VRF twice",
			routerDoc("VRF", "tenant", `{table: 100}`) + bgp("a", `, import: {ipv6: {vrfs: [tenant, tenant]}}`),
			`BGP/a: spec.import.ipv6.vrfs[0], spec.import.ipv6.vrfs[1]: "tenant" is not unique; name each VRF once`},
		{"two networks of one range",
			bgp("a", `, networks: [{prefix: 192.0.2.0/24}, {prefix: 192.0.2.1/24}]`),
			`BGP/a: spec.networks[0].prefix, spec.networks[1].prefix: "192.0.2.0/24" is not unique; give each range one network`},
		{"two entries that match alike",
			routerDoc("PrefixList", "edge", `{family: ipv6, entries: [{prefix: "2001:db8:ffff::/48", le: 128}, {prefix: "2001:db8:ffff::/48", ge: 64, le: 128}, {prefix: "2001:db8:ffff::1/48", le: 128, action: deny}]}`),
			`PrefixList/edge: (spec.entries[0].prefix, spec.entries[0].ge, spec.entries[0].le), (spec.entries[2].prefix, spec.entries[2].ge, spec.entries[2].le): ("2001:db8:ffff::/48", null, 128) is not unique; the later never decides; drop one`},
		{"a community twice",
			routerDoc("CommunityList", "c", `{communities: ["64512:100", "64512:200", "64512:100"]}`),
			`CommunityList/c: spec.communities[0], spec.communities[2]: "64512:100" is not unique; name each community once`},
	})
}

// A community has one spelling, without leading zeros, so that two of one value compare equal as
// written: the schema refuses a leading zero in a CommunityList and in a RoutingPolicy's
// addCommunities, and takes the bounds of each number.
func TestCommunityWithALeadingZero(t *testing.T) {
	s := routerImageSchemas(t)
	for _, c := range []struct{ name, yaml, want string }{
		{"a standard community",
			routerDoc("CommunityList", "c", `{communities: ["64512:0100"]}`),
			`CommunityList "c": - at '/spec/communities/0': '64512:0100' does not match pattern `},
		{"a large community",
			routerDoc("CommunityList", "c", `{communities: ["1:2:03"]}`),
			`CommunityList "c": - at '/spec/communities/0': '1:2:03' does not match pattern `},
		{"an added community",
			routerDoc("RoutingPolicy", "p", `{terms: [{then: {action: accept, addCommunities: ["64512:0100"]}}]}`),
			`RoutingPolicy "p": - at '/spec/terms/0/then/addCommunities/0': '64512:0100' does not match pattern `},
		{"the bounds",
			routerDoc("CommunityList", "c", `{communities: ["0:0", "65535:65535", "4294967295:0:1"]}`), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs, err := Parse(strings.NewReader(minimal + c.yaml))
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewSet(docs, s)
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.HasPrefix(err.Error(), c.want)) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A pool entry names each protocol once, a VRF has one resolver, and a resolver names each
// address, client range, forwarder and local zone once, addresses and ranges in whatever spelling.
func TestNAT64AndResolverUnique(t *testing.T) {
	resolver := func(name, spec string) string {
		return routerDoc("Resolver", name, `{listen: ["2001:db8:ffff::1"], allow: ["2001:db8::/32"]`+spec+`}`)
	}
	checkUniqueCases(t, []uniqueCase{
		{"a protocol twice",
			routerDoc("NAT64", "lab", `{prefix: "2001:db8:64::/96", pool4: [{prefix: 203.0.113.1/32, protocols: [udp]}, {prefix: 203.0.113.2/32, protocols: [tcp, udp, tcp]}]}`),
			`NAT64/lab: spec.pool4[1].protocols[0], spec.pool4[1].protocols[2]: "tcp" is not unique; name each protocol once`},
		{"two resolvers of one VRF",
			resolver("a", "") + resolver("b", ""),
			`Resolver/a, Resolver/b: spec.vrf "default" is not unique; a VRF has one resolver`},
		{"an address twice",
			routerDoc("Resolver", "r", `{listen: ["2001:db8:ffff::1", "2001:DB8:ffff::1"], allow: ["2001:db8::/32"]}`),
			`Resolver/r: spec.listen[0], spec.listen[1]: "2001:db8:ffff::1" is not unique; name each address once`},
		{"a client range twice",
			routerDoc("Resolver", "r", `{listen: ["2001:db8:ffff::1"], allow: ["2001:db8::/32", "2001:db8::1/32"]}`),
			`Resolver/r: spec.allow[0], spec.allow[1]: "2001:db8::/32" is not unique; name each range once`},
		{"a forwarder twice",
			resolver("r", `, forwarders: [192.0.2.53, 192.0.2.53]`),
			`Resolver/r: spec.forwarders[0], spec.forwarders[1]: "192.0.2.53" is not unique; name each forwarder once`},
		{"a zone twice",
			resolver("r", `, localZones: [{name: lan.example.org}, {name: lan.example.org, type: refuse}]`),
			`Resolver/r: spec.localZones[0].name, spec.localZones[1].name: "lan.example.org" is not unique; name each zone once`},
	})
}

// A subnet reserves a MAC address once and an address once, in whatever spelling, a relay names
// each server once in whatever spelling, and a link takes one relay, named once, and a subnet
// names a link once.
func TestDHCPUnique(t *testing.T) {
	subnet := func(reservations string) string {
		return routerDoc("DHCPSubnet", "office", `{interfaces: [lan0], subnet: 192.0.2.0/24, reservations: [`+reservations+`]}`)
	}
	checkUniqueCases(t, []uniqueCase{
		{"one MAC address twice",
			subnet(`{mac: "52:54:00:12:34:56", address: 192.0.2.10}, {mac: "52:54:00:12:34:56", address: 192.0.2.11}`),
			`DHCPSubnet/office: spec.reservations[0].mac, spec.reservations[1].mac: "52:54:00:12:34:56" is not unique; keep one reservation of the MAC address`},
		{"one address twice",
			routerDoc("DHCPSubnet", "office", `{interfaces: [lan0], subnet: "2001:db8:1::/64", reservations: [{mac: "52:54:00:12:34:56", address: "2001:db8:1::10"}, {mac: "52:54:00:12:34:57", address: "2001:db8:1:0::10"}]}`),
			`DHCPSubnet/office: spec.reservations[0].address, spec.reservations[1].address: "2001:db8:1::10" is not unique; reserve each address once`},
		{"a server twice",
			routerDoc("DHCPRelay", "guests", `{interfaces: [lan0], servers: ["2001:db8:ff::53", "2001:db8:ff:0::53"]}`),
			`DHCPRelay/guests: spec.servers[0], spec.servers[1]: "2001:db8:ff::53" is not unique; name each server once`},
		{"a link of two relays",
			routerDoc("DHCPRelay", "guests", `{interfaces: [lan0, lan1], servers: [198.51.100.53]}`) + routerDoc("DHCPRelay", "office", `{interfaces: [lan1], servers: [198.51.100.54]}`),
			`DHCPRelay/guests: spec.interfaces[1], DHCPRelay/office: spec.interfaces[0]: "lan1" is not unique; a link takes one relay`},
		{"a link twice in one relay",
			routerDoc("DHCPRelay", "guests", `{interfaces: [lan0, lan0], servers: [198.51.100.53]}`),
			`DHCPRelay/guests: spec.interfaces[0], spec.interfaces[1]: "lan0" is not unique; a link takes one relay`},
		{"a link twice in one subnet",
			routerDoc("DHCPSubnet", "office", `{interfaces: [lan0, lan1, lan0], subnet: 192.0.2.0/24}`),
			`DHCPSubnet/office: spec.interfaces[0], spec.interfaces[2]: "lan0" is not unique; name each link once`},
	})
}
