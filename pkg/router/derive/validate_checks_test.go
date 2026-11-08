package derive

import (
	"slices"
	"strings"
	"testing"
)

// validated runs the validator on docs, by kind, as a validator reads them, and returns its
// refusals as chalkd reports them.
func validated(t *testing.T, docs map[string][]any) ([]string, error) {
	t.Helper()
	rs, err := Validate(stdin(t, docs))
	var lines []string
	for _, r := range rs {
		lines = append(lines, r.String())
	}
	return lines, err
}

// refused wants the validator to refuse docs with want among its refusals, a refusal as chalkd
// reports it, whole.
func refused(t *testing.T, docs map[string][]any, want string) {
	t.Helper()
	rs, err := validated(t, docs)
	if err != nil || !slices.Contains(rs, want) {
		t.Errorf("refusals = %q, %v, want %q", rs, err, want)
	}
}

// guarded wants the validator to fail on docs with an error that holds want, as a guard's
// internal error does, and to refuse nothing.
func guarded(t *testing.T, docs map[string][]any, want string) {
	t.Helper()
	rs, err := validated(t, docs)
	if err == nil || !strings.Contains(err.Error(), want) || len(rs) > 0 {
		t.Errorf("refusals = %q, err = %v, want the error %q", rs, err, want)
	}
}

// refusedOrGuarded is guarded for a want of a guard, which starts with "internal error: ", and
// refused for any other.
func refusedOrGuarded(t *testing.T, docs map[string][]any, want string) {
	t.Helper()
	if strings.HasPrefix(want, "internal error: ") {
		guarded(t, docs, want)
	} else {
		refused(t, docs, want)
	}
}

// accepted wants the validator to accept docs.
func accepted(t *testing.T, docs map[string][]any) {
	t.Helper()
	if rs, err := validated(t, docs); err != nil || len(rs) > 0 {
		t.Errorf("refusals = %q, %v, want none", rs, err)
	}
}

// The validator reports every fault of a config it can see at once, each once, from several
// documents of several kinds: here a Network that names a VRF's device, two NAT64s whose pools
// share an address, a zone of a VRF's device and a resolver that listens on another VRF's address.
// A check that reads the links' VRFs waits for the VRFs' links to pass, as their refusals leave it
// nothing to read; the NAT64s' check, which does not read them, runs anyway.
func TestValidateReportsEveryFault(t *testing.T) {
	nat64 := func(name, pool string) any {
		return routerDoc(t, "NAT64", name, `{"prefix": "64:ff9b::/96", "vrf": "`+name+`", "pool4": [{"prefix": "`+pool+`", "ports": "61001-65535", "protocols": ["tcp"]}]}`)
	}
	docs := map[string][]any{
		"VRF": {
			routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3"]}`),
			routerDoc(t, "VRF", "lab", `{"table": 400, "interfaces": ["eth4"]}`),
		},
		"Network": {
			document(t, "Network", "10-eth0", `{"match": {"name": "eth0"}, "network": {"address": ["2001:db8:ffff::1/128"]}}`),
			document(t, "Network", "40-guest", `{"match": {"name": "eth3"}}`),
			document(t, "Network", "50-lab", `{"match": {"name": "eth4"}}`),
		},
		"NAT64":        {nat64("guest", "192.0.2.8/30"), nat64("lab", "192.0.2.10/32")},
		"FirewallZone": {routerDoc(t, "FirewallZone", "x", `{"interfaces": ["lab"], "vrfs": []}`)},
		"Resolver":     {routerDoc(t, "Resolver", "guest", `{"vrf": "guest", "listen": ["2001:db8:ffff::1"], "allow": ["2001:db8:3::/64"]}`)},
	}
	want := []string{
		"NAT64/lab: spec.pool4[0]: the entry shares addresses with the pool of NAT64 guest; give each NAT64 a pool of its own",
		"FirewallZone/x: spec.interfaces[0]: lab matches lab, the device of VRF lab; name the VRF in vrfs",
		"Resolver/guest: spec.listen[0]: the address 2001:db8:ffff::1 of listen belongs to links of Network 10-eth0 in VRF default alone, so the resolver of VRF guest never receives its queries; listen on an address of the VRF's links",
	}
	if rs, err := validated(t, docs); err != nil || !slices.Equal(rs, want) {
		t.Errorf("refusals = %q, %v, want %q", rs, err, want)
	}
	docs["Network"] = append(docs["Network"], document(t, "Network", "00-lab", `{"match": {"name": "lab"}}`))
	want = []string{
		"VRF/lab: metadata.name: a Network document names the link lab, the VRF's device",
		"Network/00-lab: metadata.name: Network 00-lab matches the device of VRF lab before 00-vrf-lab; rename it to sort after 00-vrf-lab",
		"NAT64/lab: spec.pool4[0]: the entry shares addresses with the pool of NAT64 guest; give each NAT64 a pool of its own",
	}
	if rs, err := validated(t, docs); err != nil || !slices.Equal(rs, want) {
		t.Errorf("refusals = %q, %v, want %q", rs, err, want)
	}
}

