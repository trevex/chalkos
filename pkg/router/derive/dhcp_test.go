package derive

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
)

// relayDoc is a DHCPRelay document of the links and servers given in JSON.
func relayDoc(t *testing.T, name, links, servers string) any {
	t.Helper()
	return derivetest.RouterDoc(t, "DHCPRelay", name, `{"interfaces": `+links+`, "servers": `+servers+`}`)
}

// The validator refuses what spans documents: a subnet's link that is a VRF's device or a NAT64's
// link, a link of no Network, links of two VRFs or of a VRF no document declares, a link without
// the router's address in the subnet, a link of two subnets, subnets that overlap in a VRF, and a
// pool or reservation of the router's own address. It guards, with an internal error, no
// interface, a glob, which dnsmasq's config and the firewall's ruleset would read as syntax, and
// a range or address that does not parse.
func TestSubnetRefusals(t *testing.T) {
	office := func(more string) any {
		return derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"`+more+`}`)
	}
	pool := func(first, last string) any {
		return office(`, "pools": [{"first": "` + first + `", "last": "` + last + `"}]`)
	}
	reserve := func(address string) any {
		return office(`, "reservations": [{"mac": "52:54:00:12:34:56", "address": "` + address + `", "hostname": null}]`)
	}
	const checked = "; chalkd checks the config against the schemas, the rules and the validators before it renders"
	for name, c := range map[string]struct {
		subnets []any
		want    string
	}{
		"a VRF's device":         {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": ["guest"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet/office: spec.interfaces[0]: guest is the device of VRF guest; name the VRF's links"},
		"a NAT64's link":         {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": ["n64-edge"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet/office: spec.interfaces[0]: n64-edge is the link of NAT64 edge"},
		"links of two VRFs":      {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan0", "vlan20"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet/office: spec.interfaces[1]: its links are in VRF default and VRF guest; a subnet is served in one VRF"},
		"a VRF of no document":   {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan2"], "subnet": "203.0.113.0/24"}`)}, `DHCPSubnet/office: spec.interfaces: its links are in the VRF "ghost", which no VRF document declares`},
		"a link of no Network":   {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan9"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet/office: spec.interfaces[0]: no Network document matches lan9 by Name= alone, so the router cannot find its address in the subnet 192.0.2.0/24; give the link one"},
		"a link without address": {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "203.0.113.0/24"}`)}, "DHCPSubnet/office: spec.interfaces[0]: Network 10-lan0 gives lan0 no static address in the subnet 203.0.113.0/24, which Kea answers its clients from"},
		"a link of two subnets":  {[]any{office(""), derivetest.SubnetDoc(t, "other", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet/other: spec.interfaces[0]: DHCPSubnet office and DHCPSubnet other both serve lan0; a link takes one subnet of each family"},
		"overlapping subnets":    {[]any{office(""), derivetest.SubnetDoc(t, "servers", `{"interfaces": ["lan1"], "subnet": "192.0.2.64/26"}`)}, "DHCPSubnet/servers: spec.subnet: DHCPSubnet office and DHCPSubnet servers overlap in VRF default: 192.0.2.0/24 and 192.0.2.64/26"},
		"a pool's first address": {[]any{pool("192.0.2.1", "192.0.2.10")}, "DHCPSubnet/office: spec.pools[0]: the pool 192.0.2.1-192.0.2.10 holds 192.0.2.1, the router's own address on the subnet's links"},
		"a pool of the router's": {[]any{pool("192.0.2.10", "192.0.2.50")}, "DHCPSubnet/office: spec.pools[0]: the pool 192.0.2.10-192.0.2.50 holds 192.0.2.30, the router's own address on the subnet's links"},
		"the router reserved":    {[]any{reserve("192.0.2.30")}, "DHCPSubnet/office: spec.reservations[0].address: the reservation of 52:54:00:12:34:56, 192.0.2.30, is the router's own address on the subnet's links"},
		// What the schemas refuse and the model would panic on, or the outputs read as syntax.
		"no interface":                {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": [], "subnet": "192.0.2.0/24"}`)}, "internal error: DHCPSubnet office: interfaces names no interface" + checked},
		"a glob":                      {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan*"], "subnet": "192.0.2.0/24"}`)}, `internal error: DHCPSubnet office: interfaces: "lan*" is no interface name` + checked},
		"an interface of a quote":     {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan0\""], "subnet": "192.0.2.0/24"}`)}, `internal error: DHCPSubnet office: interfaces: "lan0\"" is no interface name`},
		"a subnet of no range":        {[]any{derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0"}`)}, `internal error: DHCPSubnet office: netip.ParsePrefix("192.0.2.0"): no '/'` + checked},
		"a pool of no address":        {[]any{pool("192.0.2.100", "192.0.2.x")}, `internal error: DHCPSubnet office: ParseAddr("192.0.2.x"): unexpected character`},
		"a reservation of no address": {[]any{reserve("printer")}, `internal error: DHCPSubnet office: ParseAddr("printer"): unable to parse IP`},
	} {
		t.Run(name, func(t *testing.T) { refusedOrGuarded(t, derivetest.KeaDocs(t, c.subnets...), c.want) })
	}
}

