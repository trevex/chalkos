package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// routerRules compiles the router kinds' rules, and returns a maker of their documents, whose
// specs, in JSON, get their schema defaults as a delivery's do.
func routerRules(t *testing.T) (*Set, func(kind, name, spec string) config.Document) {
	t.Helper()
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/router")
	if err != nil {
		t.Fatal(err)
	}
	set, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	return set, func(kind, name, spec string) config.Document {
		t.Helper()
		d := builtinDoc(t, config.RouterAPI, kind, name, spec)
		filled, err := s.Default(d.Object)
		if err != nil {
			t.Fatal(err)
		}
		return config.FromObject(filled)
	}
}

// network is a networkd Network document with the [Match] section given, in JSON.
func network(t *testing.T, name, match string) config.Document {
	t.Helper()
	return builtinDoc(t, config.NetworkdAPI, "Network", name, `{"match": `+match+`}`)
}

// The router kinds' rules accept a fabric edge's documents and a zone of a VRF, and refuse a
// dangling zone, set or VRF, two zones of one interface or VRF, a zone of nothing, a policy from
// local, two policies between one pair, a set beside ranges, addresses of another family and
// ports without a protocol.
func TestRouterRules(t *testing.T) {
	set, doc := routerRules(t)
	edge := []config.Document{
		doc("Router", "router", `{"management": {"interfaces": ["eth0"]}}`),
		doc("FirewallZone", "wan", `{"interfaces": ["eth0"]}`),
		doc("FirewallZone", "fabric", `{"interfaces": ["eth1", "eth2"]}`),
		doc("AddressSet", "office", `{"family": "ipv6", "prefixes": ["2001:db8:1::/48"]}`),
		doc("FirewallPolicy", "wan-local", `{"from": "wan", "to": "local", "rules": [
			{"protocol": "tcp", "destinationPorts": [22, "8000-8100"], "source": ["set:office"], "action": "accept"},
			{"protocol": "icmpv6", "icmpTypes": ["packet-too-big"], "action": "accept"},
			{"source": ["192.0.2.0/24", "2001:db8::/32"], "action": "reject", "log": true}]}`),
		doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "default": "accept"}`),
		doc("NATRule", "wan", `{"type": "masquerade", "out": {"zone": "wan"}}`),
		doc("NATRule", "web", `{"type": "dnat", "in": {"interface": "eth0"}, "match": {"protocol": "tcp", "destinationPorts": [80]}, "to": {"address": "192.0.2.10", "port": 8080}}`),
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
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"a fabric edge", edge, nil},
		{"no documents", nil, nil},
		{"a zone named local", replace(edge, doc("FirewallZone", "local", `{"interfaces": ["lo0"]}`)),
			[]string{"a FirewallZone is named local, which is the router itself: FirewallPolicy documents name it without a document, so give the zone another name"}},
		{"an interface in two zones", replace(edge, doc("FirewallZone", "lan", `{"interfaces": ["eth2"]}`)),
			[]string{"the interface eth2 is in more than one FirewallZone, or twice in one; give each interface one zone"}},
		{"a policy from local", replace(edge, doc("FirewallPolicy", "out", `{"from": "local", "to": "wan"}`)),
			[]string{"FirewallPolicy out comes from local, the router itself, whose own connections the firewall accepts; a policy comes from a FirewallZone"}},
		{"a zone no document declares", replace(edge, doc("FirewallPolicy", "dmz-local", `{"from": "dmz", "to": "local"}`)),
			[]string{"FirewallPolicy dmz-local names the zone dmz, which no FirewallZone document declares"}},
		{"two policies between one pair", replace(edge, doc("FirewallPolicy", "wan-local-2", `{"from": "wan", "to": "local"}`)),
			[]string{"more than one FirewallPolicy goes from wan to local; give each pair of zones one policy"}},
		{"a set beside ranges", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"source": ["set:office", "2001:db8:2::/48"], "action": "accept"}]}`)),
			[]string{`FirewallPolicy "fabric-wan" spec.rules: a rule's source or destination holds set:<name> beside other entries; a set stands alone, so give the other ranges a rule of their own or add them to the set`}},
		{"a set the config lacks", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"destination": ["set:servers"], "action": "accept"}]}`)),
			[]string{`FirewallPolicy "fabric-wan" spec.rules: a rule names an AddressSet that the config does not hold`}},
		{"a set of one family and addresses of the other", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"source": ["set:office"], "destination": ["192.0.2.0/24"], "action": "accept"}]}`)),
			[]string{`FirewallPolicy "fabric-wan" spec.rules: a rule matches no connection: its family, protocol, addresses and sets leave neither IPv4 nor IPv6`}},
		{"ICMP types without a protocol", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"icmpTypes": ["echo-request"], "action": "accept"}]}`)),
			[]string{`FirewallPolicy "fabric-wan" spec.rules: a rule has ports without the protocol tcp, udp or sctp, or ICMP types its protocol lacks`}},
		{"an interface twice in one zone", replace(edge, doc("FirewallZone", "fabric", `{"interfaces": ["eth1", "eth1"]}`)),
			[]string{"the interface eth1 is in more than one FirewallZone, or twice in one; give each interface one zone"}},
		{"dnat with out", replace(edge, doc("NATRule", "web", `{"type": "dnat", "in": {"interface": "eth0"}, "out": {"interface": "eth1"}, "to": {"address": "192.0.2.10"}}`)),
			[]string{"NATRule web: a masquerade or snat rule names out alone, a dnat rule in alone"}},
		{"an empty place", replace(edge, doc("NATRule", "wan", `{"type": "masquerade", "out": {}}`)),
			[]string{"NATRule wan: out and in name a zone or an interface, one of them"}},
		{"masquerade with to", replace(edge, doc("NATRule", "wan", `{"type": "masquerade", "out": {"zone": "wan"}, "to": {"address": "192.0.2.1"}}`)),
			[]string{"NATRule wan: snat and dnat translate to the address of to, masquerade to that of the interface and has no to"}},
		{"a family and addresses of the other", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"family": "ipv4", "source": ["set:office"], "action": "accept"}]}`)),
			[]string{`FirewallPolicy "fabric-wan" spec.rules: a rule has addresses or a set of another family than its own`,
				`FirewallPolicy "fabric-wan" spec.rules: a rule matches no connection: its family, protocol, addresses and sets leave neither IPv4 nor IPv6`}},
		{"icmp with IPv6 addresses", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"protocol": "icmp", "destination": ["2001:db8::/32"], "action": "accept"}]}`)),
			[]string{`FirewallPolicy "fabric-wan" spec.rules: a rule matches no connection: its family, protocol, addresses and sets leave neither IPv4 nor IPv6`}},
		{"ports without a protocol", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"destinationPorts": [53], "action": "accept"}]}`)),
			[]string{`FirewallPolicy "fabric-wan" spec.rules: a rule has ports without the protocol tcp, udp or sctp, or ICMP types its protocol lacks`}},
		{"a set of another family", replace(edge, doc("AddressSet", "office", `{"family": "ipv4", "prefixes": ["2001:db8:1::/48"]}`)),
			[]string{"AddressSet office holds ranges of another family than ipv4"}},
		{"masquerade with in", replace(edge, doc("NATRule", "wan", `{"type": "masquerade", "in": {"zone": "wan"}}`)),
			[]string{"NATRule wan: a masquerade or snat rule names out alone, a dnat rule in alone"}},
		{"a place of zone and interface", replace(edge, doc("NATRule", "wan", `{"type": "masquerade", "out": {"zone": "wan", "interface": "eth0"}}`)),
			[]string{"NATRule wan: out and in name a zone or an interface, one of them"}},
		{"a NAT zone no document declares", replace(edge, doc("NATRule", "wan", `{"type": "masquerade", "out": {"zone": "uplink"}}`)),
			[]string{"NATRule wan names a zone that no FirewallZone document declares"}},
		{"snat without to", replace(edge, doc("NATRule", "wan", `{"type": "snat", "out": {"zone": "wan"}}`)),
			[]string{"NATRule wan: snat and dnat translate to the address of to, masquerade to that of the interface and has no to"}},
		{"NAT of another family", replace(edge, doc("NATRule", "web", `{"type": "dnat", "in": {"interface": "eth0"}, "match": {"protocol": "tcp", "destinationPorts": [80]}, "to": {"address": "2001:db8::10"}}`)),
			[]string{"NATRule web has addresses of another family than ipv4"}},
		{"NAT ports without a protocol", replace(edge, doc("NATRule", "web", `{"type": "dnat", "in": {"interface": "eth0"}, "match": {"destinationPorts": [80]}, "to": {"address": "192.0.2.10"}}`)),
			[]string{"NATRule web: ports need match.protocol"}},
		{"a zone of a VRF", replace(edge, doc("VRF", "tenant", `{"table": 100}`), doc("FirewallZone", "tenant", `{"vrfs": ["tenant"]}`)), nil},
		{"a zone of nothing", replace(edge, doc("FirewallZone", "dmz", `{}`)),
			[]string{"FirewallZone dmz: a FirewallZone names an interface or a VRF; give it interfaces, vrfs or both"}},
		{"a zone of interfaces and a VRF", replace(edge, doc("VRF", "tenant", `{"table": 100}`), doc("FirewallZone", "tenant", `{"interfaces": ["eth3"], "vrfs": ["tenant"]}`)), nil},
		{"a zone of a VRF no document declares", replace(edge, doc("FirewallZone", "tenant", `{"vrfs": ["tenant"]}`)),
			[]string{"FirewallZone tenant names the VRF tenant, which no VRF document declares"}},
		{"a VRF in two zones", replace(edge, doc("VRF", "tenant", `{"table": 100}`), doc("FirewallZone", "tenant", `{"vrfs": ["tenant"]}`), doc("FirewallZone", "more", `{"vrfs": ["tenant"]}`)),
			[]string{"the VRF tenant is in more than one FirewallZone, or twice in one; give each VRF one zone"}},
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