// vrfZoneDocs are a router's documents of three VRFs and their zones: guest of eth3 and eth4,
// whose links two zones name, internet of eth1 and tenant of eth2 and swp*, each a zone's VRF,
// and the zone lan of eth5 and vlan*, links of no VRF.
func vrfZoneDocs(t *testing.T) map[string][]any {
	t.Helper()
	zone := func(name, spec string) any { return routerDoc(t, "FirewallZone", name, spec) }
	return map[string][]any{
		"VRF": {
			routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"]}`),
			routerDoc(t, "VRF", "internet", `{"table": 100, "interfaces": ["eth1"]}`),
			routerDoc(t, "VRF", "tenant", `{"table": 200, "interfaces": ["eth2", "swp*"]}`),
		},
		"Network": {
			document(t, "Network", "10-mgmt", `{"match": {"name": "eth0 eth5"}}`),
			document(t, "Network", "20-internet", `{"match": {"name": "eth1"}}`),
			document(t, "Network", "30-tenant", `{"match": {"name": "eth2 swp1 swp2"}}`),
			document(t, "Network", "40-guest", `{"match": {"name": "eth3 eth4"}}`),
		},
		"FirewallZone": {
			zone("guest-lan", `{"interfaces": ["eth3"], "vrfs": []}`),
			zone("guest-wan", `{"interfaces": ["eth4"], "vrfs": []}`),
			zone("internet", `{"interfaces": [], "vrfs": ["internet"]}`),
			zone("lan", `{"interfaces": ["eth5", "vlan*"], "vrfs": []}`),
			zone("tenant", `{"interfaces": [], "vrfs": ["tenant"]}`),
		},
	}
}

// natRule is a NATRule document that masquerades the connections out of the interface i.
func natRule(t *testing.T, i string) any {
	t.Helper()
	return routerDoc(t, "NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": {"zone": null, "interface": "`+i+`"}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)
}

