package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	// routerSchemas are the router kinds' schemas, as a router image generates them.
	routerSchemas = "../../test/fixtures/kinds/router"
	// routerExamples are the examples of the outputs router-sysctl, router-nftables and frr.
	routerExamples = "../../modules/node/examples/router"
)

// routerKinds are the kinds of router.chalkos.dev.
var routerKinds = []string{"Router", "FirewallZone", "FirewallPolicy", "AddressSet", "NATRule", "BGP", "BGPPeer", "PrefixList", "CommunityList", "RoutingPolicy"}

// routerInput is the input of the router outputs for the config file path, as chalkd builds it:
// the documents of each router kind with their schema defaults, sorted by name.
func routerInput(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	docs, err := config.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.LoadSchemas(routerSchemas)
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string][]any{}
	for _, kind := range append(slices.Clone(routerKinds), "Network") {
		byKind[kind] = []any{}
	}
	slices.SortStableFunc(docs, func(a, b config.Document) int { return cmp.Compare(a.Name, b.Name) })
	for _, d := range docs {
		// The Network documents' [Match] names the links a BGPPeer's interfaces expand to; the
		// renderers read nothing a default fills in.
		if d.APIVersion == config.NetworkdAPI && d.Kind == "Network" {
			byKind[d.Kind] = append(byKind[d.Kind], d.Object)
			continue
		}
		if d.APIVersion != config.RouterAPI {
			continue
		}
		filled, err := s.Default(d.Object)
		if err != nil {
			t.Fatal(err)
		}
		byKind[d.Kind] = append(byKind[d.Kind], filled)
	}
	data, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": byKind})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// golden compares got with the golden file testdata/router/<name>, or rewrites it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "router", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs; run go test ./cmd/chalkos-render -update and review the diff:\n%s", path, got)
	}
}

