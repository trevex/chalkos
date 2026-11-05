package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// The PolicyRoute rules accept a LAN router's policies into a VRF, a table and next hops, link-local
// ones included, and refuse an in of a zone and interfaces, an empty match, a set beside ranges,
// addresses of another family, ports without a protocol, a to of no target or of two, a next hop
// of another family, the local table or a VRF's as a table, a next hop's table of the kernel's or
// of a VRF, a next hop on lo, and a next hop that is no unicast address.
func TestPolicyRouteRules(t *testing.T) {
	set, doc := routerRules(t)
	lan := []config.Document{
		doc("Router", "router", `{"management": {"interfaces": ["lan0"]}}`),
		doc("FirewallZone", "lan", `{"interfaces": ["lan0"]}`),
		doc("AddressSet", "guests", `{"family": "ipv4", "prefixes": ["192.0.2.64/26"]}`),
		doc("VRF", "transit", `{"table": 100, "interfaces": ["vt0"]}`),
		doc("PolicyRoute", "lan", `{"in": {"zone": "lan"}, "rules": [
			{"match": {"source": {"set": "guests"}, "protocol": "tcp", "destinationPorts": [443]}, "to": {"vrf": "transit"}},
			{"match": {"dscp": [46]}, "to": {"vrf": "transit"}},
			{"match": {"destination": {"cidrs": ["192.0.2.0/24", "2001:db8:1::/64"]}}, "to": {"table": 254}}]}`),
		doc("PolicyRoute", "servers", `{"in": {"interfaces": ["lan0"]}, "rules": [
			{"match": {"source": {"cidrs": ["192.0.2.32/27"]}}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 200}}},
			{"match": {"source": {"cidrs": ["192.0.2.48/28"]}}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 200}}},
			{"match": {"family": "ipv6", "source": {"cidrs": ["2001:db8:1::20/123"]}}, "to": {"nextHop": {"address": "2001:db8:b::2", "interface": "wanb0", "table": 201}}}]}`),
	}
	replace := func(docs []config.Document, with ...config.Document) []config.Document {
		out := slices.Clone(docs)
		for _, w := range with {
			i := slices.IndexFunc(out, func(d config.Document) bool { return d.Kind == w.Kind && d.Name == w.Name })
			if i < 0 {
				out = append(out, w)
			} else {
				out[i] = w
			}
		}
		return out
	}
	route := func(name, spec string) config.Document { return doc("PolicyRoute", name, spec) }
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"a LAN router", lan, nil},
		{"every link", replace(lan, route("all", `{"rules": [{"match": {"source": {"cidrs": ["0.0.0.0/0"]}}, "to": {"table": 100000}}]}`)), nil},
		{"a zone and interfaces", replace(lan, route("lan", `{"in": {"zone": "lan", "interfaces": ["lan0"]}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)),
			[]string{"PolicyRoute lan: in names a zone or interfaces, one of them; leave in out for traffic from every link"}},
		{"an empty in", replace(lan, route("lan", `{"in": {}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)),
			[]string{"PolicyRoute lan: in names a zone or interfaces, one of them; leave in out for traffic from every link"}},
		// The output router-nftables refuses an interface of a VRF's device, by name and by glob.
		{"a VRF's device", replace(lan, route("lan", `{"in": {"interfaces": ["lan0", "transit"]}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)), nil},
		{"an empty match", replace(lan, route("lan", `{"rules": [{"match": {}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule has an empty match, which would take every packet; to send every packet, say so, such as with source: {cidrs: [0.0.0.0/0]}`}},
		{"a set beside ranges", replace(lan, route("lan", `{"rules": [{"match": {"source": {"set": "guests", "cidrs": ["192.0.2.0/24"]}}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's source or destination names both set and cidrs, or neither; give one, or leave the field out to match every address`}},
		{"a family and a set of the other", replace(lan, route("lan", `{"rules": [{"match": {"family": "ipv6", "source": {"set": "guests"}}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule has addresses or a set of another family than its own`,
				`PolicyRoute "lan" spec.rules: a rule matches no packet: its family, protocol, addresses and sets leave neither IPv4 nor IPv6`}},
		{"ports without a protocol", replace(lan, route("lan", `{"rules": [{"match": {"destinationPorts": [443]}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule has ports without the protocol tcp, udp or sctp`}},
		{"no target", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's to names a VRF, a table or a next hop, one of them`}},
		{"two targets", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit", "table": 300}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's to names a VRF, a table or a next hop, one of them`}},
		{"a next hop of the other family", replace(lan, route("lan", `{"rules": [{"match": {"source": {"cidrs": ["2001:db8:1::/64"]}}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 202}}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's next hop is of another family than the packets the rule matches`}},
		{"the local table", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 255}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table`}},
		{"a VRF's table", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 100}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table`}},
		{"a next hop in main", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 254}}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a next hop's table is 253, 254 or 255, the kernel's own, or a VRF's table; give the next hop a table of its own`}},
		{"a next hop in a VRF's table", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 100}}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a next hop's table is 253, 254 or 255, the kernel's own, or a VRF's table; give the next hop a table of its own`}},
		{"a next hop on lo", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "lo", "table": 202}}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a next hop's interface is lo, which no next hop is on; name the link the next hop is on`}},
		{"link-local next hops", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": {"address": "fe80::1", "interface": "wanb0", "table": 202}}}, {"match": {"dscp": [10]}, "to": {"nextHop": {"address": "169.254.0.1", "interface": "wanb0", "table": 203}}}]}`)), nil},
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
	// A next hop is a unicast address: of neither family unspecified, loopback or multicast, not
	// IPv4's broadcast and no IPv4-mapped IPv6 address, which is no IPv6 neighbour on a link. The
	// match takes both families, so the rule of the next hop's family passes.
	for _, address := range []string{"0.0.0.0", "::", "127.0.0.1", "127.1.2.3", "::1", "224.0.0.1", "239.255.255.250", "ff02::1", "255.255.255.255", "::ffff:198.51.100.130", "::ffff:127.0.0.1"} {
		t.Run("a next hop of "+address, func(t *testing.T) {
			docs := replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}, {"match": {"dscp": [10]}, "to": {"nextHop": {"address": "`+address+`", "interface": "wanb0", "table": 202}}}]}`))
			failures, err := set.Evaluate(Input{Documents: docs, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{`PolicyRoute "lan" spec.rules: the next hop ` + address + ` is no unicast address of a router, or is an IPv4-mapped IPv6 address; give the router's address, an IPv4 one as IPv4`}
			if got := messages(failures); !slices.Equal(got, want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