// The firewall's view of the links refuses a zone's interface or glob of a VRF's device, which
// the input and forward hooks see for each of the VRF's links, and an interface that may be a
// link of a VRF another zone holds, a zone or a conntrack zone of a VRF whose links nftables
// cannot match, a NAT rule's zone of no link and a NAT rule's interface that matches nothing but
// VRF devices, which translates nothing; a VRF's name matches its device alone, also where a glob
// of another VRF's links covers the name. A management interface of a VRF's device takes no
// connection to chalkd.
func TestFirewallRefusals(t *testing.T) {
	zone := func(name, spec string) any { return routerDoc(t, "FirewallZone", name, spec) }
	tenant := zone("tenant", `{"interfaces": [], "vrfs": ["tenant"]}`)
	with := func(zones ...any) map[string][]any {
		docs := vrfZoneDocs(t)
		docs["FirewallZone"] = zones
		return docs
	}
	unmatchable := with(tenant)
	unmatchable["Network"] = append(unmatchable["Network"], document(t, "Network", "50-lab", `{"match": {"name": "lab[12]"}, "network": {"vrf": "tenant"}}`))
	conntrack := vrfZoneDocs(t)
	conntrack["VRF"][0] = routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"], "conntrackZone": 1}`)
	conntrack["Network"] = append(conntrack["Network"], document(t, "Network", "50-lab", `{"match": {"name": "lab[12]"}, "network": {"vrf": "guest"}}`))
	empty := with(zone("x", `{"interfaces": [], "vrfs": ["empty"]}`))
	empty["VRF"] = append(empty["VRF"], routerDoc(t, "VRF", "empty", `{"table": 400, "interfaces": []}`))
	empty["NATRule"] = []any{routerDoc(t, "NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": {"zone": "x", "interface": null}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)}
	natOut := func(i string) map[string][]any {
		docs := with(tenant)
		docs["NATRule"] = []any{natRule(t, i)}
		return docs
	}
	// The Network 50-in puts the links of in* into the VRF lab; in* covers the name of the device
	// internet as well.
	covered := natOut("internet")
	covered["Network"] = append(covered["Network"], document(t, "Network", "50-in", `{"match": {"name": "in*"}, "network": {"vrf": "lab"}}`))
	narrowed := with(zone("x", `{"interfaces": ["swp3"], "vrfs": []}`))
	narrowed["Network"] = append(narrowed["Network"], document(t, "Network", "25-swp", `{"match": {"name": "swp3", "macAddress": "02:00:00:00:00:01"}}`))
	management := func(i string) map[string][]any {
		docs := with(tenant)
		docs["Router"] = []any{routerDoc(t, "Router", "router", `{"forwarding": {"ipv4": true, "ipv6": true}, "rpFilter": "loose", "ecmpHash": "l4", "conntrackMax": null, "management": {"interfaces": ["eth0", "`+i+`"]}}`)}
		return docs
	}
	// The Network 60-lab puts eth6 into the VRF lab by its own VRF=, which no VRF document names.
	withLab := func(docs map[string][]any) map[string][]any {
		docs["Network"] = append(docs["Network"], document(t, "Network", "60-lab", `{"match": {"name": "eth6"}, "network": {"vrf": "lab"}}`))
		return docs
	}
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a VRF's device":                                  {with(zone("x", `{"interfaces": ["eth9", "tenant"], "vrfs": []}`)), "FirewallZone/x: spec.interfaces[1]: tenant matches tenant, the device of VRF tenant; name the VRF in vrfs"},
		"a glob of a VRF's device":                        {with(zone("x", `{"interfaces": ["ten*"], "vrfs": []}`)), "FirewallZone/x: spec.interfaces[0]: ten* matches tenant, the device of VRF tenant; name the VRF in vrfs"},
		"a link of another zone's VRF":                    {with(tenant, zone("x", `{"interfaces": ["eth2"], "vrfs": []}`)), "FirewallZone/x: spec.interfaces[0]: FirewallZone x names eth2, which may be a link of VRF tenant, which FirewallZone tenant holds; give each link one zone"},
		"a glob of another zone's VRF":                    {with(tenant, zone("x", `{"interfaces": ["swp1*"], "vrfs": []}`)), "FirewallZone/x: spec.interfaces[0]: FirewallZone x names swp1*, which may be a link of VRF tenant, which FirewallZone tenant holds; give each link one zone"},
		"links nftables cannot match":                     {unmatchable, "Network/50-lab: spec.match.name: VRF tenant holds the links of Network 50-lab's Name=lab[12], which nftables cannot match; name them by a name or a prefix and *"},
		"a conntrack zone nftables cannot match":          {conntrack, "Network/50-lab: spec.match.name: VRF guest holds the links of Network 50-lab's Name=lab[12], which nftables cannot match; name them by a name or a prefix and *"},
		"a NAT zone of no link":                           {empty, "NATRule/n: spec.out.zone: the zone x holds no link"},
		"a NAT interface of a VRF's device":               {natOut("internet"), "NATRule/n: spec.out.interface: internet matches no link, only internet, the device of VRF internet; name the VRF's links or a zone of the VRF"},
		"a NAT glob of a VRF's device alone":              {natOut("inter*"), "NATRule/n: spec.out.interface: inter* matches no link, only internet, the device of VRF internet; name the VRF's links or a zone of the VRF"},
		"a NAT interface of a VRF's device a glob covers": {covered, "NATRule/n: spec.out.interface: internet matches no link, only internet, the device of VRF internet; name the VRF's links or a zone of the VRF"},
		"a link of a narrowed Network":                    {narrowed, "Network/25-swp: spec.match: networkd puts swp3 in VRF tenant only if the other [Match] keys of Network 25-swp match it; give the link a Network that matches it by Name= alone"},
		"a management VRF device":                         {management("tenant"), "Router/router: spec.management.interfaces[1]: the management interface tenant matches tenant, the device of VRF tenant, and chalkd answers in the default VRF alone; name the default VRF's links"},
		"a management glob of a VRF device":               {management("gu*"), "Router/router: spec.management.interfaces[1]: the management interface gu* matches guest, the device of VRF guest, and chalkd answers in the default VRF alone; name the default VRF's links"},
		"a management interface in a VRF":                 {management("eth1"), "Router/router: spec.management.interfaces[1]: the management interface eth1 may be a link of VRF internet, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF"},
		"a management interface a VRF's glob holds":       {management("swp2"), "Router/router: spec.management.interfaces[1]: the management interface swp2 may be a link of VRF tenant, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF"},
		"a management glob over a VRF's interface":        {management("eth*"), "Router/router: spec.management.interfaces[1]: the management interface eth* may be a link of VRF guest, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF"},
		"a management glob over a VRF's glob":             {management("sw*"), "Router/router: spec.management.interfaces[1]: the management interface sw* may be a link of VRF tenant, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF"},
		"a management interface a Network's VRF= holds":   {withLab(management("eth6")), "Router/router: spec.management.interfaces[1]: the management interface eth6 may be a link of VRF lab, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF"},
	} {
		t.Run(name, func(t *testing.T) { refusedOrGuarded(t, c.docs, c.want) })
	}
	accepted(t, vrfZoneDocs(t))
	// A NAT rule of every link translates links of VRFs as well, and a management glob of no
	// VRF's links takes chalkd's connections.
	accepted(t, natOut("*"))
	accepted(t, management("eth0*"))
}