// renderedFile renders the router output of args from input and returns its file.
func renderedFile(t *testing.T, args []string, input, file string) string {
	t.Helper()
	out, err := render(t, args, input)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Each example of the router outputs renders its golden sysctl file, ruleset and FRR config: no
// documents, a leaf that only routes, a fabric edge and a LAN router.
func TestRouterGolden(t *testing.T) {
	examples, err := filepath.Glob(filepath.Join(routerExamples, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 4 {
		t.Fatalf("examples = %v", examples)
	}
	for _, path := range examples {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			input := routerInput(t, path)
			golden(t, name+".conf", renderedFile(t, []string{"router-sysctl"}, input, sysctlFile))
			golden(t, name+".nft", renderedFile(t, []string{"router-nftables", "50000"}, input, rulesetFile))
			golden(t, name+".frr", renderedFile(t, []string{"frr"}, input, frrFile))
		})
	}
}

// The fallback ruleset is that of a node without documents, but its forward chain drops what the
// protected rules do not accept.
func TestRouterFallbackGolden(t *testing.T) {
	golden(t, "fallback.nft", renderedFile(t, []string{"router-nftables-fallback", "50000"}, `{"node": {"name": ""}, "documents": {}}`, rulesetFile))
}

// A node without a Router document renders the kind's defaults, as the schema gives them.
func TestDefaultRouterMatchesTheSchema(t *testing.T) {
	s, err := schema.LoadSchemas(routerSchemas)
	if err != nil {
		t.Fatal(err)
	}
	filled, err := s.Default(map[string]any{"apiVersion": config.RouterAPI, "kind": "Router", "metadata": map[string]any{"name": "router"}, "spec": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(filled["spec"])
	if err != nil {
		t.Fatal(err)
	}
	var fromSchema routerSpec
	if err := json.Unmarshal(data, &fromSchema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromSchema, defaultRouter()) {
		t.Errorf("the schema's defaults %+v, defaultRouter %+v", fromSchema, defaultRouter())
	}
}

// An opening of a service renders after chalkd's port: a line per family of its sources, or one
// without sources, scoped to its interfaces.
func TestRouterOpenings(t *testing.T) {
	d, err := readRouterDocuments(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	fw, err := newFirewall(d, 50000, false)
	if err != nil {
		t.Fatal(err)
	}
	fw.openings = []opening{
		{comment: "BGPPeer fabric", protocol: "tcp", ports: []portRange{"179"}, interfaces: []string{"eth1", "eth2"}},
		{comment: "Resolver resolver", protocol: "udp", ports: []portRange{"53"}, sources: []string{"192.0.2.0/24", "2001:db8::/32"}},
	}
	text, err := fw.ruleset()
	if err != nil {
		t.Fatal(err)
	}
	want := "\t\ttcp dport 50000 accept\n" +
		"\t\tiifname { \"eth1\", \"eth2\" } tcp dport { 179 } accept comment \"BGPPeer fabric\"\n" +
		"\t\tip saddr { 192.0.2.0/24 } udp dport { 53 } accept comment \"Resolver resolver\"\n" +
		"\t\tip6 saddr { 2001:db8::/32 } udp dport { 53 } accept comment \"Resolver resolver\"\n" +
		"\t}\n"
	if !strings.Contains(text, want) {
		t.Errorf("the input chain lacks\n%s\nin\n%s", want, text)
	}
}

// The renderers refuse what the schemas and rules refuse, should a document bypass them.
func TestRouterRefusals(t *testing.T) {
	doc := func(kind, name, spec string) any {
		var s any
		dec := json.NewDecoder(strings.NewReader(spec))
		dec.UseNumber()
		if err := dec.Decode(&s); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"apiVersion": config.RouterAPI, "kind": kind, "metadata": map[string]any{"name": name}, "spec": s}
	}
	zone := doc("FirewallZone", "wan", `{"interfaces": ["eth0"]}`)
	rule := func(r string) any {
		return doc("FirewallPolicy", "wan-local", `{"from": "wan", "to": "local", "default": "drop", "rules": [`+r+`]}`)
	}
	for name, c := range map[string]struct {
		args []string
		docs map[string][]any
		want string
	}{
		"a port that is no number": {[]string{"router-nftables", "chalkd"}, nil, `"chalkd" is no TCP port of chalkd`},
		"an unknown hash":          {[]string{"router-sysctl"}, map[string][]any{"Router": {doc("Router", "r", `{"forwarding": {"ipv4": true, "ipv6": true}, "rpFilter": "loose", "ecmpHash": "l5", "conntrackMax": null, "management": {"interfaces": null}}`)}}, `ecmpHash "l5" is neither l3 nor l4`},
		"a zone that is missing":   {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallPolicy": {rule(`{"action": "accept"}`)}}, "names the zone wan, which no FirewallZone declares"},
		"an inverted range":        {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"protocol": "tcp", "destinationPorts": ["90-80"], "action": "accept"}`)}}, `"90-80" is no range of ports`},
		"port zero":                {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"protocol": "tcp", "destinationPorts": [0], "action": "accept"}`)}}, "a port is a number from 1 to 65535"},
		"a range from port zero":   {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"protocol": "tcp", "destinationPorts": ["0-80"], "action": "accept"}`)}}, `"0-80" is no range of ports`},
		"a target port zero":       {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "NATRule": {doc("NATRule", "n", `{"type": "dnat", "family": "ipv4", "in": {"zone": "wan", "interface": null}, "out": null, "match": {"source": [], "destination": [], "protocol": "tcp", "destinationPorts": []}, "to": {"address": "192.0.2.1", "port": 0}}`)}}, "to.port 0 is no port"},
		"a set beside ranges":      {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"source": ["set:a", "192.0.2.0/24"], "action": "accept"}`)}}, "set:a stands alone"},
		"ports without a protocol": {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"destinationPorts": [22], "action": "accept"}`)}}, "ports and ICMP types need a protocol"},
		"masquerade with to":       {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "NATRule": {doc("NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": {"zone": "wan", "interface": null}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": {"address": "192.0.2.1", "port": null}}`)}}, "masquerade translates to the interface's address and has no to"},
		// Values nftables would read as syntax of the ruleset.
		"an ICMP type that adds a rule": {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"protocol": "icmp", "icmpTypes": ["echo-request } ip saddr 0.0.0.0/0 accept; icmp type { echo-reply"], "action": "drop"}`)}}, `is no ICMP type the firewall matches`},
		"a family that adds a match":    {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"family": "ipv4 ip saddr 0.0.0.0/0", "action": "accept"}`)}}, `family "ipv4 ip saddr 0.0.0.0/0" is neither ipv4 nor ipv6`},
		"a policy name that adds rules": {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {doc("FirewallPolicy", "x { accept; }; chain y", `{"from": "wan", "to": "local", "default": "drop", "rules": []}`)}}, `"x { accept; }; chain y" is no document name`},
		"a set name in capitals":        {[]string{"router-nftables", "50000"}, map[string][]any{"AddressSet": {doc("AddressSet", "Office", `{"family": "ipv4", "prefixes": ["192.0.2.0/24"]}`)}}, `"Office" is no document name`},
		"an interface that ends a set":  {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {doc("FirewallZone", "wan", `{"interfaces": ["eth0\" } accept; iifname { \"eth1"]}`)}}, `is no interface name or glob`},
		"a glob inside a name":          {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {doc("FirewallZone", "wan", `{"interfaces": ["e*h0"]}`)}}, `"e*h0" is no interface name or glob`},
		"a management interface":        {[]string{"router-nftables", "50000"}, map[string][]any{"Router": {doc("Router", "router", `{"forwarding": {"ipv4": true, "ipv6": true}, "rpFilter": "loose", "ecmpHash": "l4", "conntrackMax": null, "management": {"interfaces": ["eth0 accept"]}}`)}}, `"eth0 accept" is no interface name or glob`},
		"a NAT rule's interface":        {[]string{"router-nftables", "50000"}, map[string][]any{"NATRule": {doc("NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": {"zone": null, "interface": "eth0\"; accept"}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)}}, `is no interface name or glob`},
		"no management interface":       {[]string{"router-nftables", "50000"}, map[string][]any{"Router": {doc("Router", "router", `{"forwarding": {"ipv4": true, "ipv6": true}, "rpFilter": "loose", "ecmpHash": "l4", "conntrackMax": null, "management": {"interfaces": []}}`)}}, "management.interfaces names no interface"},
		"a zone without interfaces":     {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {doc("FirewallZone", "wan", `{"interfaces": []}`)}}, "FirewallZone wan names no interface"},
		// A BGPPeer's ports open on its interfaces or from its address alone.
		"a peer's interface that ends a set": {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"interface": "eth1\" } accept; iifname { \"eth2"}`)}}, `is no interface name or glob`},
		"a peer's address with a zone":       {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"address": "fe80::1%eth1"}`)}}, `BGPPeer a: address "fe80::1%eth1" is no neighbour address`},
		"a peer's address range":             {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"address": "0.0.0.0/0"}`)}}, `BGPPeer a: address "0.0.0.0/0" is no neighbour address`},
		"a peer's unspecified address":       {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"address": "::"}`)}}, `BGPPeer a: address "::" is no neighbour address`},
		"a peer of no interface":             {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"interfaces": []}`)}}, "BGPPeer a names no interface"},
		"a peer of nothing":                  {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{}`)}}, "BGPPeer a names not exactly one of address, interface and interfaces"},
		"a peer of an address and links":     {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"address": "192.0.2.9", "interfaces": ["eth1"]}`)}}, "BGPPeer a names not exactly one of address, interface and interfaces"},
	} {
		t.Run(name, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": c.docs})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := render(t, c.args, string(input)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// The FRR renderer refuses what the schemas and rules refuse, should a document bypass them, and
// what FRR's mgmtd refuses when it applies the config at boot, which would leave all of the
// config's lists and route-maps out.
func TestFRRRefusals(t *testing.T) {
	doc := func(kind, name, spec string) any {
		var s any
		dec := json.NewDecoder(strings.NewReader(spec))
		dec.UseNumber()
		if err := dec.Decode(&s); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"apiVersion": config.RouterAPI, "kind": kind, "metadata": map[string]any{"name": name}, "spec": s}
	}
	bgp := func(more string) any {
		return doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false},
			"bfd": {"receiveMs": 300, "transmitMs": 300, "multiplier": 3}, "gracefulRestart": false, "networks": [`+more+`]}`)
	}
	peer := func(name, spec string) any { return doc("BGPPeer", name, spec) }
	list := func(name, family, entries string) any {
		return doc("PrefixList", name, `{"family": "`+family+`", "entries": [`+entries+`]}`)
	}
	policy := func(name, terms string) any {
		return doc("RoutingPolicy", name, `{"terms": [`+terms+`], "default": "reject"}`)
	}
	v6 := list("v6", "ipv6", `{"prefix": "2001:db8::/32", "action": "permit"}`)
	v4 := list("v4", "ipv4", `{"prefix": "192.0.2.0/24", "action": "permit"}`)
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"peers without BGP":      {map[string][]any{"BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, "the config holds BGPPeer documents but no BGP document"},
		"two BGP documents":      {map[string][]any{"BGP": {bgp(""), doc("BGP", "other", `{}`)}}, "the config holds 2 BGP documents"},
		"an IPv6 router ID":      {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "2001:db8::1"}`)}}, `routerID "2001:db8::1" is no IPv4 unicast address`},
		"an unknown remote AS":   {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "anyone", "families": {"ipv6": {}}}`)}}, "remoteAS is external, internal or an AS number"},
		"a community of 17 bits": {map[string][]any{"BGP": {bgp("")}, "CommunityList": {doc("CommunityList", "c", `{"communities": ["65536:1"]}`)}}, `"65536:1" is no community`},
		"ge below the prefix":    {map[string][]any{"BGP": {bgp("")}, "PrefixList": {list("l", "ipv4", `{"prefix": "192.0.2.0/24", "ge": 16, "action": "permit"}`)}}, "ge 16 of 192.0.2.0/24 lies outside 24 to 32"},
		"a policy of both families": {map[string][]any{"BGP": {bgp("")}, "PrefixList": {v4, v6},
			"RoutingPolicy": {policy("p", `{"match": {"prefixList": "v4"}, "then": {"action": "accept"}}, {"match": {"prefixList": "v6"}, "then": {"action": "accept"}}`)}}, "it matches PrefixLists of both families"},
		"a network's policy of the other family": {map[string][]any{"BGP": {bgp(`{"prefix": "192.0.2.0/24", "policy": "p"}`)}, "PrefixList": {v6},
			"RoutingPolicy": {policy("p", `{"match": {"prefixList": "v6"}, "then": {"action": "accept"}}`)}}, "the RoutingPolicy p matches PrefixLists of ipv6, not ipv4"},
		"two peers of one session": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {
			peer("a", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {}}}`),
			peer("b", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, "BGPPeer a and BGPPeer b both name eth1"},
		"an interface of a peer group's name": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {
			peer("fabric", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {}}}`),
			peer("x", `{"interface": "peer-fabric", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, "BGPPeer x names peer-fabric, the peer group of BGPPeer fabric"},
		"a password's key with a space":         {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "password": {"secretRef": {"name": "bgp", "key": "fab ric"}}, "families": {"ipv6": {}}}`)}}, `the password's Secret "bgp" and key "fab ric"`},
		"an AS path that adds a command":        {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "^1$\n neighbor x shutdown"}, "then": {"action": "accept"}}`)}}, "holds more than digits, spaces and"},
		"a description of two lines":            {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "description": "a\nb", "families": {"ipv6": {}}}`)}}, "the description is 1 to 80 printable ASCII characters"},
		"a peer of no family":                   {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external"}`)}}, "it exchanges the routes of no family"},
		"65 equal paths":                        {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", "multipath": {"ebgp": 65, "ibgp": 1}, "bfd": {"receiveMs": 300, "transmitMs": 300, "multiplier": 3}}`)}}, "multipath.ebgp 65 lies outside 1 to 64"},
		"a prepend of 11":                       {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"then": {"action": "accept", "prepend": 11}}`)}}, "prepend 11 lies outside 1 to 10"},
		"an AS path that does not compile":      {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "(1"}, "then": {"action": "accept"}}`)}}, `asPath "(1" is no regular expression`},
		"a maximum of no prefix":                {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {"maxPrefix": 0}}}`)}}, "maxPrefix 0 accepts no route"},
		"an interface that reads as an address": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "192.0.2.9", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, `interface "192.0.2.9" is an address`},
		"a link that reads as an address": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interfaces": ["*"], "remoteAS": "external", "families": {"ipv6": {}}}`)},
			"Network": {map[string]any{"apiVersion": config.NetworkdAPI, "kind": "Network", "metadata": map[string]any{"name": "n"}, "spec": map[string]any{"match": map[string]any{"name": "2001:db8::9"}}}}}, `interface "2001:db8::9" is an address`},
		"a description of spaces":              {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "description": "  ", "families": {"ipv6": {}}}`)}}, "the description is 1 to 80 printable ASCII characters"},
		"a description of two spaces in a row": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "description": "a  b", "families": {"ipv6": {}}}`)}}, "the description is 1 to 80 printable ASCII characters"},
		"a description that ends in a space":   {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "description": "a ", "families": {"ipv6": {}}}`)}}, "the description is 1 to 80 printable ASCII characters"},
		"a description of 81 characters":       {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "description": "`+strings.Repeat("a", 81)+`", "families": {"ipv6": {}}}`)}}, "the description is 1 to 80 printable ASCII characters"},
		"a large community of 33 bits":         {map[string][]any{"BGP": {bgp("")}, "CommunityList": {doc("CommunityList", "c", `{"communities": ["1:4294967296:1"]}`)}}, `"1:4294967296:1" is no community`},
		"a router ID of no host":               {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "0.0.0.0"}`)}}, `routerID "0.0.0.0" is no IPv4 unicast address`},
		"a broadcast router ID":                {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "255.255.255.255"}`)}}, `routerID "255.255.255.255" is no IPv4 unicast address`},
		"a multicast router ID":                {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "224.0.0.5"}`)}}, `routerID "224.0.0.5" is no IPv4 unicast address`},
		"two networks of one prefix":           {map[string][]any{"BGP": {bgp(`{"prefix": "192.0.2.0/24"}, {"prefix": "192.0.2.1/24"}`)}}, "networks 192.0.2.0/24 and 192.0.2.1/24 both announce 192.0.2.0/24"},
		"a community twice":                    {map[string][]any{"BGP": {bgp("")}, "CommunityList": {doc("CommunityList", "c", `{"communities": ["1:1", "01:1"]}`)}}, "CommunityList c holds 1:1 twice"},
		"a link-local address":                 {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"address": "fe80::1", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, `address "fe80::1" is no neighbour address`},
		"an IPv4-mapped address":               {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"address": "::ffff:192.0.2.9", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, `address "::ffff:192.0.2.9" is no neighbour address`},
		"a loopback address":                   {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"address": "127.0.0.1", "remoteAS": "external", "families": {"ipv4": {}}}`)}}, `address "127.0.0.1" is no neighbour address`},
		"the IPv6 loopback address":            {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"address": "::1", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, `address "::1" is no neighbour address`},
		"IPv4 routes over IPv6 alone":          {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"address": "2001:db8::9", "remoteAS": "external", "families": {"ipv4": {}}}`)}}, "families.ipv4 at the IPv6 address 2001:db8::9 needs extendedNextHop"},
		// The credentials helper fills in a placeholder wherever a line ends in one, and vtysh shows
		// a description to every reader.
		"a description that holds a placeholder": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "description": "x password @secret_bgp.fabric@", "families": {"ipv6": {}}}`)}}, "the description is 1 to 80 printable ASCII characters"},
		"a router ID of five parts":              {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1.1"}`)}}, `routerID "192.0.2.1.1" is no IPv4 unicast address`},
		// mgmtd refuses a prefix-list entry equal to another of its list.
		"two equal entries":                {map[string][]any{"BGP": {bgp("")}, "PrefixList": {list("l", "ipv4", `{"prefix": "192.0.2.1/24", "action": "permit"}, {"prefix": "192.0.2.0/24", "action": "deny"}`)}}, "entries 1 and 2 both match 192.0.2.0/24"},
		"two equal entries with ge and le": {map[string][]any{"BGP": {bgp("")}, "PrefixList": {list("l", "ipv6", `{"prefix": "2001:db8::/32", "action": "permit"}, {"prefix": "2001:db8::/32", "ge": 48, "le": 64, "action": "permit"}, {"prefix": "2001:db8::/32", "ge": 48, "le": 64, "action": "permit"}`)}}, "entries 2 and 3 both match 2001:db8::/32 ge 48 le 64"},
		// AS paths bgpd's regcomp refuses, which would leave the term matching no route.
		"an AS path with a parenthesis too many": {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "1)"}, "then": {"action": "accept"}}`)}}, `asPath "1)" is no regular expression`},
		"an AS path that starts with a star":     {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "*1"}, "then": {"action": "accept"}}`)}}, `asPath "*1" is no regular expression`},
		"an AS path that repeats an anchor":      {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "^*1"}, "then": {"action": "accept"}}`)}}, `asPath "^*1" is no regular expression`},
		"an AS path that repeats an alternative": {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "(1|+2)"}, "then": {"action": "accept"}}`)}}, `asPath "(1|+2)" is no regular expression`},
		"an AS path with an AS set's brace":      {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "_{64512"}, "then": {"action": "accept"}}`)}}, `asPath "_{64512" is no regular expression`},
		"an AS path with an open interval":       {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "1{,5}"}, "then": {"action": "accept"}}`)}}, `asPath "1{,5}" is no regular expression`},
		"an AS path with an inverted interval":   {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "1{3,2}"}, "then": {"action": "accept"}}`)}}, `asPath "1{3,2}" is no regular expression`},
		"an AS path with an unclosed interval":   {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "1{2"}, "then": {"action": "accept"}}`)}}, `asPath "1{2" is no regular expression`},
		// Values FRR would read as further commands or arguments of the config.
		"a description that adds a neighbour":         {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "description": "x\n neighbor 192.0.2.9 remote-as 64999", "families": {"ipv6": {}}}`)}}, "the description is 1 to 80 printable ASCII characters"},
		"a description that ends the router":          {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "description": "x\rexit\r", "families": {"ipv6": {}}}`)}}, "the description is 1 to 80 printable ASCII characters"},
		"a remote AS that adds a neighbour":           {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external\n neighbor 192.0.2.9 remote-as 64999", "families": {"ipv6": {}}}`)}}, "remoteAS is external, internal or an AS number"},
		"a fractional remote AS":                      {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": 64512.5, "families": {"ipv6": {}}}`)}}, "remoteAS is external, internal or an AS number"},
		"an address that adds an argument":            {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"address": "192.0.2.9 remote-as 64999", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, `address "192.0.2.9 remote-as 64999" is no neighbour address`},
		"an address with a zone":                      {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"address": "fe80::1%eth1", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, `address "fe80::1%eth1" is no neighbour address`},
		"a multicast address":                         {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"address": "ff02::1", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, `address "ff02::1" is no neighbour address`},
		"an interface that adds a command":            {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1\n neighbor 192.0.2.9 remote-as 64999", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, "is no interface name"},
		"interfaces that add a command":               {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interfaces": ["eth1 remote-as 64999"], "remoteAS": "external", "families": {"ipv6": {}}}`)}}, "is no interface name or glob"},
		"a password that adds a command":              {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "password": {"secretRef": {"name": "bgp@\n neighbor 192.0.2.9 remote-as 64999\n", "key": "k"}}, "families": {"ipv6": {}}}`)}}, "the password's Secret"},
		"a password's key with an @":                  {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "password": {"secretRef": {"name": "bgp", "key": "a@b"}}, "families": {"ipv6": {}}}`)}}, `the password's Secret "bgp" and key "a@b"`},
		"a router ID that adds a command":             {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1\n neighbor 192.0.2.9 remote-as 64999"}`)}}, "is no IPv4 unicast address"},
		"a network that adds an argument":             {map[string][]any{"BGP": {bgp(`{"prefix": "192.0.2.0/24 route-map x"}`)}}, `network "192.0.2.0/24 route-map x" is no address range`},
		"a prefix that adds an argument":              {map[string][]any{"BGP": {bgp("")}, "PrefixList": {list("l", "ipv4", `{"prefix": "192.0.2.0/24 le 32", "action": "permit"}`)}}, `"192.0.2.0/24 le 32" is no ipv4 address range`},
		"a prefix list action that adds a prefix":     {map[string][]any{"BGP": {bgp("")}, "PrefixList": {list("l", "ipv4", `{"prefix": "192.0.2.0/24", "action": "permit 0.0.0.0/0\nip prefix-list pl-l seq 1 permit"}`)}}, "is neither permit nor deny"},
		"a family that adds a word":                   {map[string][]any{"BGP": {bgp("")}, "PrefixList": {list("l", "ipv4 x", `{"prefix": "192.0.2.0/24", "action": "permit"}`)}}, `family "ipv4 x" is neither ipv4 nor ipv6`},
		"a community that adds an argument":           {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"then": {"action": "accept", "addCommunities": ["64512:1 no-export"]}}`)}}, `"64512:1 no-export" is no community`},
		"a community with a sign":                     {map[string][]any{"BGP": {bgp("")}, "CommunityList": {doc("CommunityList", "c", `{"communities": ["+1:1"]}`)}}, `"+1:1" is no community`},
		"a term's action that adds a command":         {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"then": {"action": "accept\nexit\nrouter bgp 64999"}}`)}}, "is neither accept nor reject"},
		"a policy's default that adds a command":      {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {doc("RoutingPolicy", "p", `{"terms": [], "default": "reject 1\nexit"}`)}}, "is neither accept nor reject"},
		"a policy name that adds a command":           {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {doc("RoutingPolicy", "p 10\nexit\nrouter bgp 64999", `{"terms": [], "default": "reject"}`)}}, "is no document name"},
		"a peer name that adds a command":             {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a remote-as 64999", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, "is no document name"},
		"a list a term names that adds a command":     {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"prefixList": "v4\n set metric 1"}, "then": {"action": "accept"}}`)}}, "does not exist"},
		"a policy a family names that adds a command": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {"import": "p in\n neighbor 192.0.2.9 remote-as 64999"}}}`)}}, "does not exist"},
		"a network's policy that adds a command":      {map[string][]any{"BGP": {bgp(`{"prefix": "192.0.2.0/24", "policy": "p\n neighbor 192.0.2.9 remote-as 64999"}`)}}, "does not exist"},
	} {
		t.Run(name, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": c.docs})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := render(t, []string{"frr"}, string(input)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A node name FRR's hostname does not take renders no hostname, whatever it holds.
func TestFRRHostname(t *testing.T) {
	for name, want := range map[string]string{"r1": "hostname r1\n", "r1\nrouter bgp 64999": "", "-r1": ""} {
		input, err := json.Marshal(map[string]any{"node": map[string]any{"name": name}, "documents": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		text := renderedFile(t, []string{"frr"}, string(input), frrFile)
		if got := regexp.MustCompile(`(?m)^hostname.*\n`).FindString(text); got != want || strings.Contains(text, "router bgp") {
			t.Errorf("node %q renders\n%s", name, text)
		}
	}
}

// checkASPath refuses each AS path glibc's regcomp refuses once bgpd has replaced its _, and takes
// the common ones it compiles.
func TestCheckASPath(t *testing.T) {
	for _, r := range []string{"^65001_", "_64512$", "^(64512|64513)_.*$", "^1{1,2}?$", "^$", ".*", "_6451(2|3)_", "(|1)", "1|", "}", "_*", "1{0}", "1{2,}", "1{1,2}{3}", "_^"} {
		if err := checkASPath(r); err != nil {
			t.Errorf("checkASPath(%q) = %v", r, err)
		}
	}
	for _, r := range []string{"(1", "*1", "^*1", "$*", "(1|+2)", "_{64512", "1{3,2}", "1{2", "{1}", "(*1)", "^+", "^?", "|*", "1{1000}"} {
		if err := checkASPath(r); err == nil {
			t.Errorf("checkASPath(%q) takes it", r)
		}
	}
}

// The links the Network documents name are the plain names of their [Match] sections' Name=; a
// value whose first name starts with ! inverts all of its names, as systemd reads it, and names
// none.
func TestNetworkLinks(t *testing.T) {
	network := func(match any) any { return map[string]any{"spec": map[string]any{"match": match}} }
	got := networkLinks(map[string]any{"Network": []any{
		network(map[string]any{"name": "swp1 swp2"}),
		network([]any{map[string]any{"name": []any{"eth0", "!eth1 eth2"}}, map[string]any{"name": "swp* eth3 !eth4"}}),
		network(map[string]any{"name": "!eth5 eth6"}),
	}})
	if want := []string{"eth0", "eth3", "swp1", "swp2"}; !slices.Equal(got, want) {
		t.Errorf("networkLinks = %v, want %v", got, want)
	}
}
