package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// policyDocs are a router's documents with the PolicyRoute documents given as name and spec: a
// LAN zone on lan0, a VRF transit of table 100 on vt0, and Networks of lan0, wanb0 and vt0.
func policyDocs(t *testing.T, routes ...string) map[string][]any {
	t.Helper()
	var docs []any
	for i := 0; i+1 < len(routes); i += 2 {
		docs = append(docs, routerDoc(t, "PolicyRoute", routes[i], routes[i+1]))
	}
	return map[string][]any{
		"FirewallZone": {routerDoc(t, "FirewallZone", "lan", `{"interfaces": ["lan0"], "vrfs": []}`)},
		"VRF":          {routerDoc(t, "VRF", "transit", `{"table": 100, "interfaces": ["vt0"]}`)},
		"Network": {
			document(t, "Network", "10-lan", `{"match": {"name": "lan0"}}`),
			document(t, "Network", "21-wanb", `{"match": {"name": "wanb0"}}`),
			document(t, "Network", "30-transit", `{"match": {"name": "vt0"}}`),
		},
		"PolicyRoute": docs,
	}
}

// policyInput is the input of the router outputs for the documents.
func policyInput(t *testing.T, docs map[string][]any) string {
	t.Helper()
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	return string(input)
}

// nextHopRoute is a PolicyRoute's spec of one rule, for DSCP 46, to the next hop.
func nextHopRoute(address, link string, table int) string {
	b, _ := json.Marshal(map[string]any{"rules": []any{map[string]any{
		"match": map[string]any{"dscp": []int{46}},
		"to":    map[string]any{"nextHop": map[string]any{"address": address, "interface": link, "table": table}},
	}}})
	return string(b)
}