// No zone names a NAT64's link, by name or glob: the forward chain lets the NAT64s' translated
// traffic through ahead of the zones and drops the rest, which a zone of the link would accept. A
// NAT rule of nothing but NAT64s' links translates nothing, as NAT leaves the links alone.
func TestNAT64LinkOutsideZones(t *testing.T) {
	with := func(zone, out string) map[string][]any {
		docs := vrfZoneDocs(t)
		docs["NAT64"] = []any{routerDoc(t, "NAT64", "guest", `{"vrf": "guest", "prefix": "64:ff9b::/96", "pool4": []}`)}
		if zone != "" {
			docs["FirewallZone"] = append(docs["FirewallZone"], routerDoc(t, "FirewallZone", "x", `{"interfaces": ["`+zone+`"], "vrfs": []}`))
		}
		if out != "" {
			docs["NATRule"] = []any{natRule(t, out)}
		}
		return docs
	}
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a zone of the link":     {with("n64-guest", ""), "FirewallZone/x: spec.interfaces[0]: n64-guest matches n64-guest, the link of NAT64 guest, which no zone holds; name other links"},
		"a zone's glob":          {with("n6*", ""), "FirewallZone/x: spec.interfaces[0]: n6* matches n64-guest, the link of NAT64 guest, which no zone holds; name other links"},
		"a NAT rule of the link": {with("", "n64-guest"), "NATRule/n: spec.out.interface: n64-guest matches no link but n64-guest, the link of NAT64 guest, which no NAT rule translates; name other links"},
		"a NAT rule's glob":      {with("", "n6*"), "NATRule/n: spec.out.interface: n6* matches no link but n64-guest, the link of NAT64 guest, which no NAT rule translates; name other links"},
	} {
		t.Run(name, func(t *testing.T) { refusedOrGuarded(t, c.docs, c.want) })
	}
	// A link of another NAT64's name is no NAT64's, and a NAT rule of every link translates
	// the others' connections.
	accepted(t, with("n64-other", "n64-other"))
	accepted(t, with("", "*"))
}

