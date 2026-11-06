package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// policyDocs are a router's documents with the PolicyRoute documents given as name and spec: a
// LAN zone on lan0, a VRF transit of table 100 on vt0, Networks of lan0, wanb0 and vt0, and the
// NextHop b4 of table 200 on wanb0.
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
		"NextHop":     {hopDoc(t, "b4", "198.51.100.130", "wanb0", 200)},
		"PolicyRoute": docs,
	}
}

// hopDoc is a NextHop document.
func hopDoc(t *testing.T, name, address, link string, table int) any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"address": address, "interface": link, "table": table})
	return routerDoc(t, "NextHop", name, string(b))
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

// nextHopRoute is a PolicyRoute's spec of one rule, for DSCP 46, to the NextHop.
func nextHopRoute(hop string) string {
	return `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": "` + hop + `"}}]}`
}

// The firewall's renderer refuses more than 255 tables, which its marks cannot tell apart. It
// guards, with an internal error, values that would add syntax to the ruleset, a zone of no link,
// whose empty set nftables would not load, a rule of no target, a NextHop the input lacks, a
// NextHop's address of a zone and table 0, should a document bypass the schemas, the rules, the
// reference check and the validator router.
func TestPolicyRouteRefusals(t *testing.T) {
	nft := []string{"router-nftables", "50000"}
	dscp := `"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]`
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"an interface that ends a set": {policyDocs(t, "a", `{"in": {"zone": null, "interfaces": ["lan0\" } accept; iifname { \"x"]}, `+dscp+`}`), `internal error: PolicyRoute a: "lan0\" } accept; iifname { \"x" is no interface name or glob`},
		"a zone without links": {func() map[string][]any {
			d := policyDocs(t, "a", `{"in": {"zone": "core", "interfaces": []}, `+dscp+`}`)
			d["VRF"] = append(d["VRF"], routerDoc(t, "VRF", "core", `{"table": 400, "interfaces": []}`))
			d["FirewallZone"] = append(d["FirewallZone"], routerDoc(t, "FirewallZone", "core", `{"interfaces": [], "vrfs": ["core"]}`))
			return d
		}(), "internal error: PolicyRoute a: the zone core holds no link"},
		"no target":         {policyDocs(t, "a", `{"rules": [{"match": {"dscp": [46]}, "to": {}}]}`), "internal error: PolicyRoute a rule 1: to names no target"},
		"table 0":           {policyDocs(t, "a", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 0}}]}`), "internal error: PolicyRoute a rule 1: table 0 is no table"},
		"a missing NextHop": {policyDocs(t, "a", nextHopRoute("b6")), `internal error: PolicyRoute a rule 1: the input holds no NextHop "b6", which a reference names`},
		"a NextHop with a zone": {func() map[string][]any {
			d := policyDocs(t, "a", nextHopRoute("b4"))
			d["NextHop"] = []any{hopDoc(t, "b4", "fe80::1%wanb0", "wanb0", 200)}
			return d
		}(), `internal error: NextHop b4: "fe80::1%wanb0" is no address without a zone`},
		"a NextHop's table 0": {func() map[string][]any {
			d := policyDocs(t, "a", nextHopRoute("b4"))
			d["NextHop"] = []any{hopDoc(t, "b4", "198.51.100.130", "wanb0", 0)}
			return d
		}(), "internal error: NextHop b4: table 0 is no table"},
		"more than 255 tables": {func() map[string][]any {
			var rules []string
			for i := range 256 {
				rules = append(rules, `{"match": {"dscp": [46]}, "to": {"table": `+strconv.Itoa(1000+i)+`}}`)
			}
			return policyDocs(t, "a", `{"rules": [`+strings.Join(rules, ", ")+`]}`)
		}(), "PolicyRoute/a: spec.rules[255].to: the PolicyRoute documents send traffic to 256 tables, more than the 255 their marks tell apart"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, nft, c.docs, c.want) })
	}
}

// The chain policy-routing keeps a mark another pass set and leaves a packet to an address of the
// router unmarked, and each rule marks with its target table's mark, (index+1)<<16 in the order
// of the tables, rules of one table alike: two documents' rules that name one NextHop share its
// mark, and a NextHop no rule names takes none. A NextHop of IPv4 marks IPv4 alone. The VRFs'
// devices accept a marked packet in the reverse-path filter, and a ruleset without PolicyRoute
// documents marks nothing.
func TestPolicyRouteChain(t *testing.T) {
	docs := policyDocs(t,
		"a", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": "b4"}}, {"match": {"source": {"cidrs": ["192.0.2.0/24"]}}, "to": {"vrf": "transit"}}]}`,
		"b", `{"in": {"zone": "lan", "interfaces": []}, "rules": [{"match": {"dscp": [10]}, "to": {"nextHop": "b4"}}]}`)
	docs["NextHop"] = append(docs["NextHop"], hopDoc(t, "spare", "198.51.100.131", "wanb0", 150))
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
		t.Errorf("a NextHop of IPv4 marks IPv6:\n%s", ruleset)
	}
	delete(docs, "PolicyRoute")
	if ruleset := renderedFile(t, []string{"router-nftables", "50000"}, policyInput(t, docs), rulesetFile); strings.Contains(ruleset, "meta mark") || strings.Contains(ruleset, "policy-routing") {
		t.Errorf("a ruleset without PolicyRoute documents marks packets:\n%s", ruleset)
	}
}

