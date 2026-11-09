package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
)

// Each VRF's relays render a dnsmasq relay of their own, dnsmasq-relay-<vrf>.conf apart from the
// default VRF's dnsmasq-relay.conf, which relays on its links alone, each family to its servers
// from the router's first address of the family on the link, a global one for IPv6, with
// bind-dynamic, so that relays of one family in the default VRF and in another bind beside each
// other. A family relays in a VRF beside another VRF's server of the family, and beside a server
// of the other family in any VRF.
func TestRelayPerVRF(t *testing.T) {
	relay := func(name, links, servers string) any {
		return derivetest.RouterDoc(t, "DHCPRelay", name, `{"interfaces": `+links+`, "servers": `+servers+`}`)
	}
	files, err := unitFiles(t, []string{"dhcp-relay"}, derivetest.RelayDocs(t,
		[]any{
			relay("office", `["lan1", "lan0"]`, `["198.51.100.53"]`),
			relay("guests", `["lan2"]`, `["198.51.100.53", "2001:db8:ff::53", "2001:db8:ff::54"]`),
		},
		derivetest.SubnetDoc(t, "dmz6", `{"interfaces": ["lan4"], "subnet": "2001:db8:4::/64", "pools": [{"first": "2001:db8:4::100", "last": "2001:db8:4::1ff"}]}`),
	))
	if err != nil {
		t.Fatal(err)
	}
	if names := slicesSorted(files); !slices.Equal(names, []string{"dnsmasq-relay-guest.conf", "dnsmasq-relay.conf"}) {
		t.Fatalf("files = %v", names)
	}
	for file, want := range map[string][]string{
		"dnsmasq-relay.conf":       {"interface=lan0", "interface=lan1", "dhcp-relay=192.0.2.1,198.51.100.53", "dhcp-relay=198.51.100.1,198.51.100.53"},
		"dnsmasq-relay-guest.conf": {"interface=lan2", "dhcp-relay=203.0.113.1,198.51.100.53", "dhcp-relay=2001:db8:2::1,2001:db8:ff::53", "dhcp-relay=2001:db8:2::1,2001:db8:ff::54"},
	} {
		var got []string
		for _, l := range strings.Split(files[file], "\n") {
			if strings.HasPrefix(l, "interface=") || strings.HasPrefix(l, "dhcp-relay=") {
				got = append(got, l)
			}
		}
		if !slices.Equal(got, want) || !strings.Contains(files[file], "\nport=0\n") || !strings.Contains(files[file], "\nbind-dynamic\n") {
			t.Errorf("%s = %s", file, files[file])
		}
	}
}

// The firewall opens DHCPv6 from link-local addresses on an IPv6 subnet's link, DHCPv4 and DHCPv6
// on a relay's links by the families of its servers, each in the links' VRF, and nothing for an
// IPv4 subnet, whose server reads ahead of the firewall.
func TestDHCPOpenings(t *testing.T) {
	relay := func(name, links, servers string) any {
		return derivetest.RouterDoc(t, "DHCPRelay", name, `{"interfaces": `+links+`, "servers": `+servers+`}`)
	}
	// openings are the lines of the ruleset of docs that open DHCP.
	openings := func(docs map[string][]any) []string {
		ruleset, err := unitFiles(t, []string{"router-nftables", "50000"}, docs)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range strings.Split(ruleset[rulesetFile], "\n") {
			if strings.Contains(l, `comment "DHCP`) {
				got = append(got, strings.TrimSpace(l))
			}
		}
		return got
	}
	got := openings(derivetest.RelayDocs(t,
		// As chalkd gives documents, by name.
		[]any{relay("guests", `["lan2"]`, `["198.51.100.53", "2001:db8:ff::53"]`), relay("office", `["lan1"]`, `["198.51.100.53"]`)},
		derivetest.SubnetDoc(t, "dmz6", `{"interfaces": ["lan4"], "subnet": "2001:db8:4::/64"}`),
	))
	want := []string{
		`iifname "dmz" meta sdifname { "lan4" } ip6 saddr { fe80::/10 } udp dport { 547 } accept comment "DHCPSubnet dmz6"`,
		`iifname "guest" meta sdifname { "lan2" } ip saddr { 0.0.0.0/0 } udp dport { 67 } accept comment "DHCPRelay guests"`,
		`iifname "guest" meta sdifname { "lan2" } ip6 saddr { fe80::/10 } udp dport { 547 } accept comment "DHCPRelay guests"`,
		`iifname { "lan1" } ip saddr { 0.0.0.0/0 } udp dport { 67 } accept comment "DHCPRelay office"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("openings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := openings(derivetest.KeaDocs(t, derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"}`))); len(got) > 0 {
		t.Errorf("an IPv4 subnet opens %q", got)
	}
	// The schema takes no glob, which the opening would write into the ruleset.
	refused(t, []string{"router-nftables", "50000"}, derivetest.RelayDocs(t, []any{relay("office", `["lan*"]`, `["198.51.100.53"]`)}),
		`internal error: DHCPRelay office: interfaces: "lan*" is no interface name; chalkd checks the config against the schemas, the rules and the validators before it renders`)
}