// The validator refuses what spans IPv6 subnets: a pool or reservation of the router's own
// address, two IPv6 subnets of one link or that overlap, the subnet of one and the delegated
// prefixes of another, delegated prefixes of the router's address on a link of the VRF, and links
// whose Network sends no router advertisements with the flag that tells clients to ask: Managed
// for addresses, Managed or OtherInformation for delegated prefixes and options alone. It guards,
// with an internal error, delegated prefixes no longer than their range, which it would count by
// a negative shift.
func TestSubnet6Refusals(t *testing.T) {
	office := func(more string) any {
		return derivetest.SubnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64"`+more+`}`)
	}
	pool := `, "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}]`
	for name, c := range map[string]struct {
		subnets []any
		want    string
	}{
		"the router's address":        {[]any{office(`, "pools": [{"first": "2001:db8:1::1", "last": "2001:db8:1::ff"}]`)}, "DHCPSubnet/office6: spec.pools[0]: the pool 2001:db8:1::1-2001:db8:1::ff holds 2001:db8:1::1, the router's own address on the subnet's link"},
		"the router reserved":         {[]any{office(`, "reservations": [{"mac": "52:54:00:12:34:56", "address": "2001:db8:1::1", "hostname": null}]`)}, "DHCPSubnet/office6: spec.reservations[0].address: the reservation of 52:54:00:12:34:56, 2001:db8:1::1, is the router's own address on the subnet's link"},
		"two subnets of a link":       {[]any{office(""), derivetest.SubnetDoc(t, "other", `{"interfaces": ["lan0"], "subnet": "2001:db8:5::/64"}`)}, "DHCPSubnet/other: spec.interfaces[0]: DHCPSubnet office6 and DHCPSubnet other both serve lan0; a link takes one subnet of each family"},
		"overlapping subnets":         {[]any{office(""), derivetest.SubnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8::/32", "prefixDelegation": {"prefix": "2001:db9::/48", "length": 56}}`)}, "DHCPSubnet/cpe: spec.subnet: DHCPSubnet office6 and DHCPSubnet cpe overlap in VRF default: 2001:db8:1::/64 and 2001:db8::/32"},
		"delegating another's":        {[]any{office(""), derivetest.SubnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64", "prefixDelegation": {"prefix": "2001:db8:1:0:8000::/65", "length": 72}}`)}, "DHCPSubnet/cpe: spec.prefixDelegation.prefix: DHCPSubnet office6 and DHCPSubnet cpe overlap in VRF default: 2001:db8:1::/64 and 2001:db8:1:0:8000::/65"},
		"delegating a link's address": {[]any{derivetest.SubnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64", "prefixDelegation": {"prefix": "2001:db8:4::/48", "length": 56}}`)}, "DHCPSubnet/cpe: spec.prefixDelegation.prefix: prefixDelegation's prefix 2001:db8:4::/48 holds 2001:db8:4::1, the address Network 13-lan3 gives a link of VRF default"},
		"no advertisements":           {[]any{derivetest.SubnetDoc(t, "lab", `{"interfaces": ["lan2"], "subnet": "2001:db8:3::/64"`+strings.ReplaceAll(pool, ":1::", ":3::")+`}`)}, "DHCPSubnet/lab: spec.interfaces[0]: Network 12-lan2 sends no router advertisements on lan2 with Managed=yes, which tell its clients to ask the DHCPv6 server for addresses; give it ipv6SendRA: true in network and managed: true in ipv6SendRA"},
		"addresses without Managed":   {[]any{derivetest.SubnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64"`+strings.ReplaceAll(pool, ":1::", ":2::")+`}`)}, "DHCPSubnet/cpe: spec.interfaces[0]: Network 11-lan1 sends no router advertisements on lan1 with Managed=yes, which tell its clients to ask the DHCPv6 server for addresses; give it ipv6SendRA: true in network and managed: true in ipv6SendRA"},
		"options without a flag":      {[]any{derivetest.SubnetDoc(t, "lab", `{"interfaces": ["lan3"], "subnet": "2001:db8:4::/64", "dns": ["2001:db8:4::1"]}`)}, "DHCPSubnet/lab: spec.interfaces[0]: Network 13-lan3 sends no router advertisements on lan3 with Managed=yes or OtherInformation=yes, which tell its clients to ask the DHCPv6 server; give it ipv6SendRA: true in network and otherInformation: true in ipv6SendRA"},
		// What the model would count by a negative shift.
		"delegating short prefixes": {[]any{office(`, "prefixDelegation": {"prefix": "2001:db8:100::/56", "length": 56}`)},
			`internal error: DHCPSubnet office6: prefixDelegation's prefix 2001:db8:100::/56 is no range shorter than its length 56; chalkd checks the config against the schemas, the rules and the validators before it renders`},
		"delegating no range": {[]any{office(`, "prefixDelegation": {"prefix": "2001:db8:100::", "length": 56}`)},
			`internal error: DHCPSubnet office6: netip.ParsePrefix("2001:db8:100::"): no '/'`},
	} {
		t.Run(name, func(t *testing.T) { refusedOrGuarded(t, derivetest.Kea6Docs(t, c.subnets...), c.want) })
	}
}

// Each VRF's subnets are apart from the other VRFs': one range serves in two VRFs, and a delegated
// range may hold the router's addresses on links of other VRFs.
func TestSubnetsPerVRF(t *testing.T) {
	accepted(t, derivetest.KeaDocs(t,
		derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan1", "lan0"], "subnet": "192.0.2.0/24", "pools": [{"first": "192.0.2.100", "last": "192.0.2.199"}]}`),
		derivetest.SubnetDoc(t, "guests", `{"interfaces": ["vlan20"], "subnet": "192.0.2.0/24", "leaseTime": "1h"}`),
	))
	accepted(t, derivetest.Kea6Docs(t,
		derivetest.SubnetDoc(t, "guests6", `{"interfaces": ["vlan30"], "subnet": "2001:db8:1::/64", "prefixDelegation": {"prefix": "2001:db8:4::/48", "length": 56}}`),
	))
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

// networkd reads router advertisements under their former names as well, IPv6PrefixDelegation=
// in [Network] and the section [IPv6PrefixDelegation], and the unit lists them before the
// current names, whose valid values win.
func TestSubnet6FormerAdvertisementNames(t *testing.T) {
	office := derivetest.SubnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64", "pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}]}`)
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
				"Network":    {derivetest.Doc(t, "Network", "10-lan0", `{"match": {"name": "lan0"}, "network": {"address": ["2001:db8:1::1/64"], `+c.network+`}`)},
				"DHCPSubnet": {office},
			}
			if c.served {
				accepted(t, docs)
				return
			}
			refused(t, docs, "DHCPSubnet/office6: spec.interfaces[0]: Network 10-lan0 sends no router advertisements on lan0 with Managed=yes, which tell its clients to ask the DHCPv6 server for addresses; give it ipv6SendRA: true in network and managed: true in ipv6SendRA")
		})
	}
}

// A delegated range holds no static route of a Network of the VRF's links, nor a NAT64's prefix
// in the VRF, whose routes a route of a delegated prefix would replace. A route of a wider range
// or of another VRF, and a NAT64 of another VRF, may lie over it.
func TestSubnet6DelegatesNoConfiguredRoute(t *testing.T) {
	cpe := derivetest.SubnetDoc(t, "cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64", "prefixDelegation": {"prefix": "2001:db8:100::/40", "length": 56}}`)
	route := func(name, link, destination string) any {
		return derivetest.Doc(t, "Network", name, `{"match": {"name": "`+link+`"}, "network": {"address": ["2001:db8:9::1/64"]}, "route": [{"gateway": "2001:db8:9::2"}, {"destination": "`+destination+`", "gateway": "2001:db8:9::2"}]}`)
	}
	nat64 := func(vrf string) any {
		return derivetest.RouterDoc(t, "NAT64", "nat64", `{"vrf": `+vrf+`, "prefix": "2001:db8:100:ff::/96", "pool4": []}`)
	}
	for name, c := range map[string]struct {
		extra map[string][]any
		want  string
	}{
		"a static route":           {map[string][]any{"Network": {route("40-lan9", "lan9", "2001:db8:100:100::/56")}}, "DHCPSubnet/cpe: spec.prefixDelegation.prefix: prefixDelegation's prefix 2001:db8:100::/40 holds the route of 2001:db8:100:100::/56 that Network 40-lan9 gives a link of VRF default"},
		"a static route's address": {map[string][]any{"Network": {route("40-lan9", "lan9", "2001:db8:100::5")}}, "DHCPSubnet/cpe: spec.prefixDelegation.prefix: prefixDelegation's prefix 2001:db8:100::/40 holds the route of 2001:db8:100::5/128 that Network 40-lan9 gives a link of VRF default"},
		"a NAT64's prefix":         {map[string][]any{"NAT64": {nat64("null")}}, "DHCPSubnet/cpe: spec.prefixDelegation.prefix: prefixDelegation's prefix 2001:db8:100::/40 holds NAT64 nat64's prefix 2001:db8:100:ff::/96 of VRF default"},
		"a wider route":            {map[string][]any{"Network": {route("40-lan9", "lan9", "2001:db8::/32")}}, ""},
		"another VRF's route":      {map[string][]any{"Network": {route("40-lan9", "lan9", "2001:db8:100:100::/56")}, "VRF": {derivetest.RouterDoc(t, "VRF", "lab", `{"table": 400, "interfaces": ["lan9"]}`)}}, ""},
		"another VRF's NAT64":      {map[string][]any{"NAT64": {nat64(`"guest"`)}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			docs := derivetest.Kea6Docs(t, cpe)
			for kind, more := range c.extra {
				docs[kind] = append(docs[kind], more...)
			}
			if c.want != "" {
				refused(t, docs, c.want)
				return
			}
			accepted(t, docs)
		})
	}
}

// The router routes 65536 delegated prefixes at most, so the subnets of all VRFs delegate no
// more altogether.
func TestSubnet6DelegatesAtMost65536(t *testing.T) {
	cpe := func(name, link, subnet, prefix string, length int) any {
		return derivetest.SubnetDoc(t, name, `{"interfaces": ["`+link+`"], "subnet": "`+subnet+`", "prefixDelegation": {"prefix": "`+prefix+`", "length": `+strconv.Itoa(length)+`}}`)
	}
	accepted(t, derivetest.Kea6Docs(t, cpe("cpe", "lan1", "2001:db8:2::/64", "2001:db8:100::/40", 56)))
	refused(t, derivetest.Kea6Docs(t,
		cpe("cpe", "lan1", "2001:db8:2::/64", "2001:db8:100::/40", 56),
		cpe("guests6", "vlan30", "2001:db8:1::/64", "2001:db8:200::/48", 56),
	), "DHCPSubnet/guests6: spec.prefixDelegation.length: the DHCPSubnet documents delegate more than 65536 prefixes, the most the router routes; delegate fewer")
	refused(t, derivetest.Kea6Docs(t, cpe("cpe", "lan1", "2001:db8:2::/64", "2001:db9::/32", 128)),
		"DHCPSubnet/cpe: spec.prefixDelegation.length: the DHCPSubnet documents delegate more than 65536 prefixes, the most the router routes; delegate fewer")
}

// The validator refuses what spans a relay's documents: a VRF's device, links of two VRFs, a link
// of no Network or without the router's address of a server's family, a global one for IPv6, and
// a family relayed and served in one VRF, or in two of which one is the default. It guards, with
// an internal error, a link or server that dnsmasq's config would read as syntax. The rules refuse
// a server that is no unicast address.
func TestRelayRefusals(t *testing.T) {
	office := relayDoc(t, "office", `["lan0"]`, `["198.51.100.53", "2001:db8:ff::53"]`)
	for name, c := range map[string]struct {
		relays  []any
		subnets []any
		want    string
	}{
		"a VRF's device":         {[]any{relayDoc(t, "office", `["guest"]`, `["198.51.100.53"]`)}, nil, "DHCPRelay/office: spec.interfaces[0]: guest is the device of VRF guest; name the VRF's links"},
		"links of two VRFs":      {[]any{relayDoc(t, "office", `["lan0", "lan2"]`, `["198.51.100.53"]`)}, nil, "DHCPRelay/office: spec.interfaces[1]: its links are in VRF default and VRF guest; a relay relays in one VRF"},
		"a link of no Network":   {[]any{relayDoc(t, "office", `["lan9"]`, `["198.51.100.53"]`)}, nil, "DHCPRelay/office: spec.interfaces[0]: no Network document matches lan9 by Name= alone, so the router cannot find its addresses; give the link one"},
		"no IPv6 address":        {[]any{relayDoc(t, "office", `["lan1"]`, `["2001:db8:ff::53"]`)}, nil, "DHCPRelay/office: spec.interfaces[0]: Network 11-lan1 gives lan1 no static global IPv6 address, which the relay forwards its clients' requests from"},
		"no IPv4 address":        {[]any{relayDoc(t, "office", `["lan4"]`, `["198.51.100.53"]`)}, nil, "DHCPRelay/office: spec.interfaces[0]: Network 14-lan4 gives lan4 no static IPv4 address, which the relay forwards its clients' requests from"},
		"served in the same VRF": {[]any{office}, []any{derivetest.SubnetDoc(t, "lan", `{"interfaces": ["lan1"], "subnet": "198.51.100.0/24"}`)}, "DHCPSubnet/lan: spec.interfaces: DHCPRelay office relays DHCPv4 in VRF default and DHCPSubnet lan serves DHCPv4 in VRF default; dnsmasq's and Kea's sockets of one family bind beside each other only in two VRFs other than the default one"},
		"served in a VRF":        {[]any{office}, []any{derivetest.SubnetDoc(t, "dmz", `{"interfaces": ["lan3"], "subnet": "203.0.113.64/26"}`)}, "DHCPSubnet/dmz: spec.interfaces: DHCPRelay office relays DHCPv4 in VRF default and DHCPSubnet dmz serves DHCPv4 in VRF dmz; dnsmasq's and Kea's sockets of one family bind beside each other only in two VRFs other than the default one"},
		"relayed in a VRF":       {[]any{relayDoc(t, "guests", `["lan2"]`, `["2001:db8:ff::53"]`)}, []any{derivetest.SubnetDoc(t, "office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64"}`)}, "DHCPSubnet/office6: spec.interfaces: DHCPRelay guests relays DHCPv6 in VRF guest and DHCPSubnet office6 serves DHCPv6 in VRF default; dnsmasq's and Kea's sockets of one family bind beside each other only in two VRFs other than the default one"},
		// Values dnsmasq's config would read as syntax.
		"a glob": {[]any{relayDoc(t, "office", `["lan*"]`, `["198.51.100.53"]`)}, nil,
			`internal error: DHCPRelay office: interfaces: "lan*" is no interface name; chalkd checks the config against the schemas, the rules and the validators before it renders`},
		"a link that adds an option": {[]any{relayDoc(t, "office", `["lan0\ndhcp-range=lan0"]`, `["198.51.100.53"]`)}, nil, `internal error: DHCPRelay office: interfaces: "lan0\ndhcp-range=lan0" is no interface name`},
		"a server that adds an option": {[]any{relayDoc(t, "office", `["lan0"]`, `["fe80::53%x\ndhcp-range=lan0"]`)}, nil,
			`internal error: DHCPRelay office: spec.servers[0]: "fe80::53%x\ndhcp-range=lan0" has a zone; chalkd checks the config against the schemas, the rules and the validators before it renders`},
		"a server of no address": {[]any{relayDoc(t, "office", `["lan0"]`, `["dhcp.example.org"]`)}, nil, `internal error: DHCPRelay office: ParseAddr("dhcp.example.org"): unexpected character`},
	} {
		t.Run(name, func(t *testing.T) { refusedOrGuarded(t, derivetest.RelayDocs(t, c.relays, c.subnets...), c.want) })
	}
}

// A family relays in a VRF beside another VRF's server of the family, and beside a server of the
// other family in any VRF.
func TestRelaysBesideServers(t *testing.T) {
	accepted(t, derivetest.RelayDocs(t,
		[]any{
			relayDoc(t, "office", `["lan1", "lan0"]`, `["198.51.100.53"]`),
			relayDoc(t, "guests", `["lan2"]`, `["198.51.100.53", "2001:db8:ff::53", "2001:db8:ff::54"]`),
		},
		derivetest.SubnetDoc(t, "dmz6", `{"interfaces": ["lan4"], "subnet": "2001:db8:4::/64", "pools": [{"first": "2001:db8:4::100", "last": "2001:db8:4::1ff"}]}`),
	))
}

// A relay forwards from none of the router's addresses on a link that the servers' rule refuses,
// such as one of 240.0.0.0/4 or of ::/8.
func TestRelayAddressRanges(t *testing.T) {
	relay := relayDoc(t, "lab", `["lan5"]`, `["198.51.100.53", "2001:db8:ff::53"]`)
	for name, c := range map[string]struct{ addresses, want string }{
		"IPv4 in 240.0.0.0/4": {`"240.0.0.1/24", "2001:db8:5::1/64"`, "DHCPRelay/lab: spec.interfaces[0]: Network 15-lan5 gives lan5 no static IPv4 address, which the relay forwards its clients' requests from"},
		"IPv4 in 0.0.0.0/8":   {`"0.0.0.1/24", "2001:db8:5::1/64"`, "DHCPRelay/lab: spec.interfaces[0]: Network 15-lan5 gives lan5 no static IPv4 address, which the relay forwards its clients' requests from"},
		"IPv6 in ::/8":        {`"192.0.2.65/26", "::1:0:0:1/64"`, "DHCPRelay/lab: spec.interfaces[0]: Network 15-lan5 gives lan5 no static global IPv6 address, which the relay forwards its clients' requests from"},
	} {
		t.Run(name, func(t *testing.T) {
			docs := derivetest.RelayDocs(t, []any{relay})
			docs["Network"] = append(docs["Network"], derivetest.Doc(t, "Network", "15-lan5", `{"match": {"name": "lan5"}, "network": {"address": [`+c.addresses+`]}}`))
			refused(t, docs, c.want)
		})
	}
}