// A delivery that keeps the management interfaces keeps a Network for each one a running Network
// names, whether by a name of a list or of a value of several, or by a glob either way. One that
// renames the Network, keeps another that matches, or changes the management interfaces passes,
// and so does a router whose node API answers on every interface, a first delivery and one
// without a Router.
func TestManagementGuard(t *testing.T) {
	set, doc := routerRules(t)
	router := func(mgmt string) config.Document {
		return doc("Router", "router", `{"management": {"interfaces": `+mgmt+`}}`)
	}
	running := []config.Document{
		router(`["eth0", "mgmt*"]`),
		network(t, "10-eth0", `{"name": "eth0"}`),
		network(t, "20-mgmt", `[{"name": ["mgmt*", "eth9"]}]`),
		network(t, "30-eth1", `{"name": "eth1"}`),
	}
	lost := func(names string) []string {
		return []string{`Router "router" spec.management.interfaces: the config leaves the management interfaces ` + names + ` without a Network document that matches them, so the router would no longer configure them; keep a Network that matches each, or change spec.management.interfaces in the same delivery`}
	}
	for _, tc := range []struct {
		name string
		old  []config.Document
		docs []config.Document
		want []string
	}{
		{"unchanged", running, running, nil},
		{"another interface's Network removed", running, running[:3], nil},
		{"the Network renamed", running, []config.Document{running[0], network(t, "11-eth0", `{"name": "eth0"}`), running[2], running[3]}, nil},
		{"the management Network removed", running, []config.Document{running[0], running[2], running[3]}, lost("eth0")},
		{"both removed", running, []config.Document{running[0], running[3]}, lost("eth0, mgmt*")},
		{"the management interfaces changed too", running, []config.Document{router(`["eth1"]`), running[3]}, nil},
		{"the node API on every interface", running, []config.Document{router(`null`), running[3]}, nil},
		{"the Router removed", running, running[1:], nil},
		{"a first delivery", nil, []config.Document{running[0]}, nil},
		{"a Network of several names removed",
			[]config.Document{router(`["eth0"]`), network(t, "10-uplinks", `{"name": " eth0  eth9"}`)},
			[]config.Document{router(`["eth0"]`)}, lost("eth0")},
		{"a Network of several names kept",
			[]config.Document{router(`["eth0"]`), network(t, "10-eth0", `{"name": "eth0"}`)},
			[]config.Document{router(`["eth0"]`), network(t, "10-uplinks", `{"name": "eth9\teth0"}`)}, nil},
		{"a Network the management glob matches removed",
			[]config.Document{router(`["eth*"]`), network(t, "10-eth0", `{"name": "eth0"}`)},
			[]config.Document{router(`["eth*"]`)}, lost("eth*")},
		{"a Network whose glob matches the interface removed",
			[]config.Document{router(`["eth0"]`), network(t, "10-e", `{"name": "e*"}`)},
			[]config.Document{router(`["eth0"]`)}, lost("eth0")},
		{"a Network replaced by a glob that matches it",
			[]config.Document{router(`["eth0"]`), network(t, "10-eth0", `{"name": "eth0"}`)},
			[]config.Document{router(`["eth0"]`), network(t, "10-eth", `{"name": "eth*"}`)}, nil},
		{"a Network of another interface's glob removed",
			[]config.Document{router(`["eth0"]`), network(t, "10-en", `{"name": "en*"}`)},
			[]config.Document{router(`["eth0"]`)}, nil},
		{"a Network whose glob is the interface's name removed",
			[]config.Document{router(`["eth0"]`), network(t, "10-eth0", `{"name": "eth0*"}`)},
			[]config.Document{router(`["eth0"]`)}, lost("eth0")},
		{"a Network replaced by a glob of the interface's name",
			[]config.Document{router(`["eth0"]`), network(t, "10-eth0", `{"name": "eth0"}`)},
			[]config.Document{router(`["eth0"]`), network(t, "10-eth0", `{"name": "eth0*"}`)}, nil},
		{"the last Network of a router managed on every interface removed",
			[]config.Document{router(`["*"]`), network(t, "10-eth1", `{"name": "eth1"}`)},
			[]config.Document{router(`["*"]`)}, lost("*")},
		{"a Network of a router managed on every interface replaced",
			[]config.Document{router(`["*"]`), network(t, "10-eth1", `{"name": "eth1"}`)},
			[]config.Document{router(`["*"]`), network(t, "10-eth2", `{"name": "eth2"}`)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: tc.docs, Old: tc.old, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
	// A router whose node API answered on every interface had nothing to keep.
	open := []config.Document{router(`null`), running[1]}
	if failures, err := set.Evaluate(Input{Documents: []config.Document{router(`null`)}, Old: open, Node: "r1"}); err != nil || len(failures) != 0 {
		t.Errorf("without management interfaces: %v, %v", messages(failures), err)
	}
}
