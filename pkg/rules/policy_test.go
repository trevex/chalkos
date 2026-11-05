package rules

import (
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// policyLAN is a LAN router's config with policies into a VRF, a table and next hops: the NextHop
// b4 named by two rules, which share its table, and b6 of IPv6.
func policyLAN(doc func(kind, name, spec string) config.Document) []config.Document {
	return []config.Document{
		doc("Router", "router", `{"management": {"interfaces": ["lan0"]}}`),
		doc("FirewallZone", "lan", `{"interfaces": ["lan0"]}`),
		doc("AddressSet", "guests", `{"family": "ipv4", "prefixes": ["192.0.2.64/26"]}`),
		doc("VRF", "transit", `{"table": 100, "interfaces": ["vt0"]}`),
		doc("NextHop", "b4", `{"address": "198.51.100.130", "interface": "wanb0", "table": 200}`),
		doc("NextHop", "b6", `{"address": "2001:db8:b::2", "interface": "wanb0", "table": 201}`),
		doc("PolicyRoute", "lan", `{"in": {"zone": "lan"}, "rules": [
			{"match": {"source": {"set": "guests"}, "protocol": "tcp", "destinationPorts": [443]}, "to": {"vrf": "transit"}},
			{"match": {"dscp": [46]}, "to": {"vrf": "transit"}},
			{"match": {"destination": {"cidrs": ["192.0.2.0/24", "2001:db8:1::/64"]}}, "to": {"table": 254}}]}`),
		doc("PolicyRoute", "servers", `{"in": {"interfaces": ["lan0"]}, "rules": [
			{"match": {"source": {"cidrs": ["192.0.2.32/27"]}}, "to": {"nextHop": "b4"}},
			{"match": {"source": {"cidrs": ["192.0.2.48/28"]}}, "to": {"nextHop": "b4"}},
			{"match": {"family": "ipv6", "source": {"cidrs": ["2001:db8:1::20/123"]}}, "to": {"nextHop": "b6"}}]}`),
	}
}

// The PolicyRoute rules accept a LAN router's policies into a VRF, a table and next hops, and
// refuse an in of a zone and interfaces, an empty match, a set beside ranges, addresses of another
// family, ports without a protocol, a to of no target or of two, a next hop of another family, and
// the local table, a VRF's or a NextHop's as a table.
func TestPolicyRouteRules(t *testing.T) {
	set, doc := routerRules(t)
	lan := policyLAN(doc)
	route := func(name, spec string) config.Document { return doc("PolicyRoute", name, spec) }
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"a LAN router", lan, nil},
		{"every link", replaced(lan, route("all", `{"rules": [{"match": {"source": {"cidrs": ["0.0.0.0/0"]}}, "to": {"table": 100000}}]}`)), nil},
		{"a zone and interfaces", replaced(lan, route("lan", `{"in": {"zone": "lan", "interfaces": ["lan0"]}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)),
			[]string{"PolicyRoute lan: in names a zone or interfaces, one of them; leave in out for traffic from every link"}},
		{"an empty in", replaced(lan, route("lan", `{"in": {}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)),
			[]string{"PolicyRoute lan: in names a zone or interfaces, one of them; leave in out for traffic from every link"}},
		// The output router-nftables refuses an interface of a VRF's device, by name and by glob.
		{"a VRF's device", replaced(lan, route("lan", `{"in": {"interfaces": ["lan0", "transit"]}, "rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`)), nil},
		{"an empty match", replaced(lan, route("lan", `{"rules": [{"match": {}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule has an empty match, which would take every packet; to send every packet, say so, such as with source: {cidrs: [0.0.0.0/0]}`}},
		{"a set beside ranges", replaced(lan, route("lan", `{"rules": [{"match": {"source": {"set": "guests", "cidrs": ["192.0.2.0/24"]}}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's source or destination names both set and cidrs, or neither; give one, or leave the field out to match every address`}},
		{"a family and a set of the other", replaced(lan, route("lan", `{"rules": [{"match": {"family": "ipv6", "source": {"set": "guests"}}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule has addresses or a set of another family than its own`,
				`PolicyRoute "lan" spec.rules: a rule matches no packet: its family, protocol, addresses and sets leave neither IPv4 nor IPv6`}},
		{"ports without a protocol", replaced(lan, route("lan", `{"rules": [{"match": {"destinationPorts": [443]}, "to": {"vrf": "transit"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule has ports without the protocol tcp, udp or sctp`}},
		{"no target", replaced(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's to names a VRF, a table or a next hop, one of them`}},
		{"two targets", replaced(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit", "nextHop": "b4"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's to names a VRF, a table or a next hop, one of them`}},
		{"a next hop of the other family", replaced(lan, route("lan", `{"rules": [{"match": {"source": {"cidrs": ["2001:db8:1::/64"]}}, "to": {"nextHop": "b4"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's next hop is of another family than the packets the rule matches`}},
		{"a next hop of a family the match leaves out", replaced(lan, route("lan", `{"rules": [{"match": {"protocol": "icmp"}, "to": {"nextHop": "b6"}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's next hop is of another family than the packets the rule matches`}},
		{"the local table", replaced(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 255}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table`}},
		{"a VRF's table", replaced(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 100}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table`}},
		{"a NextHop's table", replaced(lan, route("lan", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 201}}]}`)),
			[]string{`PolicyRoute "lan" spec.rules: a rule's table 201 is the table of NextHop b6; name the NextHop in nextHop, or take another table`}},
	} {
		t.Run(tc.name, func(t *testing.T) { wantFailures(t, set, tc.docs, tc.want) })
	}
}
