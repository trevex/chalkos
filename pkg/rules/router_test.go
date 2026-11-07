package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/router/derive"
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
		d := builtinDoc(t, derive.API, kind, name, spec)
		filled, err := s.Default(d.Object)
		if err != nil {
			t.Fatal(err)
		}
		return config.FromObject(filled)
	}
}

// The router kinds' schemas take addresses and address ranges as ip() and cidr() read them, and
// ranges of ports from 1 to 65535 that run forwards, wherever a kind holds them: in a list, in a
// list's items and in a field of its own.
func TestRouterSchemaFormats(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/router")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, spec, want string
	}{
		{"NATRule", `{"type": "snat", "out": {"interface": "eth0"}, "to": {"address": "192.0.2.10"}}`, ""},
		{"NATRule", `{"type": "snat", "out": {"interface": "eth0"}, "to": {"address": "2001:DB8::a"}}`, ""},
		{"NATRule", `{"type": "snat", "out": {"interface": "eth0"}, "to": {"address": "192.0.2.256"}}`,
			`NATRule "x": - at '/spec/to/address': '192.0.2.256' is not valid ip: an address is an IPv4 address of four numbers from 0 to 255 without leading zeros, or an IPv6 address without a zone`},
		{"NATRule", `{"type": "snat", "out": {"interface": "eth0"}, "to": {"address": "192.0.2.010"}}`, "is not valid ip"},
		{"NATRule", `{"type": "snat", "out": {"interface": "eth0"}, "to": {"address": "fe80::1%eth0"}}`, "is not valid ip"},
		{"NATRule", `{"type": "snat", "out": {"interface": "eth0"}, "to": {"address": "1:2"}}`, "is not valid ip"},
		{"NATRule", `{"type": "masquerade", "out": {"interface": "eth0"}, "match": {"source": ["10.0.0.0/33"]}}`,
			`NATRule "x": - at '/spec/match/source/0': '10.0.0.0/33' is not valid cidr: an address range is an address, a slash and a prefix length of at most 32 for IPv4 or 128 for IPv6`},
		{"NATRule", `{"type": "masquerade", "out": {"interface": "eth0"}, "match": {"source": ["10.0.0.1"]}}`, "is not valid cidr"},
		{"AddressSet", `{"family": "ipv6", "prefixes": ["2001:db8::/129"]}`, "is not valid cidr"},
		{"NextHop", `{"address": "fe80::1", "interface": "wanb0", "table": 200}`, ""},
		{"NextHop", `{"address": "fe80::1%wanb0", "interface": "wanb0", "table": 200}`, "is not valid ip"},
		{"FirewallPolicy", `{"from": "wan", "to": "local", "rules": [{"protocol": "tcp", "destinationPorts": [22, "8000-8100", "1-65535", "443-443"], "action": "accept"}]}`, ""},
		{"FirewallPolicy", `{"from": "wan", "to": "local", "rules": [{"protocol": "tcp", "destinationPorts": ["9-1"], "action": "accept"}]}`,
			`FirewallPolicy "x": - at '/spec/rules/0/destinationPorts/0': '9-1' is not valid port-range: a range of ports is "<first>-<last>", each from 1 to 65535, and the first no higher than the last`},
		{"FirewallPolicy", `{"from": "wan", "to": "local", "rules": [{"protocol": "tcp", "sourcePorts": ["70000-80000"], "action": "accept"}]}`, "is not valid port-range"},
		{"FirewallPolicy", `{"from": "wan", "to": "local", "rules": [{"protocol": "tcp", "sourcePorts": ["22"], "action": "accept"}]}`, "a single port is a number, unquoted"},
		{"NAT64", `{"pool4": [{"prefix": "192.0.2.10/32", "ports": "65535-61001"}]}`, "is not valid port-range"},
	} {
		d := builtinDoc(t, derive.API, tc.kind, "x", tc.spec)
		err := s.Validate(d.Object)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s %s: %v", tc.kind, tc.spec, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s %s: err = %v, want one with %q", tc.kind, tc.spec, err, tc.want)
		}
	}
}

// network is a networkd Network document with the [Match] section given, in JSON.
func network(t *testing.T, name, match string) config.Document {
	t.Helper()
	return builtinDoc(t, config.NetworkdAPI, "Network", name, `{"match": `+match+`}`)
}

