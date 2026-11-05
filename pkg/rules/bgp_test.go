package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// The BGP kinds' rules accept a fabric edge's documents and refuse a peer with two ways to reach
// it or with no family, peers without BGP, lists of another family than a peer's family or a
// network's, a policy of both families or of more than 6000 terms, ge and le outside their range,
// a list of both kinds of community, a rejecting term that sets attributes, an AS path or a
// description too long, a router ID or a neighbour address that is no unicast address, IPv4
// routes at an IPv6 address without extended next hops, an interface named like an address, a
// password whose Secret names no credential, and a defaultOriginateWhen without defaultOriginate
// or of a list of the other family.
func TestBGPRules(t *testing.T) {
	set, doc := routerRules(t)
	edge := []config.Document{
		doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "multipath": {"ebgp": 64, "asPathRelax": true}, "networks": [{"prefix": "2001:db8:ffff::1/128"}, {"prefix": "64:ff9b::/96", "policy": "to-fabric"}]}`),
		doc("BGPPeer", "fabric", `{"interfaces": ["eth1", "eth2"], "remoteAS": "external", "bfd": true, "extendedNextHop": true, "password": {"secretRef": {"name": "bgp", "key": "fabric"}},
			"families": {"ipv6": {"defaultOriginate": true, "import": "from-fabric", "export": "to-fabric"}}}`),
		doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "remoteAS": 64496, "families": {"ipv4": {"maxPrefix": 1000}}}`),
		doc("RoutingPolicy", "from-fabric", `{"terms": [{"match": {"prefixList": "fabric"}, "then": {"action": "accept", "localPref": 200}}, {"match": {"communityList": "blackhole"}, "then": {"action": "reject"}}]}`),
		doc("RoutingPolicy", "to-fabric", `{"terms": [{"match": {"prefixList": "edge", "asPath": "^$"}, "then": {"action": "accept", "addCommunities": ["64512:1", "64512:1:2"], "prepend": 2}}]}`),
		doc("PrefixList", "fabric", `{"family": "ipv6", "entries": [{"prefix": "2001:db8::/32", "le": 128}]}`),
		doc("PrefixList", "edge", `{"family": "ipv6", "entries": [{"prefix": "2001:db8:ffff::/48", "ge": 48, "le": 128}, {"prefix": "64:ff9b::/96"}, {"prefix": "::/0", "action": "deny"}]}`),
		doc("CommunityList", "blackhole", `{"communities": ["64512:666", "65535:666"]}`),
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
	without := func(docs []config.Document, kind string) []config.Document {
		return slices.DeleteFunc(slices.Clone(docs), func(d config.Document) bool { return d.Kind == kind })
	}
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"a fabric edge", edge, nil},
		{"lists and policies without peers", without(without(edge, "BGPPeer"), "BGP"), nil},
		{"two ways to reach a peer", replace(edge, doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "interface": "eth0", "remoteAS": 64496, "families": {"ipv4": {}}}`)),
			[]string{"BGPPeer upstream names not exactly one of address, interface and interfaces; a peer is reached at an address, on an interface or on each interface of a list"}},
		{"no way to reach a peer", replace(edge, doc("BGPPeer", "upstream", `{"remoteAS": 64496, "families": {"ipv4": {}}}`)),
			[]string{"BGPPeer upstream names not exactly one of address, interface and interfaces; a peer is reached at an address, on an interface or on each interface of a list"}},
		{"peers without BGP", without(edge, "BGP"),
			[]string{"BGPPeer fabric runs in the VRF default, BGPPeer upstream runs in the VRF default: a peer's VRF needs a BGP document, which gives the router its AS number there"}},
		{"a peer of no family", replace(edge, doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "remoteAS": 64496}`)),
			[]string{`BGPPeer/upstream: spec.families: the peer exchanges the routes of no family; give families.ipv4 or families.ipv6, an empty one accepting and sending every route`}},
		{"a policy of another family", replace(edge, doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "remoteAS": 64496, "families": {"ipv4": {"import": "from-fabric"}}}`)),
			[]string{`BGPPeer/upstream: spec.families: the ipv4 family's RoutingPolicy from-fabric matches a PrefixList of another family`}},
		{"a policy of both families", replace(edge, doc("PrefixList", "v4", `{"family": "ipv4", "entries": [{"prefix": "192.0.2.0/24"}]}`),
			doc("RoutingPolicy", "from-fabric", `{"terms": [{"match": {"prefixList": "fabric"}, "then": {"action": "accept"}}, {"match": {"prefixList": "v4"}, "then": {"action": "accept"}}]}`)),
			[]string{`RoutingPolicy/from-fabric: spec.terms: the terms match PrefixLists of both families; a policy serves the routes of one family, so give each family a policy of its own`}},
		{"an AS path past 200 characters", replace(edge, doc("RoutingPolicy", "to-fabric", `{"terms": [{"match": {"prefixList": "edge", "asPath": "`+strings.Repeat("_64512", 34)+`"}, "then": {"action": "accept"}}]}`)),
			[]string{`RoutingPolicy/to-fabric: spec.terms: a term's asPath holds more than 200 characters`}},
		{"entries that differ in ge", replace(edge, doc("PrefixList", "edge", `{"family": "ipv6", "entries": [{"prefix": "2001:db8:ffff::/48", "le": 128}, {"prefix": "2001:db8:ffff::/48", "ge": 64, "le": 128}]}`)), nil},
		{"IPv4 routes at an IPv6 address", replace(edge, doc("BGPPeer", "upstream", `{"address": "2001:db8::9", "remoteAS": 64496, "families": {"ipv4": {}, "ipv6": {}}}`)),
			[]string{"BGPPeer upstream exchanges IPv4 routes at the IPv6 address 2001:db8::9 without extendedNextHop: true; the routes' next hops are IPv6 addresses, which a session carries with extendedNextHop alone"}},
		{"IPv4 routes at an IPv6 address with extended next hops", replace(edge, doc("BGPPeer", "upstream", `{"address": "2001:db8::9", "remoteAS": 64496, "extendedNextHop": true, "families": {"ipv4": {}}}`)), nil},
		{"a network's policy of the other family", replace(edge, doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "networks": [{"prefix": "192.0.2.0/24", "policy": "to-fabric"}]}`)),
			[]string{`BGP/bgp: spec.networks: the RoutingPolicy to-fabric of the network 192.0.2.0/24 matches PrefixLists of another family`}},
		{"a network's policy that matches no PrefixList", replace(edge, doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "networks": [{"prefix": "192.0.2.0/24", "policy": "tag"}]}`),
			doc("RoutingPolicy", "tag", `{"terms": [{"then": {"action": "accept", "addCommunities": ["64512:2"]}}]}`)), nil},
		{"a network's policy of its family", replace(edge, doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "networks": [{"prefix": "192.0.2.0/24", "policy": "to-upstream"}]}`),
			doc("PrefixList", "v4", `{"family": "ipv4", "entries": [{"prefix": "192.0.2.0/24"}]}`),
			doc("RoutingPolicy", "to-upstream", `{"terms": [{"match": {"prefixList": "v4"}, "then": {"action": "accept"}}]}`)), nil},
		{"6000 terms", replace(edge, doc("RoutingPolicy", "from-fabric", `{"terms": [`+strings.Repeat(`{"then": {"action": "accept"}}, `, 5999)+`{"then": {"action": "accept"}}]}`)), nil},
		{"6001 terms", replace(edge, doc("RoutingPolicy", "from-fabric", `{"terms": [`+strings.Repeat(`{"then": {"action": "accept"}}, `, 6000)+`{"then": {"action": "accept"}}]}`)),
			[]string{`RoutingPolicy/from-fabric: spec.terms: the policy holds 6001 terms; a route-map holds 6000 terms at most, numbered 10, 20 and on below its default's 65535`}},
		{"an interface named as an address", replace(edge, doc("BGPPeer", "upstream", `{"interface": "198.51.100.1", "remoteAS": 64496, "families": {"ipv4": {}}}`)),
			[]string{`BGPPeer/upstream: spec.interface: the interface 198.51.100.1 is named like an address, which FRR cannot tell from a neighbour's; give the peer its address, or rename the interface`}},
		{"interfaces named as addresses", replace(edge, doc("BGPPeer", "fabric", `{"interfaces": ["eth1", "fe80::1"], "remoteAS": "external", "families": {"ipv6": {}}}`)),
			[]string{`BGPPeer/fabric: spec.interfaces: the interface fe80::1 is named like an address, which FRR cannot tell from a neighbour's; give the peer its address, or rename the interface`}},
		{"an interface with a colon", replace(edge, doc("BGPPeer", "fabric", `{"interfaces": ["eth1:1"], "remoteAS": "external", "families": {"ipv6": {}}}`)), nil},
		{"a password that is no credential", replace(edge, doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "remoteAS": 64496, "password": {"secretRef": {"name": "bgp", "key": "up stream"}}, "families": {"ipv4": {}}}`)),
			[]string{`BGPPeer/upstream: spec.password: the Secret's name and key hold letters, digits, '.', '_' and '-' alone, as the names of frr.service's credentials do`}},
		{"a description past 80 characters", replace(edge, doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "remoteAS": 64496, "description": "`+strings.Repeat("transit ", 10)+`x", "families": {"ipv4": {}}}`)),
			[]string{`BGPPeer/upstream: spec.description: the description holds more than 80 characters`}},
		{"a rejecting term that sets", replace(edge, doc("RoutingPolicy", "from-fabric", `{"terms": [{"match": {"prefixList": "fabric"}, "then": {"action": "reject", "med": 10}}]}`)),
			[]string{`RoutingPolicy/from-fabric: spec.terms: a term that rejects routes sets local preference, MED, communities or a prepend, which no route it rejects keeps; drop them or accept`}},
		{"ge below the prefix length", replace(edge, doc("PrefixList", "edge", `{"family": "ipv6", "entries": [{"prefix": "2001:db8:ffff::/48", "ge": 40}]}`)),
			[]string{"PrefixList edge has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"}},
		{"le below ge", replace(edge, doc("PrefixList", "edge", `{"family": "ipv6", "entries": [{"prefix": "2001:db8:ffff::/48", "ge": 64, "le": 56}]}`)),
			[]string{"PrefixList edge has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"}},
		{"le past IPv4's longest prefix", replace(edge, doc("PrefixList", "v4", `{"family": "ipv4", "entries": [{"prefix": "192.0.2.0/24", "le": 64}]}`)),
			[]string{"PrefixList v4 has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"}},
		{"an entry of another family", replace(edge, doc("PrefixList", "fabric", `{"family": "ipv4", "entries": [{"prefix": "2001:db8::/32"}]}`)),
			[]string{`BGPPeer/fabric: spec.families: the ipv6 family's RoutingPolicy from-fabric matches a PrefixList of another family`,
				"PrefixList fabric has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"}},
		{"standard and large communities", replace(edge, doc("CommunityList", "blackhole", `{"communities": ["64512:666", "64512:666:1"]}`)),
			[]string{"CommunityList blackhole holds standard and large communities; give each kind a list of its own"}},
		{"a default while a route exists", replace(edge, doc("BGPPeer", "fabric", `{"interfaces": ["eth1", "eth2"], "remoteAS": "external",
			"families": {"ipv6": {"defaultOriginate": true, "defaultOriginateWhen": "fabric", "import": "from-fabric", "export": "to-fabric"}}}`)), nil},
		{"defaultOriginateWhen without defaultOriginate", replace(edge, doc("BGPPeer", "fabric", `{"interfaces": ["eth1", "eth2"], "remoteAS": "external",
			"families": {"ipv6": {"defaultOriginateWhen": "fabric"}}}`)),
			[]string{`BGPPeer/fabric: spec.families: the ipv6 family names defaultOriginateWhen without defaultOriginate: true; defaultOriginateWhen says when the router sends the default route that defaultOriginate originates`}},
		{"a tracked list of the other family", replace(edge, doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "remoteAS": 64496,
			"families": {"ipv4": {"defaultOriginate": true, "defaultOriginateWhen": "fabric"}}}`)),
			[]string{`BGPPeer/upstream: spec.families: the ipv4 family's defaultOriginateWhen names the PrefixList fabric of ipv6; it tracks routes of the family whose default route it sends`}},
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
	// A router ID is an IPv4 unicast address: neither unspecified, multicast nor the broadcast
	// address, which the schema's pattern takes.
	for _, id := range []string{"0.0.0.0", "224.0.0.5", "239.255.255.255", "255.255.255.255"} {
		t.Run("a router ID of "+id, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: replace(edge, doc("BGP", "bgp", `{"asn": 64512, "routerID": "`+id+`"}`)), Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{`BGP/bgp: spec.routerID: the router ID ` + id + ` is no IPv4 unicast address; give one that is neither 0.0.0.0, multicast nor 255.255.255.255`}
			if got := messages(failures); !slices.Equal(got, want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
	// A neighbour of address is a unicast address beyond the router: of neither family
	// unspecified, loopback, link-local or multicast, not IPv4's broadcast and no IPv4-mapped IPv6
	// address, which stands for an IPv4 neighbour. The peer exchanges IPv6 routes, so the rule of
	// extended next hops passes.
	for _, address := range []string{"0.0.0.0", "::", "127.0.0.1", "::1", "169.254.0.1", "fe80::1", "224.0.0.1", "ff02::1", "255.255.255.255", "::ffff:198.51.100.1", "::ffff:127.0.0.1"} {
		t.Run("a neighbour at "+address, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: replace(edge, doc("BGPPeer", "upstream", `{"address": "`+address+`", "remoteAS": 64496, "families": {"ipv6": {}}}`)), Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{`BGPPeer/upstream: spec.address: the address ` + address + ` is no neighbour's: give a unicast address beyond the router, neither loopback, link-local nor IPv4-mapped; a neighbour on a link takes interface or interfaces`}
			if got := messages(failures); !slices.Equal(got, want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
	// The ranges end where they should: router IDs and neighbours just outside them pass, as do
	// NAT64's well-known prefix and an IPv6 address next to the IPv4-mapped range.
	for _, id := range []string{"0.0.0.1", "223.255.255.255", "240.0.0.1", "255.255.255.254"} {
		t.Run("a router ID of "+id, func(t *testing.T) {
			if failures, err := set.Evaluate(Input{Documents: replace(edge, doc("BGP", "bgp", `{"asn": 64512, "routerID": "`+id+`"}`)), Node: "r1"}); err != nil || len(failures) != 0 {
				t.Errorf("%v, %v", messages(failures), err)
			}
		})
	}
	for _, address := range []string{"0.0.0.1", "126.255.255.255", "128.0.0.1", "169.255.0.1", "223.255.255.255", "255.255.255.254", "::2", "fec0::1", "64:ff9b::1", "::fffe:c633:6401", "::1:0:c633:6401"} {
		t.Run("a neighbour at "+address, func(t *testing.T) {
			if failures, err := set.Evaluate(Input{Documents: replace(edge, doc("BGPPeer", "upstream", `{"address": "`+address+`", "remoteAS": 64496, "families": {"ipv6": {}}}`)), Node: "r1"}); err != nil || len(failures) != 0 {
				t.Errorf("%v, %v", messages(failures), err)
			}
		})
	}
}

// BGP keeps its AS number; a delivery without the BGP document lets the next one take another.
func TestBGPKeepsItsAS(t *testing.T) {
	set, doc := routerRules(t)
	running := []config.Document{doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1"}`)}
	changed := []config.Document{doc("BGP", "bgp", `{"asn": 64513, "routerID": "192.0.2.1"}`)}
	failures, err := set.Evaluate(Input{Documents: changed, Old: running, Node: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`BGP/bgp: spec.asn: the AS number stays 64512; to change it, deliver the config without the BGP document, which ends the router's sessions, and then with the new AS number`}
	if got := messages(failures); !slices.Equal(got, want) {
		t.Errorf("failures %q, want %q", got, want)
	}
	for _, old := range [][]config.Document{nil, changed} {
		if failures, err := set.Evaluate(Input{Documents: changed, Old: old, Node: "r1"}); err != nil || len(failures) != 0 {
			t.Errorf("from %v: %v, %v", old, messages(failures), err)
		}
	}
}

// The rules refuse the config with the error of ip() for a neighbour's address that is none,
// which the schema's format refuses before them.
func TestBGPRulesReadAddresses(t *testing.T) {
	set, doc := routerRules(t)
	bgp := doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1"}`)
	for _, tc := range []struct {
		name string
		docs []config.Document
		want string
	}{
		{"a neighbour address that is none", []config.Document{bgp, doc("BGPPeer", "upstream", `{"address": "198.51.100.300", "remoteAS": 64496, "families": {"ipv6": {}}}`)},
			`cannot check: BGPPeer/upstream: spec.address: a rule cannot be evaluated: ` + errNotIP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := set.Evaluate(Input{Documents: tc.docs, Node: "r1"})
			if err == nil || err.Error() != tc.want {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// The BGP kinds' schemas take router IDs of four octets without leading zeros, communities whose
// parts fit 16 bits, or 32 bits in a large one, descriptions without @, which marks a password's
// placeholder in FRR's config, and descriptions and AS paths with single spaces between words.
func TestBGPSchemas(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/router")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, spec string
		ok         bool
	}{
		{"BGP", `{"asn": 64512, "routerID": "192.0.2.1"}`, true},
		{"BGP", `{"asn": 64512, "routerID": "255.255.255.255"}`, true},
		{"BGP", `{"asn": 64512, "routerID": "192.0.2.256"}`, false},
		{"BGP", `{"asn": 64512, "routerID": "192.0.2.01"}`, false},
		{"BGP", `{"asn": 64512, "routerID": "999.0.2.1"}`, false},
		{"CommunityList", `{"communities": ["65535:65535", "0:0"]}`, true},
		{"CommunityList", `{"communities": ["65536:1"]}`, false},
		{"CommunityList", `{"communities": ["64512:70000"]}`, false},
		{"CommunityList", `{"communities": ["4294967295:4294967295:1"]}`, true},
		{"CommunityList", `{"communities": ["4294967296:1:1"]}`, false},
		{"CommunityList", `{"communities": ["64512:1:9999999999"]}`, false},
		{"RoutingPolicy", `{"terms": [{"then": {"action": "accept", "addCommunities": ["64512:65536"]}}]}`, false},
		{"RoutingPolicy", `{"terms": [{"then": {"action": "accept", "removeCommunities": ["64512:1:4294967296"]}}]}`, false},
		{"RoutingPolicy", `{"terms": [{"match": {"asPath": "^64512_ 64513$"}, "then": {"action": "accept"}}]}`, true},
		{"RoutingPolicy", `{"terms": [{"match": {"asPath": "^64512  64513$"}, "then": {"action": "accept"}}]}`, false},
		{"RoutingPolicy", `{"terms": [{"match": {"asPath": " ^64512$"}, "then": {"action": "accept"}}]}`, false},
		{"BGPPeer", `{"address": "198.51.100.1", "remoteAS": 64496, "description": "transit of AS 64496"}`, true},
		{"BGPPeer", `{"address": "198.51.100.1", "remoteAS": 64496, "description": "transit  of AS 64496"}`, false},
		{"BGPPeer", `{"address": "198.51.100.1", "remoteAS": 64496, "description": "transit "}`, false},
		{"BGPPeer", `{"address": "198.51.100.1", "remoteAS": 64496, "description": "x password @secret_bgp.fabric@"}`, false},
	} {
		d := builtinDoc(t, config.RouterAPI, tc.kind, "a", tc.spec)
		if err := s.Validate(d.Object); (err == nil) != tc.ok {
			t.Errorf("%s %s: err = %v, want accepted %v", tc.kind, tc.spec, err, tc.ok)
		}
	}
}

// The BGP kinds' rules per VRF accept a BGP document per VRF that imports another VRF's routes,
// and peers in each, and refuse an instance that imports its own VRF, an import of a VRF without
// BGP or through a policy of the other family, a peer in a VRF without BGP, and a default-route
// network outside the default VRF. A peer's interface of another VRF is the output frr's to
// refuse.
func TestBGPPerVRF(t *testing.T) {
	set, doc := routerRules(t)
	// eth0 stays out of every VRF, as the VRF kind's rules want of a node whose node API answers
	// on every interface.
	base := []config.Document{
		network(t, "10-eth0", `{"name": "eth0"}`),
		doc("VRF", "tenant", `{"table": 100, "interfaces": ["eth2"]}`),
		doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "import": {"ipv6": {"vrfs": ["tenant"], "policy": "from-tenant"}}}`),
		doc("BGP", "tenant", `{"vrf": "tenant", "asn": 64512, "routerID": "192.0.2.1", "import": {"ipv6": {"vrfs": ["default"]}}}`),
		doc("BGPPeer", "fabric", `{"interfaces": ["eth1"], "remoteAS": "external", "families": {"ipv6": {}}}`),
		doc("BGPPeer", "leaves", `{"vrf": "tenant", "interface": "eth2", "remoteAS": "external", "families": {"ipv6": {}}}`),
		doc("PrefixList", "tenant", `{"family": "ipv6", "entries": [{"prefix": "2001:db8:100::/40", "le": 64}]}`),
		doc("RoutingPolicy", "from-tenant", `{"terms": [{"match": {"prefixList": "tenant"}, "then": {"action": "accept"}}]}`),
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
		{"an instance per VRF", base, nil},
		{"an instance that imports its own VRF", replace(base, doc("BGP", "tenant", `{"vrf": "tenant", "asn": 64512, "routerID": "192.0.2.1", "import": {"ipv6": {"vrfs": ["default", "tenant"]}}}`)),
			[]string{"BGP tenant imports the routes of its own VRF; name other VRFs"}},
		{"an import of a VRF without BGP", replace(base, doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "import": {"ipv4": {"vrfs": ["lab"]}}}`)),
			[]string{`BGP/bgp: spec.import: the import names the VRF lab, which no BGP document serves; leaking takes routes from BGP alone`}},
		{"an import's policy of the other family", replace(base, doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "import": {"ipv4": {"vrfs": ["tenant"], "policy": "from-tenant"}}}`)),
			[]string{`BGP/bgp: spec.import: the ipv4 import's RoutingPolicy matches PrefixLists of another family`}},
		{"a peer in a VRF without BGP", replace(base, doc("VRF", "lab", `{"table": 200, "interfaces": ["eth3"]}`),
			doc("BGPPeer", "lab", `{"vrf": "lab", "interface": "eth3", "remoteAS": "external", "families": {"ipv6": {}}}`)),
			[]string{"BGPPeer lab runs in the VRF lab: a peer's VRF needs a BGP document, which gives the router its AS number there"}},
		// The output frr refuses a peer's interface of another VRF, as networkd puts the link there.
		{"a peer's interface in another VRF", replace(base, doc("BGPPeer", "fabric", `{"interface": "eth2", "remoteAS": "external", "families": {"ipv6": {}}}`)), nil},
		{"a default-route network in a VRF", replace(base, doc("BGP", "tenant", `{"vrf": "tenant", "asn": 64512, "routerID": "192.0.2.1", "networks": [{"prefix": "2001:db8:100::/40"}, {"prefix": "::/0"}]}`)),
			[]string{"BGP tenant announces the network ::/0 in VRF tenant, which would always be announced, as a VRF's table ends in an unreachable default route; use a peer family's defaultOriginate"}},
		{"an IPv4 default-route network with host bits in a VRF", replace(base, doc("BGP", "tenant", `{"vrf": "tenant", "asn": 64512, "routerID": "192.0.2.1", "networks": [{"prefix": "10.0.0.0/0"}]}`)),
			[]string{"BGP tenant announces the network 10.0.0.0/0 in VRF tenant, which would always be announced, as a VRF's table ends in an unreachable default route; use a peer family's defaultOriginate"}},
		{"a default-route network in the default VRF", replace(base, doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "networks": [{"prefix": "0.0.0.0/0"}, {"prefix": "::/0"}]}`)), nil},
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

// FRR keeps its default instance while another VRF's runs, so a delivery that drops the default
// VRF's BGP document drops every running instance with it; it may add an instance of a VRF that
// did not run, as a router whose instance moves into a VRF does.
func TestBGPKeepsTheDefaultInstance(t *testing.T) {
	set, doc := routerRules(t)
	eth0 := network(t, "10-eth0", `{"name": "eth0"}`)
	tenant := doc("VRF", "tenant", `{"table": 100}`)
	def := doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1"}`)
	inTenant := doc("BGP", "tenant", `{"vrf": "tenant", "asn": 64512, "routerID": "192.0.2.1"}`)
	moved := doc("BGP", "bgp", `{"vrf": "tenant", "asn": 64512, "routerID": "192.0.2.1"}`)
	// A running config of an image whose BGP has no vrf holds documents without the field.
	before := builtinDoc(t, config.RouterAPI, "BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1"}`)
	refused := []string{"the config drops the default VRF's BGP document and keeps a running BGP instance of another VRF, which FRR does not allow; drop the other VRFs' BGP documents in the same delivery, or keep the default VRF's"}
	for _, tc := range []struct {
		name      string
		old, docs []config.Document
		want      []string
	}{
		{"the default instance dropped, another kept", []config.Document{eth0, tenant, def, inTenant}, []config.Document{eth0, tenant, inTenant}, refused},
		{"every instance dropped", []config.Document{eth0, tenant, def, inTenant}, []config.Document{eth0, tenant}, nil},
		{"an instance moved into a VRF", []config.Document{eth0, tenant, def}, []config.Document{eth0, tenant, moved}, nil},
		{"another VRF's instance dropped", []config.Document{eth0, tenant, def, inTenant}, []config.Document{eth0, tenant, def}, nil},
		{"a first delivery", nil, []config.Document{eth0, tenant, inTenant}, nil},
		{"a running config without vrf", []config.Document{eth0, before}, []config.Document{eth0, tenant, def, inTenant}, nil},
		{"a running config without vrf, its instance dropped", []config.Document{eth0, before}, []config.Document{eth0, tenant, inTenant}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: tc.docs, Old: tc.old, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures %q, want %q", got, tc.want)
			}
		})
	}
}
