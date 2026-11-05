package main

import (
	"slices"
	"strings"
	"testing"
)

// relayDocs are a router's documents with the DHCPRelay and DHCPSubnet documents given: lan0
// holds 192.0.2.1/24, fe80::1/64 and 2001:db8:1::1/64, lan1 198.51.100.1/24 alone, lan2
// 2001:db8:2::1/64 and 203.0.113.1/24 in the VRF guest, lan3 203.0.113.65/26 in the VRF dmz,
// and lan4 2001:db8:4::1/64 in the VRF dmz with router advertisements of the Managed flag.
func relayDocs(t *testing.T, relays []any, subnets ...any) map[string][]any {
	t.Helper()
	return map[string][]any{
		"VRF": {
			routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["lan2"]}`),
			routerDoc(t, "VRF", "dmz", `{"table": 400, "interfaces": ["lan3", "lan4"]}`),
		},
		"Network": {
			document(t, "Network", "10-lan0", `{"match": {"name": "lan0"}, "network": {"address": ["192.0.2.1/24", "fe80::1/64", "2001:db8:1::1/64"]}}`),
			document(t, "Network", "11-lan1", `{"match": {"name": "lan1"}, "network": {"address": ["198.51.100.1/24"]}}`),
			document(t, "Network", "12-lan2", `{"match": {"name": "lan2"}, "network": {"address": ["2001:db8:2::1/64", "203.0.113.1/24"]}}`),
			document(t, "Network", "13-lan3", `{"match": {"name": "lan3"}, "network": {"address": ["203.0.113.65/26"]}}`),
			document(t, "Network", "14-lan4", `{"match": {"name": "lan4"}, "network": {"address": ["2001:db8:4::1/64"], "ipv6SendRA": true}, "ipv6SendRA": {"managed": true}}`),
		},
		"DHCPRelay":  relays,
		"DHCPSubnet": subnets,
	}
}

// The relay renderer refuses what spans documents: a VRF's device, links of two VRFs, a link of
// no Network or without the router's address of a server's family, a global one for IPv6, and a
// family relayed and served in one VRF, or in two of which one is the default. It guards, with an
// internal error, a link or server that dnsmasq's config would read as syntax. The rules refuse
// a server that is no unicast address.
func TestRelayRefusals(t *testing.T) {
	relay := func(name, links, servers string) any {
		return routerDoc(t, "DHCPRelay", name, `{"interfaces": `+links+`, "servers": `+servers+`}`)
	}
	office := relay("office", `["lan0"]`, `["198.51.100.53", "2001:db8:ff::53"]`)
	for name, c := range map[string]struct {
		relays  []any
		subnets []any
		want    string
	}{
		"a VRF's device":         {[]any{relay("office", `["guest"]`, `["198.51.100.53"]`)}, nil, "DHCPRelay/office: spec.interfaces[0]: guest is the device of VRF guest; name the VRF's links"},
		"links of two VRFs":      {[]any{relay("office", `["lan0", "lan2"]`, `["198.51.100.53"]`)}, nil, "its links are in VRF default and VRF guest; a relay relays in one VRF"},
		"a link of no Network":   {[]any{relay("office", `["lan9"]`, `["198.51.100.53"]`)}, nil, "no Network document matches lan9 by Name= alone, so the renderer cannot find its addresses; give the link one"},
		"no IPv6 address":        {[]any{relay("office", `["lan1"]`, `["2001:db8:ff::53"]`)}, nil, "DHCPRelay office: Network 11-lan1 gives lan1 no static global IPv6 address, which the relay forwards its clients' requests from"},
		"no IPv4 address":        {[]any{relay("office", `["lan4"]`, `["198.51.100.53"]`)}, nil, "Network 14-lan4 gives lan4 no static IPv4 address"},
		"served in the same VRF": {[]any{office}, []any{subnetDoc(t, "lan", `{"interfaces": ["lan1"], "subnet": "198.51.100.0/24"}`)}, "DHCPRelay office relays DHCPv4 in VRF default and DHCPSubnet lan serves DHCPv4 in VRF default; dnsmasq's and Kea's sockets of one family bind beside each other only in two VRFs other than the default one"},
		"served in a VRF":        {[]any{office}, []any{subnetDoc(t, "dmz", `{"interfaces": ["lan3"], "subnet": "203.0.113.64/26"}`)}, "DHCPRelay office relays DHCPv4 in VRF default and DHCPSubnet dmz serves DHCPv4 in VRF dmz"},
		"relayed in a VRF":       {[]any{relay("guests", `["lan2"]`, `["2001:db8:ff::53"]`)}, []any{subnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64"}`)}, "DHCPRelay guests relays DHCPv6 in VRF guest and DHCPSubnet office6 serves DHCPv6 in VRF default"},
		// Values dnsmasq's config would read as syntax.
		"a glob": {[]any{relay("office", `["lan*"]`, `["198.51.100.53"]`)}, nil,
			`internal error: DHCPRelay office: interfaces: "lan*" is no interface name; chalkd checks the config against the schemas and rules before it renders`},
		"a link that adds an option": {[]any{relay("office", `["lan0\ndhcp-range=lan0"]`, `["198.51.100.53"]`)}, nil, `internal error: DHCPRelay office: interfaces: "lan0\ndhcp-range=lan0" is no interface name`},
		"a server that adds an option": {[]any{relay("office", `["lan0"]`, `["fe80::53%x\ndhcp-range=lan0"]`)}, nil,
			`internal error: DHCPRelay office: the server "fe80::53%x\ndhcp-range=lan0" is no address without a zone; chalkd checks the config against the schemas and rules before it renders`},
		"a server of no address": {[]any{relay("office", `["lan0"]`, `["dhcp.example.org"]`)}, nil, `internal error: DHCPRelay office: the server "dhcp.example.org" is no address without a zone`},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"dhcp-relay"}, relayDocs(t, c.relays, c.subnets...), c.want) })
	}
}