// The units' renderer refuses a NextHop's link that no Network names, or whose Network matches
// other links too, whether or not a rule names the NextHop, a Network that may match lo by other
// [Match] keys and that sorts before the policy routing's unit or names lo, and a Network of the
// name of the policy routing's unit.
func TestPolicyRouteUnitRefusals(t *testing.T) {
	networkd := []string{"router-networkd", networkFixture}
	dscp := `{"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]}`
	// withoutHops is docs without NextHop documents.
	withoutHops := func(d map[string][]any) map[string][]any {
		delete(d, "NextHop")
		return d
	}
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a NextHop's link without a Network": {func() map[string][]any {
			d := policyDocs(t, "a", nextHopRoute("b4"))
			d["NextHop"] = []any{hopDoc(t, "b4", "198.51.100.130", "wanc0", 200)}
			return d
		}(), "NextHop/b4: spec.interface: NextHop b4 is on wanc0, which no Network document names"},
		"a link without a Network of a NextHop no rule names": {func() map[string][]any {
			d := policyDocs(t, "a", dscp)
			d["NextHop"] = []any{hopDoc(t, "b4", "198.51.100.130", "wanc0", 200)}
			return d
		}(), "NextHop/b4: spec.interface: NextHop b4 is on wanc0, which no Network document names"},
		"a Network of the NextHop's link and others": {func() map[string][]any {
			d := policyDocs(t, "a", nextHopRoute("b4"))
			d["Network"][1] = document(t, "Network", "21-wanb", `{"match": {"name": "wan*"}}`)
			return d
		}(), "Network/21-wanb: spec.match.name: Network 21-wanb matches wanb0 and other links, which would take its policy routing too"},
		"a Network word of the NextHop's link and others": {func() map[string][]any {
			d := policyDocs(t, "a", nextHopRoute("b4"))
			d["Network"][1] = document(t, "Network", "21-wanb", `{"match": [{"name": ["wanb0", "wan*"]}]}`)
			return d
		}(), "Network/21-wanb: spec.match[0].name[1]: Network 21-wanb matches wanb0 and other links, which would take its policy routing too"},
		"a Network of lo by other keys": {func() map[string][]any {
			d := withoutHops(policyDocs(t, "a", dscp))
			d["Network"] = append(d["Network"], document(t, "Network", "05-loopback", `{"match": {"name": "lo", "type": "loopback"}}`))
			return d
		}(), "Network/05-loopback: spec.match: Network 05-loopback may match lo by other [Match] keys"},
		"a Network of lo by other keys, PolicyRoutes and a NextHop": {func() map[string][]any {
			d := policyDocs(t, "a", dscp, "b", `{"rules": []}`, "c", dscp)
			d["Network"] = append(d["Network"], document(t, "Network", "05-loopback", `{"match": {"name": "lo", "type": "loopback"}}`))
			return d
		}(), "Network/05-loopback: spec.match: Network 05-loopback may match lo by other [Match] keys"},
		"a Network of lo by other keys and a NextHop alone": {func() map[string][]any {
			d := policyDocs(t)
			d["Network"] = append(d["Network"], document(t, "Network", "05-loopback", `{"match": {"name": "lo", "type": "loopback"}}`))
			return d
		}(), "Network/05-loopback: spec.match: Network 05-loopback may match lo by other [Match] keys"},
		"a Network of other keys alone before the unit": {func() map[string][]any {
			d := withoutHops(policyDocs(t, "a", `{"rules": [{"match": {"dscp": [46]}, "to": {"table": 300}}]}`))
			delete(d, "VRF")
			d["Network"] = append(d["Network"], document(t, "Network", "00-nic", `{"match": {"macAddress": "02:00:00:00:00:01"}}`))
			return d
		}(), "Network/00-nic: spec.match: Network 00-nic may match lo by other [Match] keys"},
		"a Network of the unit's name": {func() map[string][]any {
			d := policyDocs(t, "a", dscp)
			d["Network"] = append(d["Network"], document(t, "Network", "00-policy-routing", `{"match": {"name": "dummy0"}}`))
			return d
		}(), "Network/00-policy-routing: metadata.name: Network 00-policy-routing takes the name of the policy routing's unit"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, networkd, c.docs, c.want) })
	}
}