// The firewall's renderer refuses what the PolicyRoute rules refuse, should a document bypass
// them, and what it alone can see: a glob of in that matches a VRF's device or a NAT64's link, a
// zone without links, more than 255 tables, and values that would add syntax to the ruleset.
func TestPolicyRouteRefusals(t *testing.T) {
	nft := []string{"router-nftables", "50000"}
	dscp := `"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]`
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"in of a zone and interfaces":  {policyDocs(t, "a", `{"in": {"zone": "lan", "interfaces": ["lan0"]}, `+dscp+`}`), "PolicyRoute a: in names a zone or interfaces, one of them"},
		"a zone the config lacks":      {policyDocs(t, "a", `{"in": {"zone": "dmz", "interfaces": []}, `+dscp+`}`), "PolicyRoute a: the zone dmz is no FirewallZone"},
		"a glob of a VRF's device":     {policyDocs(t, "a", `{"in": {"zone": null, "interfaces": ["tran*"]}, `+dscp+`}`), "PolicyRoute a: tran* matches transit, the device of VRF transit"},
		"an interface that ends a set": {policyDocs(t, "a", `{"in": {"zone": null, "interfaces": ["lan0\" } accept; iifname { \"x"]}, `+dscp+`}`), "is no interface name or glob"},
		"a glob of a NAT64's link": {func() map[string][]any {
			d := policyDocs(t, "a", `{"in": {"zone": null, "interfaces": ["n64-*"]}, `+dscp+`}`)
			d["NAT64"] = []any{routerDoc(t, "NAT64", "edge", `{"prefix": "64:ff9b::/96", "pool4": []}`)}
			return d
		}(), "PolicyRoute a: n64-* matches n64-edge, the link of NAT64 edge"},
		"a zone without links": {func() map[string][]any {
			d := policyDocs(t, "a", `{"in": {"zone": "core", "interfaces": []}, `+dscp+`}`)
			d["VRF"] = append(d["VRF"], routerDoc(t, "VRF", "core", `{"table": 400, "interfaces": []}`))
			d["FirewallZone"] = append(d["FirewallZone"], routerDoc(t, "FirewallZone", "core", `{"interfaces": [], "vrfs": ["core"]}`))
			return d
		}(), "PolicyRoute a: the zone core holds no link"},
		"an empty match":                    {policyDocs(t, "a", `{"rules": [{"match": {}, "to": {"vrf": "transit"}}]}`), "PolicyRoute a rule 1: the match is empty"},
		"two targets":                       {policyDocs(t, "a", `{"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit", "table": 300}}]}`), "PolicyRoute a rule 1: to names a VRF, a table or a next hop, one of them"},
		"a VRF the config lacks":            {policyDocs(t, "a", `{"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "guest"}}]}`), "PolicyRoute a rule 1: the VRF guest does not exist"},
		"the local table":                   {policyDocs(t, "a", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 255}}]}`), "PolicyRoute a rule 1: table 255 is none a policy sends traffic to"},
		"a VRF's table":                     {policyDocs(t, "a", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 100}}]}`), "PolicyRoute a rule 1: table 100 is VRF transit's; name the VRF"},
		"a next hop in main":                {policyDocs(t, "a", nextHopRoute("198.51.100.130", "wanb0", 254)), "PolicyRoute a rule 1: table 254 of the next hop is one of the kernel's"},
		"a next hop in a VRF's table":       {policyDocs(t, "a", nextHopRoute("198.51.100.130", "wanb0", 100)), "PolicyRoute a rule 1: table 100 of the next hop is VRF transit's"},
		"a next hop with a zone":            {policyDocs(t, "a", nextHopRoute("fe80::1%wanb0", "wanb0", 200)), `PolicyRoute a rule 1: "fe80::1%wanb0" is no next hop's address`},
		"a next hop on lo":                  {policyDocs(t, "a", nextHopRoute("198.51.100.130", "lo", 200)), `PolicyRoute a rule 1: "lo" is no link of a next hop`},
		"a next hop's link that adds a key": {policyDocs(t, "a", nextHopRoute("198.51.100.130", "wanb0\nTable=1", 200)), `"wanb0\nTable=1" is no link of a next hop`},
		"two next hops in a table":          {policyDocs(t, "a", nextHopRoute("198.51.100.130", "wanb0", 200), "b", nextHopRoute("198.51.100.131", "wanb0", 200)), "PolicyRoute b rule 1: table 200 is another next hop's"},
		"a next hop's table as a rule's":    {policyDocs(t, "a", nextHopRoute("198.51.100.130", "wanb0", 200), "b", `{"rules": [{"match": {"dscp": [10]}, "to": {"table": 200}}]}`), "PolicyRoute b rule 1: table 200 is another next hop's"},
		"a next hop of the other family":    {policyDocs(t, "a", `{"rules": [{"match": {"source": ["2001:db8:1::/64"]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 200}}}]}`), "PolicyRoute a rule 1: the match leaves no family, or none of its next hop"},
		"a family against the next hop":     {policyDocs(t, "a", `{"rules": [{"match": {"family": "ipv6"}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 200}}}]}`), "PolicyRoute a rule 1: the next hop 198.51.100.130 is of another family than ipv6"},
		"a DSCP value above 63":             {policyDocs(t, "a", `{"rules": [{"match": {"dscp": [64]}, "to": {"vrf": "transit"}}]}`), "PolicyRoute a rule 1: dscp 64 is no value from 0 to 63"},
		"more than 255 tables": {func() map[string][]any {
			var rules []string
			for i := range 256 {
				rules = append(rules, `{"match": {"dscp": [46]}, "to": {"table": `+strconv.Itoa(1000+i)+`}}`)
			}
			return policyDocs(t, "a", `{"rules": [`+strings.Join(rules, ", ")+`]}`)
		}(), "send traffic to 256 tables, more than the 255 their marks tell apart"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, nft, c.docs, c.want) })
	}
}

