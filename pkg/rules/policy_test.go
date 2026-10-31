package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// The PolicyRoute rules accept a LAN router's policies into a VRF, a table and next hops, and
// refuse an in of a zone and interfaces, a zone, set or VRF the config lacks, an in of a VRF's
// device, an empty match, a set beside ranges, addresses of another family, ports without a
// protocol, a to of no target or of two, a next hop of another family, the local table or a VRF's
// as a table, a next hop's table of the kernel's or of a VRF, and a next hop's table that another
// next hop or a rule's table takes.
func TestPolicyRouteRules(t *testing.T) {
	set, doc := routerRules(t)
	lan := []config.Document{
		doc("Router", "router", `{"management": {"interfaces": ["lan0"]}}`),
		doc("FirewallZone", "lan", `{"interfaces": ["lan0"]}`),
		doc("AddressSet", "guests", `{"family": "ipv4", "prefixes": ["192.0.2.64/26"]}`),
		doc("VRF", "transit", `{"table": 100, "interfaces": ["vt0"]}`),
		doc("PolicyRoute", "lan", `{"in": {"zone": "lan"}, "rules": [
			{"match": {"source": ["set:guests"], "protocol": "tcp", "destinationPorts": [443]}, "to": {"vrf": "transit"}},
			{"match": {"dscp": [46]}, "to": {"vrf": "transit"}},
			{"match": {"destination": ["192.0.2.0/24", "2001:db8:1::/64"]}, "to": {"table": 254}}]}`),
		doc("PolicyRoute", "servers", `{"in": {"interfaces": ["lan0"]}, "rules": [
			{"match": {"source": ["192.0.2.32/27"]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 200}}},
			{"match": {"source": ["192.0.2.48/28"]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 200}}},
			{"match": {"family": "ipv6", "source": ["2001:db8:1::20/123"]}, "to": {"nextHop": {"address": "2001:db8:b::2", "interface": "wanb0", "table": 201}}}]}`),
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
		{"every link", replace(lan, route("all", `{"rules": [{"match": {"source": ["0.0.0.0/0"]}, "to": {"table": 100000}}]}`)), nil},
		{"a zone and interfaces", replace(lan, route("lan", `{"in": {"zone": "lan", "interfaces": ["lan0"]}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)),
			[]string{"PolicyRoute lan: in names a zone or interfaces, one of them; leave in out for traffic from every link"}},
		{"an empty in", replace(lan, route("lan", `{"in": {}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)),
			[]string{"PolicyRoute lan: in names a zone or interfaces, one of them; leave in out for traffic from every link"}},
		{"a zone the config lacks", replace(lan, route("lan", `{"in": {"zone": "dmz"}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)),
			[]string{"PolicyRoute lan names the zone dmz, which no FirewallZone document declares"}},
		{"a VRF's device", replace(lan, route("lan", `{"in": {"interfaces": ["lan0", "transit"]}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)),
			[]string{"PolicyRoute lan: transit is the device of VRF transit; name the VRF's links, or a zone of the VRF"}},
		{"an empty match", replace(lan, route("lan", `{"rules": [{"match": {}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule has an empty match, which would take every packet; to send every packet, say so, such as with source: [0.0.0.0/0]`}},
		{"a set beside ranges", replace(lan, route("lan", `{"rules": [{"match": {"source": ["set:guests", "192.0.2.0/24"]}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's source or destination holds set:<name> beside other entries; a set stands alone, so give the other ranges a rule of their own or add them to the set`}},
		{"a set the config lacks", replace(lan, route("lan", `{"rules": [{"match": {"source": ["set:staff"]}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule names an AddressSet that the config does not hold`}},
		{"a family and a set of the other", replace(lan, route("lan", `{"rules": [{"match": {"family": "ipv6", "source": ["set:guests"]}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule has addresses or a set of another family than its own`,
				`PolicyRoute "lan" spec.rules: a rule matches no packet: its family, protocol, addresses and sets leave neither IPv4 nor IPv6`}},
		{"ports without a protocol", replace(lan, route("lan", `{"rules": [{"match": {"destinationPorts": [443]}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule has ports without the protocol tcp, udp or sctp`}},
		{"no target", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's to names a VRF, a table or a next hop, one of them`}},
		{"two targets", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit", "table": 300}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's to names a VRF, a table or a next hop, one of them`}},
		{"a next hop of the other family", replace(lan, route("lan", `{"rules": [{"match": {"source": ["2001:db8:1::/64"]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 202}}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's next hop is of another family than the packets the rule matches`}},
		{"a VRF the config lacks", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "guest"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule names a VRF that no VRF document declares`}},
		{"the local table", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 255}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table`}},
		{"a VRF's table", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 100}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table`}},
		{"a next hop in main", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 254}}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a next hop's table is 253, 254 or 255, the kernel's own, or a VRF's table; give the next hop a table of its own`}},
		{"a next hop in a VRF's table", replace(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 100}}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a next hop's table is 253, 254 or 255, the kernel's own, or a VRF's table; give the next hop a table of its own`}},
		{"two next hops in one table", replace(lan, route("more", `{"rules": [{"match": {"dscp": [10]}, "to": {"nextHop": {"address": "198.51.100.131", "interface": "wanb0", "table": 200}}}]}`)),
			[]string{"PolicyRoute more: the next hop 198.51.100.131 takes table 200, which another next hop or a rule's table takes too; give each next hop a table of its own"}},
		{"a next hop's table as a rule's", replace(lan, route("more", `{"rules": [{"match": {"dscp": [10]}, "to": {"table": 201}}]}`)),
			[]string{"PolicyRoute servers: the next hop 2001:db8:b::2 takes table 201, which another next hop or a rule's table takes too; give each next hop a table of its own"}},
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