// A Network of the config that matches lo alone takes the kernel's rules and the NextHops'
// unreachable defaults after its own sections, and a NextHop's route goes into the Network of its
// link. Two rules that name one NextHop render one rule, one table and one route. Without a
// Network of lo, 00-policy-routing holds them, also beside a Network that sorts after it and
// matches by other [Match] keys alone. A NextHop that no rule names keeps its table and route
// without a rule; without PolicyRoute and NextHop documents the units are those of the VRFs alone.
func TestPolicyRouteUnits(t *testing.T) {
	docs := policyDocs(t,
		"a", `{"rules": [{"match": {"dscp": [46]}, "to": {"nextHop": "b4"}}, {"match": {"source": {"cidrs": ["192.0.2.0/24"]}}, "to": {"vrf": "transit"}}]}`,
		"b", `{"rules": [{"match": {"dscp": [10]}, "to": {"nextHop": "b4"}}]}`)
	units := func() string {
		t.Helper()
		return renderedFiles(t, []string{"router-networkd", networkFixture}, policyInput(t, docs))
	}
	rules := "[RoutingPolicyRule]\nFamily=both\nFirewallMark=0x10000/0xff0000\nPriority=900\nTable=100\n\n" +
		"[RoutingPolicyRule]\nFamily=both\nFirewallMark=0x20000/0xff0000\nPriority=900\nTable=200\n\n"
	unreachable := "[Route]\nDestination=0.0.0.0/0\nMetric=4278198272\nTable=200\nType=unreachable\n\n" +
		"[Route]\nDestination=::/0\nMetric=4278198272\nTable=200\nType=unreachable\n\n"
	hop := "# 21-wanb.network\n[Match]\nName=wanb0\n\n[Route]\nGateway=198.51.100.130\nTable=200\n\n"
	got := units()
	if !strings.Contains(got, "# 00-policy-routing.network\n[Match]\nName=lo\n\n"+unreachable+rules+"#") || !strings.Contains(got, hop+"#") {
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
	if got := units(); !strings.Contains(got, "# 05-lo.network\n[Match]\nName=lo\n\n[Address]\nAddress=192.0.2.255/32\n\n"+unreachable+"#") ||
		!strings.Contains(got, hop) || strings.Contains(got, "RoutingPolicyRule") {
		t.Errorf("the units of a NextHop no rule names:\n%s", got)
	}
	delete(docs, "NextHop")
	if got := units(); strings.Contains(got, "RoutingPolicyRule") || strings.Contains(got, "Gateway=") || strings.Contains(got, "Table=200") {
		t.Errorf("the units without PolicyRoute and NextHop documents:\n%s", got)
	}
}