// The chain policy-routing keeps a mark another pass set and leaves a packet to an address of the
// router unmarked, and each rule marks with its target table's mark, (index+1)<<16 in the order
// of the tables, rules of one table alike. A next hop of IPv4 marks IPv4 alone. The VRFs' devices accept a marked packet in the reverse-path filter, and
// a ruleset without PolicyRoute documents marks nothing.
func TestPolicyRouteChain(t *testing.T) {
	docs := policyDocs(t,
		"a", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 200}}}, {"match": {"source": ["192.0.2.0/24"]}, "to": {"vrf": "transit"}}]}`,
		"b", `{"in": {"zone": "lan", "interfaces": []}, "rules": [{"match": {"dscp": [10]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 200}}}]}`)
	ruleset := renderedFile(t, []string{"router-nftables", "50000"}, policyInput(t, docs), rulesetFile)
	for _, want := range []string{
		"\t\ttype filter hook prerouting priority mangle + 10; policy drop;\n" +
			"\t\tiifname { \"transit\" } meta mark & 0x00ff0000 != 0 accept\n\t\tmeta nfproto ipv4 udp sport",
		"\tchain policy-routing {\n\t\ttype filter hook prerouting priority mangle + 20; policy accept;\n" +
			"\t\tmeta mark & 0x00ff0000 != 0 return\n" +
			"\t\tfib daddr type local return\n" +
			"\t\tip dscp { 46 } counter meta mark set meta mark & 0xff00ffff | 0x00020000 return comment \"a 1\"\n" +
			"\t\tip saddr { 192.0.2.0/24 } counter meta mark set meta mark & 0xff00ffff | 0x00010000 return comment \"a 2\"\n" +
			"\t\tiifname { \"lan0\" } ip dscp { 10 } counter meta mark set meta mark & 0xff00ffff | 0x00020000 return comment \"b 1\"\n\t}\n",
	} {
		if !strings.Contains(ruleset, want) {
			t.Errorf("the ruleset lacks %q:\n%s", want, ruleset)
		}
	}
	if strings.Contains(ruleset, "ip6 dscp") {
		t.Errorf("a next hop of IPv4 marks IPv6:\n%s", ruleset)
	}
	delete(docs, "PolicyRoute")
	if ruleset := renderedFile(t, []string{"router-nftables", "50000"}, policyInput(t, docs), rulesetFile); strings.Contains(ruleset, "meta mark") || strings.Contains(ruleset, "policy-routing") {
		t.Errorf("a ruleset without PolicyRoute documents marks packets:\n%s", ruleset)
	}
}

// The units' renderer refuses a next hop's link that no Network names, or whose Network matches
// other links too, a Network that may match lo by other [Match] keys and that sorts before the
// policy routing's unit or names lo, and a Network of the name of the policy routing's unit.
func TestPolicyRouteUnitRefusals(t *testing.T) {
	networkd := []string{"router-networkd", networkFixture}
	dscp := `{"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a next hop's link without a Network": {policyDocs(t, "a", nextHopRoute("198.51.100.130", "wanc0", 200)), "PolicyRoute a rule 1: the next hop 198.51.100.130 is on wanc0, which no Network document names"},
		"a Network of the next hop's link and others": {func() map[string][]any {
			d := policyDocs(t, "a", nextHopRoute("198.51.100.130", "wanb0", 200))
			d["Network"][1] = document(t, "Network", "21-wanb", `{"match": {"name": "wan*"}}`)
			return d
		}(), "PolicyRoute a rule 1: Network 21-wanb matches wanb0 and other links, which would take its policy routing too"},
		"a Network of lo by other keys": {func() map[string][]any {
			d := policyDocs(t, "a", dscp)
			d["Network"] = append(d["Network"], document(t, "Network", "05-loopback", `{"match": {"name": "lo", "type": "loopback"}}`))
			return d
		}(), "PolicyRoute a: Network 05-loopback may match lo by other [Match] keys"},
		"a Network of lo by other keys and two PolicyRoutes": {func() map[string][]any {
			d := policyDocs(t, "a", dscp, "b", `{"rules": []}`, "c", dscp)
			d["Network"] = append(d["Network"], document(t, "Network", "05-loopback", `{"match": {"name": "lo", "type": "loopback"}}`))
			return d
		}(), "PolicyRoute a, c: Network 05-loopback may match lo by other [Match] keys"},
		"a Network of other keys alone before the unit": {func() map[string][]any {
			d := policyDocs(t, "a", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 300}}]}`)
			delete(d, "VRF")
			d["Network"] = append(d["Network"], document(t, "Network", "00-nic", `{"match": {"macAddress": "02:00:00:00:00:01"}}`))
			return d
		}(), "PolicyRoute a: Network 00-nic may match lo by other [Match] keys"},
		"a Network of the unit's name": {func() map[string][]any {
			d := policyDocs(t, "a", dscp)
			d["Network"] = append(d["Network"], document(t, "Network", "00-policy-routing", `{"match": {"name": "dummy0"}}`))
			return d
		}(), "Network 00-policy-routing takes the name of the policy routing's unit"},
		"a next hop's link that adds a key": {policyDocs(t, "a", nextHopRoute("198.51.100.130", "wanb0\nTable=1", 200)), `"wanb0\nTable=1" is no link of a next hop`},
	} {
		t.Run(name, func(t *testing.T) { refused(t, networkd, c.docs, c.want) })
	}
}

// A Network of the config that matches lo alone takes the kernel's rules and the next hops'
// unreachable defaults after its own sections, and a next hop's route goes into the Network of
// its link. Without a Network of lo, 00-policy-routing holds them, also beside a Network that
// sorts after it and matches by other [Match] keys alone; without PolicyRoute documents the units
// are those of the VRFs alone.
func TestPolicyRouteUnits(t *testing.T) {
	docs := policyDocs(t,
		"a", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": {"address": "198.51.100.130", "interface": "wanb0", "table": 200}}}, {"match": {"source": ["192.0.2.0/24"]}, "to": {"vrf": "transit"}}]}`)
	units := func() string {
		t.Helper()
		return renderedFiles(t, []string{"router-networkd", networkFixture}, policyInput(t, docs))
	}
	rules := "[RoutingPolicyRule]\nFamily=both\nFirewallMark=0x10000/0xff0000\nPriority=900\nTable=100\n\n" +
		"[RoutingPolicyRule]\nFamily=both\nFirewallMark=0x20000/0xff0000\nPriority=900\nTable=200\n\n"
	unreachable := "[Route]\nDestination=0.0.0.0/0\nMetric=4278198272\nTable=200\nType=unreachable\n\n" +
		"[Route]\nDestination=::/0\nMetric=4278198272\nTable=200\nType=unreachable\n\n"
	hop := "# 21-wanb.network\n[Match]\nName=wanb0\n\n[Route]\nGateway=198.51.100.130\nTable=200\n\n"
	if got := units(); !strings.Contains(got, "# 00-policy-routing.network\n[Match]\nName=lo\n\n"+unreachable+rules) || !strings.Contains(got, hop) {
		t.Errorf("the units without a Network of lo:\n%s", got)
	}
	docs["Network"] = append(docs["Network"], document(t, "Network", "50-nic", `{"match": {"macAddress": "02:00:00:00:00:01"}}`))
	if got := units(); !strings.Contains(got, "# 00-policy-routing.network\n[Match]\nName=lo\n\n"+unreachable+rules) {
		t.Errorf("the units with a Network of other keys alone after 00-policy-routing:\n%s", got)
	}
	docs["Network"] = append(docs["Network"], document(t, "Network", "05-lo", `{"match": {"name": "lo"}, "address": {"address": "192.0.2.255/32"}}`))
	if got := units(); !strings.Contains(got, "# 05-lo.network\n[Match]\nName=lo\n\n[Address]\nAddress=192.0.2.255/32\n\n"+unreachable+rules) ||
		!strings.Contains(got, hop) || strings.Contains(got, "00-policy-routing") {
		t.Errorf("the units with a Network of lo:\n%s", got)
	}
	delete(docs, "PolicyRoute")
	if got := units(); strings.Contains(got, "RoutingPolicyRule") || strings.Contains(got, "Gateway=") || strings.Contains(got, "Table=200") {
		t.Errorf("the units without PolicyRoute documents:\n%s", got)
	}
}
