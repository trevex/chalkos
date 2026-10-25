package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// The BGP kinds' rules accept a fabric edge's documents and refuse a peer with two ways to reach
// it or with no family, peers without BGP, policies and lists the config lacks, lists of another
// family than a peer's family, a policy of both families, ge and le outside their range, a list of
// both kinds of community and a rejecting term that sets attributes.
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
			[]string{"the config holds BGPPeer documents but no BGP document, which gives the router its AS number; add one"}},
		{"a peer of no family", replace(edge, doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "remoteAS": 64496}`)),
			[]string{`BGPPeer "upstream" spec.families: the peer exchanges the routes of no family; give families.ipv4 or families.ipv6, an empty one accepting and sending every route`}},
		{"a policy the config lacks", replace(edge, doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "remoteAS": 64496, "families": {"ipv4": {"export": "to-upstream"}}}`)),
			[]string{`BGPPeer "upstream" spec.families: the ipv4 family names the RoutingPolicy to-upstream, which the config does not hold`}},
		{"a policy of another family", replace(edge, doc("BGPPeer", "upstream", `{"address": "198.51.100.1", "remoteAS": 64496, "families": {"ipv4": {"import": "from-fabric"}}}`)),
			[]string{`BGPPeer "upstream" spec.families: the ipv4 family's RoutingPolicy from-fabric matches a PrefixList of another family`}},
		{"a network's policy the config lacks", replace(edge, doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "networks": [{"prefix": "2001:db8:ffff::/48", "policy": "nat64"}]}`)),
			[]string{`BGP "bgp" spec.networks: a network names the RoutingPolicy nat64, which the config does not hold`}},
		{"a policy of both families", replace(edge, doc("PrefixList", "v4", `{"family": "ipv4", "entries": [{"prefix": "192.0.2.0/24"}]}`),
			doc("RoutingPolicy", "from-fabric", `{"terms": [{"match": {"prefixList": "fabric"}, "then": {"action": "accept"}}, {"match": {"prefixList": "v4"}, "then": {"action": "accept"}}]}`)),
			[]string{`RoutingPolicy "from-fabric" spec.terms: the terms match PrefixLists of both families; a policy serves the routes of one family, so give each family a policy of its own`}},
		{"a list the config lacks", replace(edge, doc("RoutingPolicy", "from-fabric", `{"terms": [{"match": {"prefixList": "leaves"}, "then": {"action": "accept"}}]}`)),
			[]string{`RoutingPolicy "from-fabric" spec.terms: a term names a PrefixList or a CommunityList that the config does not hold`}},
		{"a rejecting term that sets", replace(edge, doc("RoutingPolicy", "from-fabric", `{"terms": [{"match": {"prefixList": "fabric"}, "then": {"action": "reject", "med": 10}}]}`)),
			[]string{`RoutingPolicy "from-fabric" spec.terms: a term that rejects routes sets local preference, MED, communities or a prepend, which no route it rejects keeps; drop them or accept`}},
		{"ge below the prefix length", replace(edge, doc("PrefixList", "edge", `{"family": "ipv6", "entries": [{"prefix": "2001:db8:ffff::/48", "ge": 40}]}`)),
			[]string{"PrefixList edge has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"}},
		{"le below ge", replace(edge, doc("PrefixList", "edge", `{"family": "ipv6", "entries": [{"prefix": "2001:db8:ffff::/48", "ge": 64, "le": 56}]}`)),
			[]string{"PrefixList edge has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"}},
		{"le past IPv4's longest prefix", replace(edge, doc("PrefixList", "v4", `{"family": "ipv4", "entries": [{"prefix": "192.0.2.0/24", "le": 64}]}`)),
			[]string{"PrefixList v4 has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"}},
		{"an entry of another family", replace(edge, doc("PrefixList", "fabric", `{"family": "ipv4", "entries": [{"prefix": "2001:db8::/32"}]}`)),
			[]string{`BGPPeer "fabric" spec.families: the ipv6 family's RoutingPolicy from-fabric matches a PrefixList of another family`,
				"PrefixList fabric has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"}},
		{"standard and large communities", replace(edge, doc("CommunityList", "blackhole", `{"communities": ["64512:666", "64512:666:1"]}`)),
			[]string{"CommunityList blackhole holds standard and large communities; give each kind a list of its own"}},
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

// BGP keeps its AS number; a delivery without the BGP document lets the next one take another.
func TestBGPKeepsItsAS(t *testing.T) {
	set, doc := routerRules(t)
	running := []config.Document{doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1"}`)}
	changed := []config.Document{doc("BGP", "bgp", `{"asn": 64513, "routerID": "192.0.2.1"}`)}
	failures, err := set.Evaluate(Input{Documents: changed, Old: running, Node: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`BGP "bgp" spec.asn: the AS number stays 64512; to change it, deliver the config without the BGP document, which ends the router's sessions, and then with the new AS number`}
	if got := messages(failures); !slices.Equal(got, want) {
		t.Errorf("failures %q, want %q", got, want)
	}
	for _, old := range [][]config.Document{nil, changed} {
		if failures, err := set.Evaluate(Input{Documents: changed, Old: old, Node: "r1"}); err != nil || len(failures) != 0 {
			t.Errorf("from %v: %v, %v", old, messages(failures), err)
		}
	}
}