// A NATRule that translates to an address of a NAT64's explicit pool is refused: the pool's
// addresses belong to the NAT64, whose route and exemptions take every connection to them. A dnat
// rule whose destination lies in a NAT64's prefix or explicit pool is refused too: the dstnat
// chain accepts those connections before any NATRule. A wider destination is no NAT64's alone.
func TestNATRulesAndNAT64(t *testing.T) {
	nat64 := routerDoc(t, "NAT64", "edge", `{"prefix": "64:ff9b::/96", "pool4": [{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp"]}]}`)
	zone := routerDoc(t, "FirewallZone", "wan", `{"interfaces": ["eth0"]}`)
	for typ, place := range map[string]string{"dnat": `"in": {"zone": "wan"}`, "snat": `"out": {"zone": "wan"}`} {
		rule := routerDoc(t, "NATRule", "web", `{"type": "`+typ+`", "family": "ipv4", `+place+`, "to": {"address": "192.0.2.10"}}`)
		refused(t, map[string][]any{"NAT64": {nat64}, "FirewallZone": {zone}, "NATRule": {rule}},
			"NATRule/web: spec.to.address: to.address 192.0.2.10 lies in 192.0.2.8/30, the pool of NAT64 edge, whose addresses belong to the NAT64 whole; translate to an address outside the pool")
	}
	rule := func(family, destination, to string) any {
		return routerDoc(t, "NATRule", "web", `{"type": "dnat", "family": "`+family+`", "in": {"zone": "wan"}, "match": {"destination": ["`+destination+`"]}, "to": {"address": "`+to+`"}}`)
	}
	for _, c := range []struct{ family, destination, to, held string }{
		{"ipv4", "192.0.2.9/32", "198.51.100.1", "192.0.2.8/30"},
		{"ipv4", "192.0.2.8/30", "198.51.100.1", "192.0.2.8/30"},
		{"ipv6", "64:ff9b::c000:201/128", "2001:db8::1", "64:ff9b::/96"},
	} {
		refused(t, map[string][]any{"NAT64": {nat64}, "FirewallZone": {zone}, "NATRule": {rule(c.family, c.destination, c.to)}},
			"NATRule/web: spec.match.destination[0]: match.destination "+c.destination+" lies in "+c.held+" of NAT64 edge, whose connections the firewall accepts before any NATRule; match a range outside it")
	}
	accepted(t, map[string][]any{"NAT64": {nat64}, "FirewallZone": {zone}, "NATRule": {rule("ipv4", "192.0.2.0/24", "198.51.100.1")}})
}

// policyDocs are a router's documents with the PolicyRoute documents given as name and spec: a
// LAN zone on lan0, a VRF transit of table 100 on vt0 and Networks of lan0, wanb0 and vt0.
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