// The router kinds' rules accept a fabric edge's documents and a zone of a VRF, and refuse a zone
// of nothing, a zone's or NAT rule's interface that is a VRF's device, a policy from local, a
// source of a set and ranges or of neither, addresses of another family and ports without a
// protocol.
func TestRouterRules(t *testing.T) {
	set, doc := routerRules(t)
	edge := []config.Document{
		doc("Router", "router", `{"management": {"interfaces": ["eth0"]}}`),
		doc("FirewallZone", "wan", `{"interfaces": ["eth0"]}`),
		doc("FirewallZone", "fabric", `{"interfaces": ["eth1", "eth2"]}`),
		doc("AddressSet", "office", `{"family": "ipv6", "prefixes": ["2001:db8:1::/48"]}`),
		doc("FirewallPolicy", "wan-local", `{"from": "wan", "to": "local", "rules": [
			{"protocol": "tcp", "destinationPorts": [22, "8000-8100"], "source": {"set": "office"}, "action": "accept"},
			{"protocol": "icmpv6", "icmpTypes": ["packet-too-big"], "action": "accept"},
			{"source": {"cidrs": ["192.0.2.0/24", "2001:db8::/32"]}, "action": "reject", "log": true}]}`),
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
		{"a policy from local", replace(edge, doc("FirewallPolicy", "out", `{"from": "local", "to": "wan"}`)),
			[]string{"FirewallPolicy out comes from local, the router itself, whose own connections the firewall accepts; a policy comes from a FirewallZone"}},
		{"a set beside ranges", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"source": {"set": "office", "cidrs": ["2001:db8:2::/48"]}, "action": "accept"}]}`)),
			[]string{`FirewallPolicy/fabric-wan: spec.rules: a rule's source or destination names both set and cidrs, or neither; give one, or leave the field out to match every address`}},
		{"a source of neither", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"source": {}, "action": "accept"}]}`)),
			[]string{`FirewallPolicy/fabric-wan: spec.rules: a rule's source or destination names both set and cidrs, or neither; give one, or leave the field out to match every address`}},
		{"a set of one family and addresses of the other", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"source": {"set": "office"}, "destination": {"cidrs": ["192.0.2.0/24"]}, "action": "accept"}]}`)),
			[]string{`FirewallPolicy/fabric-wan: spec.rules: a rule matches no connection: its family, protocol, addresses and sets leave neither IPv4 nor IPv6`}},
		{"ICMP types without a protocol", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"icmpTypes": ["echo-request"], "action": "accept"}]}`)),
			[]string{`FirewallPolicy/fabric-wan: spec.rules: a rule has ports without the protocol tcp, udp or sctp, or ICMP types its protocol lacks`}},
		{"dnat with out", replace(edge, doc("NATRule", "web", `{"type": "dnat", "in": {"interface": "eth0"}, "out": {"interface": "eth1"}, "to": {"address": "192.0.2.10"}}`)),
			[]string{"NATRule web: a masquerade or snat rule names out alone, a dnat rule in alone"}},
		{"an empty place", replace(edge, doc("NATRule", "wan", `{"type": "masquerade", "out": {}}`)),
			[]string{"NATRule wan: out and in name a zone or an interface, one of them"}},
		{"masquerade with to", replace(edge, doc("NATRule", "wan", `{"type": "masquerade", "out": {"zone": "wan"}, "to": {"address": "192.0.2.1"}}`)),
			[]string{"NATRule wan: snat and dnat translate to the address of to, masquerade to that of the interface and has no to"}},
		{"a family and addresses of the other", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"family": "ipv4", "source": {"set": "office"}, "action": "accept"}]}`)),
			[]string{`FirewallPolicy/fabric-wan: spec.rules: a rule has addresses or a set of another family than its own`,
				`FirewallPolicy/fabric-wan: spec.rules: a rule matches no connection: its family, protocol, addresses and sets leave neither IPv4 nor IPv6`}},
		{"icmp with IPv6 addresses", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"protocol": "icmp", "destination": {"cidrs": ["2001:db8::/32"]}, "action": "accept"}]}`)),
			[]string{`FirewallPolicy/fabric-wan: spec.rules: a rule matches no connection: its family, protocol, addresses and sets leave neither IPv4 nor IPv6`}},
		{"ports without a protocol", replace(edge, doc("FirewallPolicy", "fabric-wan", `{"from": "fabric", "to": "wan", "rules": [{"destinationPorts": [53], "action": "accept"}]}`)),
			[]string{`FirewallPolicy/fabric-wan: spec.rules: a rule has ports without the protocol tcp, udp or sctp, or ICMP types its protocol lacks`}},
		{"a set of another family", replace(edge, doc("AddressSet", "office", `{"family": "ipv4", "prefixes": ["2001:db8:1::/48"]}`)),
			[]string{"AddressSet office holds ranges of another family than ipv4"}},
		{"masquerade with in", replace(edge, doc("NATRule", "wan", `{"type": "masquerade", "in": {"zone": "wan"}}`)),
			[]string{"NATRule wan: a masquerade or snat rule names out alone, a dnat rule in alone"}},
		{"a place of zone and interface", replace(edge, doc("NATRule", "wan", `{"type": "masquerade", "out": {"zone": "wan", "interface": "eth0"}}`)),
			[]string{"NATRule wan: out and in name a zone or an interface, one of them"}},
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
		// The output router-nftables refuses interfaces of VRFs' devices, by name and by glob.
		{"a zone of a VRF's device", replace(edge, doc("VRF", "tenant", `{"table": 100}`), doc("FirewallZone", "tenant", `{"interfaces": ["eth3", "tenant"]}`)), nil},
		{"a NAT interface of a VRF's device", replace(edge, doc("VRF", "tenant", `{"table": 100}`), doc("NATRule", "out", `{"type": "masquerade", "out": {"interface": "tenant"}}`)), nil},
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
		return []string{`Router/router: spec.management.interfaces: the config leaves the management interfaces ` + names + ` without a Network document that matches them, so the router would no longer configure them; keep a Network that matches each, or change spec.management.interfaces in the same delivery`}
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
