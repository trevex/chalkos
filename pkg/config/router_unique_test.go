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
// written.
func TestCommunityWithALeadingZero(t *testing.T) {
	docs, err := Parse(strings.NewReader(minimal + routerDoc("CommunityList", "c", `{communities: ["64512:0100"]}`)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSet(docs, routerImageSchemas(t)); err == nil || !strings.Contains(err.Error(), "communities") {
		t.Errorf("err = %v, want the schema's refusal of 64512:0100", err)
	}
}