// A PolicyRoute's in refuses an interface or glob that matches a VRF's device, which prerouting
// sees for a VRF's link only after the pass that decides the mark, or a NAT64's link, whose
// traffic Jool translates, and a zone that holds a NAT64's link or no link.
func TestPolicyRouteInRefusals(t *testing.T) {
	dscp := `"rules": [{"match": {"dscp": [46]}, "to": {"vrf": "transit"}}]`
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a VRF's device":           {policyDocs(t, "a", `{"in": {"zone": null, "interfaces": ["lan0", "transit"]}, `+dscp+`}`), "PolicyRoute/a: spec.in.interfaces[1]: transit matches transit, the device of VRF transit; name the VRF's links or a zone of the VRF"},
		"a glob of a VRF's device": {policyDocs(t, "a", `{"in": {"zone": null, "interfaces": ["tran*"]}, `+dscp+`}`), "PolicyRoute/a: spec.in.interfaces[0]: tran* matches transit, the device of VRF transit; name the VRF's links or a zone of the VRF"},
		"a glob of a NAT64's link": {func() map[string][]any {
			d := policyDocs(t, "a", `{"in": {"zone": null, "interfaces": ["n64-*"]}, `+dscp+`}`)
			d["NAT64"] = []any{routerDoc(t, "NAT64", "edge", `{"prefix": "64:ff9b::/96", "pool4": []}`)}
			return d
		}(), "PolicyRoute/a: spec.in.interfaces[0]: n64-* matches n64-edge, the link of NAT64 edge, whose traffic Jool translates; name other links"},
		"a zone of a NAT64's link": {func() map[string][]any {
			d := policyDocs(t, "a", `{"in": {"zone": "core", "interfaces": []}, `+dscp+`}`)
			d["VRF"] = append(d["VRF"], routerDoc(t, "VRF", "core", `{"table": 400, "interfaces": ["n*"]}`))
			d["Network"] = append(d["Network"], document(t, "Network", "40-core", `{"match": {"name": "n*"}}`))
			d["FirewallZone"] = append(d["FirewallZone"], routerDoc(t, "FirewallZone", "core", `{"interfaces": [], "vrfs": ["core"]}`))
			d["NAT64"] = []any{routerDoc(t, "NAT64", "edge", `{"vrf": "core", "prefix": "64:ff9b::/96", "pool4": []}`)}
			return d
		}(), "PolicyRoute/a: spec.in.zone: the zone core holds n*, which matches n64-edge, the link of NAT64 edge, whose traffic Jool translates; name the zone's other links in interfaces"},
		"a zone without links": {func() map[string][]any {
			d := policyDocs(t, "a", `{"in": {"zone": "core", "interfaces": []}, `+dscp+`}`)
			d["VRF"] = append(d["VRF"], routerDoc(t, "VRF", "core", `{"table": 400, "interfaces": []}`))
			d["FirewallZone"] = append(d["FirewallZone"], routerDoc(t, "FirewallZone", "core", `{"interfaces": [], "vrfs": ["core"]}`))
			return d
		}(), "PolicyRoute/a: spec.in.zone: the zone core holds no link"},
	} {
		t.Run(name, func(t *testing.T) { refusedOrGuarded(t, c.docs, c.want) })
	}
	accepted(t, policyDocs(t, "a", `{"in": {"zone": "lan", "interfaces": []}, `+dscp+`}`))
}

// A resolver's listen address that Network documents give only to links of other VRFs is refused,
// one that the resolver's VRF holds too is not, and so is one that no Network gives.
func TestResolverListenRefusals(t *testing.T) {
	docs := func(listen string) map[string][]any {
		return map[string][]any{
			"VRF": {routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3"]}`)},
			"Network": {
				document(t, "Network", "10-eth0", `{"match": {"name": "eth0"}, "network": {"address": ["2001:db8:ffff::1/128", "2001:db8:3::9/64"]}}`),
				document(t, "Network", "40-guest", `{"match": {"name": "eth3"}, "network": {"address": ["2001:db8:3::1/64", "2001:db8:3::9/64"]}}`),
			},
			"Resolver": {
				routerDoc(t, "Resolver", "guest", `{"vrf": "guest", "listen": ["`+listen+`"], "allow": ["2001:db8:3::/64"]}`),
				routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"]}`),
			},
		}
	}
	for _, listen := range []string{"2001:db8:3::1", "2001:db8:3::9", "2001:db8:3::53"} {
		accepted(t, docs(listen))
	}
	refused(t, docs("2001:db8:ffff::1"),
		"Resolver/guest: spec.listen[0]: the address 2001:db8:ffff::1 of listen belongs to links of Network 10-eth0 in VRF default alone, so the resolver of VRF guest never receives its queries; listen on an address of the VRF's links")
	// The default VRF's resolver is refused an address of a VRF's links alike.
	vrfOnly := docs("2001:db8:3::1")
	vrfOnly["Resolver"] = []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:3::1"], "allow": ["2001:db8::/32"]}`)}
	refused(t, vrfOnly,
		"Resolver/resolver: spec.listen[0]: the address 2001:db8:3::1 of listen belongs to links of Network 40-guest in VRF guest alone, so the resolver of VRF default never receives its queries; listen on an address of the VRF's links")
}