// Each VRF's relays render a dnsmasq relay of their own, dnsmasq-relay-<vrf>.conf apart from the
// default VRF's dnsmasq-relay.conf, which relays on its links alone, each family to its servers
// from the router's first address of the family on the link, a global one for IPv6, with
// bind-dynamic, so that relays of one family in the default VRF and in another bind beside each
// other. A family relays in a VRF beside another VRF's server of the family, and beside a server
// of the other family in any VRF.
func TestRelayPerVRF(t *testing.T) {
	relay := func(name, links, servers string) any {
		return routerDoc(t, "DHCPRelay", name, `{"interfaces": `+links+`, "servers": `+servers+`}`)
	}
	files, err := unitFiles(t, []string{"dhcp-relay"}, relayDocs(t,
		[]any{
			relay("office", `["lan1", "lan0"]`, `["198.51.100.53"]`),
			relay("guests", `["lan2"]`, `["198.51.100.53", "2001:db8:ff::53", "2001:db8:ff::54"]`),
		},
		subnetDoc(t, "dmz6", `{"interfaces": ["lan4"], "subnet": "2001:db8:4::/64", "pools": [{"first": "2001:db8:4::100", "last": "2001:db8:4::1ff"}]}`),
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
		return routerDoc(t, "DHCPRelay", name, `{"interfaces": `+links+`, "servers": `+servers+`}`)
	}
	docs := relayDocs(t,
		// As chalkd gives documents, by name.
		[]any{relay("guests", `["lan2"]`, `["198.51.100.53", "2001:db8:ff::53"]`), relay("office", `["lan1"]`, `["198.51.100.53"]`)},
		subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"}`),
		subnetDoc(t, "dmz6", `{"interfaces": ["lan4"], "subnet": "2001:db8:4::/64"}`),
	)
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
	want := []string{
		`iifname "dmz" meta sdifname { "lan4" } ip6 saddr { fe80::/10 } udp dport { 547 } accept comment "DHCPSubnet dmz6"`,
		`iifname "guest" meta sdifname { "lan2" } ip saddr { 0.0.0.0/0 } udp dport { 67 } accept comment "DHCPRelay guests"`,
		`iifname "guest" meta sdifname { "lan2" } ip6 saddr { fe80::/10 } udp dport { 547 } accept comment "DHCPRelay guests"`,
		`iifname { "lan1" } ip saddr { 0.0.0.0/0 } udp dport { 67 } accept comment "DHCPRelay office"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("openings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The schema takes no glob, which the opening would write into the ruleset.
	refused(t, []string{"router-nftables", "50000"}, relayDocs(t, []any{relay("office", `["lan*"]`, `["198.51.100.53"]`)}),
		`internal error: DHCPRelay office: interfaces: "lan*" is no interface name; chalkd checks the config against the schemas and rules before it renders`)
}

// A relay forwards from none of the router's addresses on a link that the servers' rule refuses,
// such as one of 240.0.0.0/4 or of ::/8.
func TestRelayAddressRanges(t *testing.T) {
	relay := routerDoc(t, "DHCPRelay", "lab", `{"interfaces": ["lan5"], "servers": ["198.51.100.53", "2001:db8:ff::53"]}`)
	for name, c := range map[string]struct{ addresses, want string }{
		"IPv4 in 240.0.0.0/4": {`"240.0.0.1/24", "2001:db8:5::1/64"`, "Network 15-lan5 gives lan5 no static IPv4 address"},
		"IPv4 in 0.0.0.0/8":   {`"0.0.0.1/24", "2001:db8:5::1/64"`, "Network 15-lan5 gives lan5 no static IPv4 address"},
		"IPv6 in ::/8":        {`"192.0.2.65/26", "::1:0:0:1/64"`, "Network 15-lan5 gives lan5 no static global IPv6 address"},
	} {
		t.Run(name, func(t *testing.T) {
			docs := relayDocs(t, []any{relay})
			docs["Network"] = append(docs["Network"], document(t, "Network", "15-lan5", `{"match": {"name": "lan5"}, "network": {"address": [`+c.addresses+`]}}`))
			refused(t, []string{"dhcp-relay"}, docs, c.want)
		})
	}
}
