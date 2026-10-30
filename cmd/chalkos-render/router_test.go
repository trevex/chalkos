package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	// routerSchemas are the router kinds' schemas, as a router image generates them.
	routerSchemas = "../../test/fixtures/kinds/router"
	// routerExamples are the examples of the outputs router-sysctl, router-nftables, frr, jool,
	// unbound and router-networkd.
	routerExamples = "../../modules/node/examples/router"
	// testTrustAnchor and testZoneFile stand for the store paths the image gives chalkos-render
	// unbound; the rules check replaces them with those of a router image.
	testTrustAnchor = "/etc/unbound/root.key"
	testZoneFile    = "/etc/unbound/ipv4only.arpa.zone"
)

// routerKinds are the kinds of router.chalkos.dev.
var routerKinds = []string{"Router", "FirewallZone", "FirewallPolicy", "AddressSet", "NATRule", "BGP", "BGPPeer", "PrefixList", "CommunityList", "RoutingPolicy", "NAT64", "Resolver", "VRF"}

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
	for _, kind := range append(slices.Clone(routerKinds), "Network", "NetDev") {
		byKind[kind] = []any{}
	}
	slices.SortStableFunc(docs, func(a, b config.Document) int { return cmp.Compare(a.Name, b.Name) })
	for _, d := range docs {
		// The Network documents' [Match] names the links a BGPPeer's interfaces expand to and the
		// links of a VRF, and the networkd documents become units; the renderers read nothing a
		// default fills in.
		if d.APIVersion == config.NetworkdAPI && (d.Kind == "Network" || d.Kind == "NetDev") {
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

// renderedFiles renders the router output of args from input and returns its files in the order of
// their names, each after a line with its name; an output without files returns nothing.
func renderedFiles(t *testing.T, args []string, input string) string {
	t.Helper()
	out, err := render(t, args, input)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(out, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("# " + e.Name() + "\n" + string(data))
	}
	return b.String()
}

// Each example of the router outputs renders its golden sysctl file, ruleset, FRR config, Jool
// instances, Unbound config and networkd units: no documents, a leaf that only routes, a fabric
// edge, a LAN router and an edge with VRFs.
func TestRouterGolden(t *testing.T) {
	examples, err := filepath.Glob(filepath.Join(routerExamples, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 5 {
		t.Fatalf("examples = %v", examples)
	}
	for _, path := range examples {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			input := routerInput(t, path)
			golden(t, name+".conf", renderedFile(t, []string{"router-sysctl"}, input, sysctlFile))
			golden(t, name+".nft", renderedFile(t, []string{"router-nftables", "50000"}, input, rulesetFile))
			golden(t, name+".frr", renderedFile(t, []string{"frr"}, input, frrFile))
			golden(t, name+".jool", renderedFiles(t, []string{"jool"}, input))
			golden(t, name+".unbound", renderedFiles(t, []string{"unbound", testTrustAnchor, testZoneFile}, input))
			golden(t, name+".networkd", renderedFiles(t, []string{"router-networkd", networkFixture}, input))
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

// An opening of a service renders after chalkd's port: a line per family of its sources and
// destinations, or one without either, scoped to its interfaces; a family its sources or its
// destinations lack has no line.
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
		{comment: "BGPPeer far", protocol: "tcp", ports: []portRange{"179"}, sources: []string{"192.0.2.9/32", "2001:db8::9/128"}},
		{comment: "Resolver resolver", protocol: "udp", ports: []portRange{"53"}, sources: []string{"192.0.2.0/24", "2001:db8::/32"}, destinations: []string{"2001:db8:ffff::1/128"}},
	}
	text, err := fw.ruleset()
	if err != nil {
		t.Fatal(err)
	}
	want := "\t\ttcp dport 50000 accept\n" +
		"\t\tiifname { \"eth1\", \"eth2\" } tcp dport { 179 } accept comment \"BGPPeer fabric\"\n" +
		"\t\tip saddr { 192.0.2.9/32 } tcp dport { 179 } accept comment \"BGPPeer far\"\n" +
		"\t\tip6 saddr { 2001:db8::9/128 } tcp dport { 179 } accept comment \"BGPPeer far\"\n" +
		"\t\tip6 saddr { 2001:db8::/32 } ip6 daddr { 2001:db8:ffff::1/128 } udp dport { 53 } accept comment \"Resolver resolver\"\n" +
		"\t}\n"
	if !strings.Contains(text, want) {
		t.Errorf("the input chain lacks\n%s\nin\n%s", want, text)
	}
}

// A BGPPeer opens BGP's port, and with bfd BFD's single-hop ports, on its links or from its
// address, and BFD's multi-hop port from its address alone.
func TestBGPOpenings(t *testing.T) {
	addr, link := "192.0.2.9", "eth1"
	got, err := bgpOpenings([]named[peerSpec]{
		{Name: "far", Spec: peerSpec{Address: &addr, BFD: true}},
		{Name: "near", Spec: peerSpec{Interface: &link, BFD: true}},
		{Name: "plain", Spec: peerSpec{Interfaces: []string{"swp*"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []opening{
		{comment: "BGPPeer far", protocol: "tcp", ports: []portRange{"179"}, sources: []string{"192.0.2.9/32"}},
		{comment: "BGPPeer far", protocol: "udp", ports: []portRange{"3784", "3785", "4784"}, sources: []string{"192.0.2.9/32"}},
		{comment: "BGPPeer near", protocol: "tcp", ports: []portRange{"179"}, interfaces: []string{"eth1"}},
		{comment: "BGPPeer near", protocol: "udp", ports: []portRange{"3784", "3785"}, interfaces: []string{"eth1"}},
		{comment: "BGPPeer plain", protocol: "tcp", ports: []portRange{"179"}, interfaces: []string{"swp*"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bgpOpenings =\n%+v\nwant\n%+v", got, want)
	}
}

// A Resolver opens DNS over UDP and TCP from its clients' ranges to the addresses it listens on,
// and refuses an address twice and values that would add to the ruleset.
func TestResolverOpenings(t *testing.T) {
	got, err := resolverOpenings([]named[resolverSpec]{
		{Name: "resolver", Spec: resolverSpec{Listen: []string{"2001:db8:ffff::1", "192.0.2.1"}, Allow: []string{"2001:db8::1/32", "192.0.2.0/24"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dns := opening{comment: "Resolver resolver", ports: []portRange{"53"}, sources: []string{"2001:db8::/32", "192.0.2.0/24"}, destinations: []string{"2001:db8:ffff::1/128", "192.0.2.1/32"}}
	udp, tcp := dns, dns
	udp.protocol, tcp.protocol = "udp", "tcp"
	if want := []opening{udp, tcp}; !reflect.DeepEqual(got, want) {
		t.Errorf("resolverOpenings =\n%+v\nwant\n%+v", got, want)
	}
	for spec, want := range map[string]string{
		`{"listen": ["2001:db8:ffff::1", "2001:DB8:ffff::1"], "allow": ["2001:db8::/32"]}`:            "Resolver resolver: listen: 2001:db8:ffff::1 appears twice",
		`{"listen": ["::ffff:192.0.2.1"], "allow": ["2001:db8::/32"]}`:                                `Resolver resolver: listen: "::ffff:192.0.2.1" is no unicast address`,
		`{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32\" } accept\n\t\tip6 saddr ::/0"]}`: "is no address range",
	} {
		refused(t, []string{"router-nftables", "50000"}, map[string][]any{"Resolver": {routerDoc(t, "Resolver", "resolver", spec)}}, want)
	}
}

// Unbound refuses every client but those of allow, whose lines follow the refusals: of two lines of
// one range the later decides, so an allow of a whole family opens it.
func TestUnboundAccessControl(t *testing.T) {
	for allow, want := range map[string]string{
		`["2001:db8::/32"]`: "  access-control: 0.0.0.0/0 refuse\n  access-control: ::/0 refuse\n  access-control: 2001:db8::/32 allow\n",
		`["::/0"]`:          "  access-control: 0.0.0.0/0 refuse\n  access-control: ::/0 refuse\n  access-control: ::/0 allow\n",
	} {
		var r resolverSpec
		if err := json.Unmarshal([]byte(`{"listen": ["2001:db8:ffff::1"], "allow": `+allow+`}`), &r); err != nil {
			t.Fatal(err)
		}
		text, err := unboundConfig(r, testTrustAnchor, testZoneFile)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "  interface: 2001:db8:ffff::1\n"+want+"  module-config:") {
			t.Errorf("allow %s renders\n%s\nwant\n%s", allow, text, want)
		}
	}
}

// A record's data renders as Unbound reads it: an address in its usual form, a name with the
// root's dot, and text between double quotes, in which a hash or semicolon starts no comment.
func TestRecordValues(t *testing.T) {
	for _, c := range []struct{ typ, value, want string }{
		{"A", "192.0.2.1", "192.0.2.1"},
		{"AAAA", "2001:DB8:0::1", "2001:db8::1"},
		{"CNAME", "r1.lan.example.org", "r1.lan.example.org."},
		{"PTR", "r1.lan.example.org", "r1.lan.example.org."},
		{"MX", "10 mail.lan.example.org", "10 mail.lan.example.org."},
		{"SRV", "0 1 993 mail.lan.example.org", "0 1 993 mail.lan.example.org."},
		{"TXT", "v=spf1 -all # a; b [c] (d) @e", `"v=spf1 -all # a; b [c] (d) @e"`},
	} {
		got, err := recordValue(c.typ, c.value)
		if err != nil || got != c.want {
			t.Errorf("recordValue(%s, %q) = %q, %v, want %q", c.typ, c.value, got, err, c.want)
		}
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
		"a zone without interfaces":     {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {doc("FirewallZone", "wan", `{"interfaces": []}`)}}, "FirewallZone wan: a FirewallZone names an interface or a VRF; give it interfaces, vrfs or both"},
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
			peer("b", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, "BGPPeer a and BGPPeer b both name eth1 in the VRF default"},
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
			"Network": {map[string]any{"apiVersion": config.NetworkdAPI, "kind": "Network", "metadata": map[string]any{"name": "n"}, "spec": map[string]any{"match": map[string]any{"name": "192.0.2.10"}}}}}, `interface "192.0.2.10" is an address`},
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
		network(map[string]any{"name": []any{"eth7", ""}}),
	}})
	if want := []string{"eth0", "eth3", "swp1", "swp2"}; !slices.Equal(got, want) {
		t.Errorf("networkLinks = %v, want %v", got, want)
	}
}

// routerDoc is a document of a router kind with the spec given in JSON, its numbers exact.
func routerDoc(t *testing.T, kind, name, spec string) any {
	t.Helper()
	var s any
	dec := json.NewDecoder(strings.NewReader(spec))
	dec.UseNumber()
	if err := dec.Decode(&s); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"apiVersion": config.RouterAPI, "kind": kind, "metadata": map[string]any{"name": name}, "spec": s}
}

// refused renders the output of args from docs and wants an error that holds want.
func refused(t *testing.T, args []string, docs map[string][]any, want string) {
	t.Helper()
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := render(t, args, string(input)); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want %q", err, want)
	}
}

// Jool's instance takes a prefix and a pool entry with their host bits cleared, as the rules
// compare them.
func TestJoolMasks(t *testing.T) {
	got, err := joolInstances([]named[nat64Spec]{{Name: "edge", Spec: nat64Spec{Prefix: "64:ff9b::1/96", Pool4: []poolEntry{{Prefix: "192.0.2.9/30", Ports: "61001-65535", Protocols: []string{"tcp"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if p, e := got[0].Global.Pool6, got[0].Pool4[0].Prefix; p != "64:ff9b::/96" || e != "192.0.2.8/30" {
		t.Errorf("pool6 %s, pool4 %s, want 64:ff9b::/96 and 192.0.2.8/30", p, e)
	}
}

// The Jool renderer refuses what the schemas and rules refuse, should a document bypass them, and
// what Jool refuses when it loads an instance.
func TestJoolRefusals(t *testing.T) {
	nat64 := func(name, spec string) any { return routerDoc(t, "NAT64", name, spec) }
	pool := func(entries string) any { return nat64("edge", `{"prefix": "64:ff9b::/96", "pool4": [`+entries+`]}`) }
	for name, c := range map[string]struct {
		docs []any
		want string
	}{
		"a name of 16 characters":              {[]any{nat64("abcdefghijklmnop", `{"prefix": "64:ff9b::/96", "pool4": []}`)}, "NAT64 abcdefghijklmnop: Jool takes an instance's name of 15 characters at most"},
		"a prefix of 95 bits":                  {[]any{nat64("edge", `{"prefix": "64:ff9b::/95", "pool4": []}`)}, `NAT64 edge: the prefix "64:ff9b::/95" is no IPv6 range of 32, 40, 48, 56, 64 or 96 bits`},
		"an IPv4 prefix":                       {[]any{nat64("edge", `{"prefix": "192.0.2.0/24", "pool4": []}`)}, `the prefix "192.0.2.0/24" is no IPv6 range`},
		"an IPv4-mapped prefix":                {[]any{nat64("edge", `{"prefix": "::ffff:0:0/96", "pool4": []}`)}, `the prefix "::ffff:0:0/96" is no IPv6 range`},
		"bits 64 to 71 set":                    {[]any{nat64("edge", `{"prefix": "2001:db8:0:0:ff00::/96", "pool4": []}`)}, "the prefix 2001:db8:0:0:ff00::/96 sets bits 64 to 71, which RFC 6052 keeps zero"},
		"a multicast prefix":                   {[]any{nat64("edge", `{"prefix": "ff0e::/96", "pool4": []}`)}, "the prefix ff0e::/96 holds link-local, multicast or unspecified addresses"},
		"a link-local prefix":                  {[]any{nat64("edge", `{"prefix": "fe80::/64", "pool4": []}`)}, "the prefix fe80::/64 holds link-local, multicast or unspecified addresses"},
		"the unspecified prefix":               {[]any{nat64("edge", `{"prefix": "::/96", "pool4": []}`)}, "the prefix ::/96 holds link-local, multicast or unspecified addresses"},
		"two NAT64s of one prefix":             {[]any{nat64("a", `{"prefix": "64:ff9b::/96", "pool4": []}`), nat64("b", `{"prefix": "64:ff9b::/64", "pool4": []}`)}, "NAT64 b and NAT64 a both translate addresses of 64:ff9b::/64"},
		"two NAT64s of the router's addresses": {[]any{nat64("a", `{"prefix": "64:ff9b::/96", "pool4": []}`), nat64("b", `{"prefix": "2001:db8:64::/96", "pool4": []}`)}, "NAT64 b and NAT64 a both take the router's own addresses, with an empty pool4"},
		"an IPv6 pool":                         {[]any{pool(`{"prefix": "2001:db8::/64", "ports": "61001-65535", "protocols": ["tcp"]}`)}, `NAT64 edge: pool entry 1: "2001:db8::/64" is no IPv4 range of 16 bits or more`},
		"a pool of a /15":                      {[]any{pool(`{"prefix": "198.18.0.0/15", "ports": "61001-65535", "protocols": ["tcp"]}`)}, `"198.18.0.0/15" is no IPv4 range of 16 bits or more`},
		"a pool of the unspecified range":      {[]any{pool(`{"prefix": "0.0.0.0/16", "ports": "61001-65535", "protocols": ["tcp"]}`)}, "0.0.0.0/16 holds addresses no connection leaves from"},
		"a pool of multicast":                  {[]any{pool(`{"prefix": "224.0.0.0/24", "ports": "61001-65535", "protocols": ["tcp"]}`)}, "224.0.0.0/24 holds addresses no connection leaves from"},
		"a pool of the broadcast address":      {[]any{pool(`{"prefix": "255.255.255.255/32", "ports": "61001-65535", "protocols": ["tcp"]}`)}, "255.255.255.255/32 holds addresses no connection leaves from"},
		"ephemeral ports":                      {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "1-65535", "protocols": ["tcp"]}`)}, "the ports 1-65535 take the kernel's ephemeral ports, 32768 to 60999, which Jool refuses"},
		"port zero":                            {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "0-100", "protocols": ["tcp"]}`)}, `"0-100" is no range of ports`},
		"no protocol":                          {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": []}`)}, "NAT64 edge: pool entry 1: it names no protocol"},
		"a protocol twice":                     {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["udp", "udp"]}`)}, "it names udp twice"},
		"an unknown protocol":                  {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["sctp"]}`)}, `"sctp" is none of tcp, udp and icmp`},
		"two entries of one port": {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp", "udp"]},
			{"prefix": "192.0.2.9/32", "ports": "65000-65535", "protocols": ["udp"]}`)}, "NAT64 edge: pool entry 2 shares udp addresses and ports with pool entry 1"},
		"two NAT64s of one pool's address": {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp"]}`),
			nat64("lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "192.0.2.10/32", "ports": "1-1000", "protocols": ["udp"]}]}`)}, "NAT64 lab: pool entry 1 shares addresses with the pool of NAT64 edge"},
		// Values that would add to the JSON Jool reads, were it written by hand.
		"a prefix that adds a key":   {[]any{nat64("edge", `{"prefix": "64:ff9b::/96\", \"framework\": \"iptables", "pool4": []}`)}, "is no IPv6 range"},
		"a pool that adds an entry":  {[]any{pool(`{"prefix": "192.0.2.8/30\"}, {\"prefix\": \"198.51.100.0/24", "ports": "61001-65535", "protocols": ["tcp"]}`)}, "is no IPv4 range"},
		"ports that add a key":       {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535\", \"mark\": \"1", "protocols": ["tcp"]}`)}, "is no range of ports"},
		"a protocol that adds a key": {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp\", \"mark\": \"1"]}`)}, "is none of tcp, udp and icmp"},
		"a name that adds a key":     {[]any{nat64("e\", \"x", `{"prefix": "64:ff9b::/96", "pool4": []}`)}, "is no document name"},
		"a prefix of a line break":   {[]any{nat64("edge", `{"prefix": "64:ff9b::/96\n", "pool4": []}`)}, "is no IPv6 range"},
		"a pool that ends the list":  {[]any{pool(`{"prefix": "192.0.2.8/30\"}]}", "ports": "61001-65535", "protocols": ["tcp"]}`)}, "is no IPv4 range"},
		"ports of a backslash":       {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535\\", "protocols": ["tcp"]}`)}, "is no range of ports"},
		"a protocol of a line break": {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp\n"]}`)}, "is none of tcp, udp and icmp"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"jool"}, map[string][]any{"NAT64": c.docs}, c.want) })
	}
}

// The Unbound renderer refuses what the schemas and rules refuse, should a document bypass them,
// what unbound-checkconf refuses, and values that would add to Unbound's config.
func TestUnboundRefusals(t *testing.T) {
	resolver := func(more string) any {
		return routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "forwarders": [], "dnssec": true, "dns64": null`+more+`}`)
	}
	zone := func(z string) any { return resolver(`, "localZones": [` + z + `]`) }
	record := func(typ, value string) any {
		return zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "` + typ + `", "value": ` + strconv.Quote(value) + `, "ttl": 3600}]}`)
	}
	args := []string{"unbound", testTrustAnchor, testZoneFile}
	for name, c := range map[string]struct {
		args []string
		docs []any
		want string
	}{
		"two resolvers":                 {args, []any{resolver(""), routerDoc(t, "Resolver", "other", `{"listen": ["2001:db8:ffff::2"], "allow": ["2001:db8::/32"]}`)}, "the config holds 2 Resolver documents; a node has one"},
		"no address":                    {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": [], "allow": ["2001:db8::/32"]}`)}, "Resolver resolver: it listens on no address"},
		"no client":                     {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": []}`)}, "Resolver resolver: it answers no client"},
		"the unspecified address":       {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["::"], "allow": ["2001:db8::/32"]}`)}, `listen: "::" is no unicast address`},
		"a link-local zone":             {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["fe80::1%eth0"], "allow": ["2001:db8::/32"]}`)}, `listen: "fe80::1%eth0" is no unicast address`},
		"a loopback forwarder":          {args, []any{resolver(`, "forwarders": ["127.0.0.53"]`)}, `forwarders: "127.0.0.53" is no unicast address`},
		"a DNS64 prefix of 95 bits":     {args, []any{resolver(`, "dns64": {"prefix": "64:ff9b::/95"}`)}, `dns64: the prefix "64:ff9b::/95" is no IPv6 range`},
		"a zone twice":                  {args, []any{zone(`{"name": "lan.example.org", "type": "static", "records": []}, {"name": "lan.example.org", "type": "refuse", "records": []}`)}, "local zone 2: lan.example.org appears twice"},
		"a zone in capitals":            {args, []any{zone(`{"name": "Lan.example.org", "type": "static", "records": []}`)}, `"Lan.example.org" is no DNS name in lower case`},
		"a record outside its zone":     {args, []any{zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "xlan.example.org", "type": "A", "value": "192.0.2.1", "ttl": 3600}]}`)}, `lan.example.org: record 1: "xlan.example.org" is no name of the zone`},
		"an A record of IPv6":           {args, []any{record("A", "2001:db8::1")}, `"2001:db8::1" is no IPv4 address`},
		"an IPv4-mapped AAAA record":    {args, []any{record("AAAA", "::ffff:192.0.2.1")}, `"::ffff:192.0.2.1" is no IPv6 address`},
		"an MX record without a name":   {args, []any{record("MX", "10")}, `"10" is not a preference and a name, a single space apart`},
		"an SRV record of a big port":   {args, []any{record("SRV", "0 1 65536 mail.lan.example.org")}, `"65536" is no number from 0 to 65535`},
		"a TXT record of a backslash":   {args, []any{record("TXT", `a\b`)}, "is no text of 1 to 255 printable characters without quotes or backslashes"},
		"an unknown record type":        {args, []any{record("NS", "ns.lan.example.org")}, `type "NS" is none of A, AAAA, CNAME, PTR, MX, SRV and TXT`},
		"an unknown zone type":          {args, []any{zone(`{"name": "lan.example.org", "type": "inform", "records": []}`)}, `type "inform" is none of static`},
		"a relative trust anchor":       {[]string{"unbound", "root.key", testZoneFile}, []any{resolver("")}, `"root.key" is no absolute path`},
		"a trust anchor with a dot dot": {[]string{"unbound", "/etc/../root.key", testZoneFile}, []any{resolver("")}, `"/etc/../root.key" is no absolute path`},
		// Values Unbound would read as further options.
		"an address that adds an option":     {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1\n  interface: 0.0.0.0"], "allow": ["2001:db8::/32"]}`)}, "is no unicast address"},
		"a client that adds an option":       {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32 allow\naccess-control: ::/0"]}`)}, "is no address range"},
		"a zone that adds an option":         {args, []any{zone(`{"name": "lan.example.org.\" static\n  local-zone: \"x", "type": "static", "records": []}`)}, "is no DNS name"},
		"a zone type that adds an option":    {args, []any{zone(`{"name": "lan.example.org", "type": "static\n  verbosity: 5", "records": []}`)}, "is none of static"},
		"an A record that adds data":         {args, []any{record("A", "192.0.2.1'\n  local-data: 'b.lan.example.org. A 192.0.2.2")}, "is no IPv4 address"},
		"a TXT record that ends the quote":   {args, []any{record("TXT", "x' \n  include: '/etc/passwd")}, "is no text of 1 to 255 printable characters"},
		"a TXT record of a line break":       {args, []any{record("TXT", "a\nb")}, "is no text of 1 to 255 printable characters"},
		"a CNAME that adds data":             {args, []any{record("CNAME", "b.lan.example.org.'\n  local-data: 'c")}, "is no DNS name"},
		"a trust anchor that adds an option": {[]string{"unbound", "/etc/root.key\"\n  include: \"/etc/passwd", testZoneFile}, []any{resolver("")}, "is no absolute path"},
		"a trust anchor of a hash":           {[]string{"unbound", "/etc/root.key#x", testZoneFile}, []any{resolver("")}, "is no absolute path"},
		"a zone that adds a server option":   {args, []any{zone(`{"name": "lan.example.org\nserver:\n  do-not-query-localhost: no", "type": "static", "records": []}`)}, "is no DNS name"},
		"a zone of a hash":                   {args, []any{zone(`{"name": "lan.example.org#x", "type": "static", "records": []}`)}, "is no DNS name"},
		"a TXT record that adds a server":    {args, []any{record("TXT", "x\"'\nserver:\n  do-not-query-localhost: no")}, "is no text of 1 to 255 printable characters"},
		"a TXT record of a quote":            {args, []any{record("TXT", `say "hi"`)}, "is no text of 1 to 255 printable characters"},
		"a TXT record of a single quote":     {args, []any{record("TXT", "it's")}, "is no text of 1 to 255 printable characters"},
		"a TXT record past 255 characters":   {args, []any{record("TXT", strings.Repeat("x", 256))}, "is no text of 1 to 255 printable characters"},
		"an AAAA record of a hash":           {args, []any{record("AAAA", "2001:db8::1 # x")}, "is no IPv6 address"},
		"an MX record that adds a server":    {args, []any{record("MX", "10 mail.lan.example.org.'\nserver:\n  do-not-query-localhost: no")}, "is not a preference and a name"},
		"an MX record of a leading zero":     {args, []any{record("MX", "010 mail.lan.example.org")}, `"010" is no number from 0 to 65535`},
		"an SRV record of two spaces":        {args, []any{record("SRV", "0 1  993 mail.lan.example.org")}, "is not a priority, a weight, a port and a name, a single space apart"},
		"a PTR in capitals":                  {args, []any{record("PTR", "Mail.lan.example.org")}, "is no DNS name"},
		"a CNAME of the root's dot":          {args, []any{record("CNAME", "mail.lan.example.org.")}, "is no DNS name"},
		"a forwarder that adds a server":     {args, []any{resolver(`, "forwarders": ["192.0.2.53\nserver:\n  do-not-query-localhost: no"]`)}, "is no unicast address"},
		"a client of a hash":                 {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32#x"]}`)}, "is no address range"},
		"a DNS64 prefix that adds an option": {args, []any{resolver(`, "dns64": {"prefix": "64:ff9b::/96\nserver:\n  do-not-query-localhost: no"}`)}, "is no IPv6 range"},
		"a link-local address":               {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["fe80::1"], "allow": ["2001:db8::/32"]}`)}, `listen: "fe80::1" is no unicast address`},
		"a link-local forwarder":             {args, []any{resolver(`, "forwarders": ["169.254.0.53"]`)}, `forwarders: "169.254.0.53" is no unicast address`},
		"a link-local DNS64":                 {args, []any{resolver(`, "dns64": {"prefix": "fe80::/96"}`)}, "the prefix fe80::/96 holds link-local, multicast or unspecified addresses"},
		"ipv4only.arpa as a zone":            {args, []any{resolver(`, "dns64": {"prefix": "64:ff9b::/96"}, "localZones": [{"name": "ipv4only.arpa", "type": "static", "records": []}]`)}, "local zone 1: ipv4only.arpa overlaps ipv4only.arpa, which DNS64 answers itself"},
		"a zone above ipv4only.arpa":         {args, []any{resolver(`, "dns64": {"prefix": "64:ff9b::/96"}, "localZones": [{"name": "arpa", "type": "static", "records": []}]`)}, "local zone 1: arpa overlaps ipv4only.arpa, which DNS64 answers itself"},
		"a zone below ipv4only.arpa":         {args, []any{resolver(`, "dns64": {"prefix": "64:ff9b::/96"}, "localZones": [{"name": "x.ipv4only.arpa", "type": "static", "records": []}]`)}, "local zone 1: x.ipv4only.arpa overlaps ipv4only.arpa, which DNS64 answers itself"},
		// What the rules refuse beyond Unbound's own checks.
		"an IPv4-mapped address": {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["::ffff:192.0.2.1"], "allow": ["2001:db8::/32"]}`)}, `listen: "::ffff:192.0.2.1" is no unicast address`},
		"an address twice":       {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1", "2001:DB8:ffff::1"], "allow": ["2001:db8::/32"]}`)}, "listen: 2001:db8:ffff::1 appears twice"},
		"a client range twice":   {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32", "2001:db8::1/32"]}`)}, "allow: 2001:db8::/32 appears twice"},
		"a forwarder twice":      {args, []any{resolver(`, "forwarders": ["192.0.2.53", "192.0.2.53"]`)}, "forwarders: 192.0.2.53 appears twice"},
		"a record twice": {args, []any{zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "AAAA", "value": "2001:db8::1", "ttl": 3600},
			{"name": "a.lan.example.org", "type": "AAAA", "value": "2001:DB8::1", "ttl": 300}]}`)}, "lan.example.org: record 2: a.lan.example.org. AAAA 2001:db8::1 appears twice"},
		"a record in two zones": {args, []any{zone(`{"name": "example.org", "type": "transparent", "records": [{"name": "a.lan.example.org", "type": "A", "value": "192.0.2.1", "ttl": 3600}]},
			{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "A", "value": "192.0.2.1", "ttl": 3600}]}`)}, "lan.example.org: record 1: a.lan.example.org. A 192.0.2.1 appears twice"},
		"a redirect zone's record below it": {args, []any{zone(`{"name": "lan.example.org", "type": "redirect", "records": [{"name": "a.lan.example.org", "type": "A", "value": "192.0.2.1", "ttl": 3600}]}`)},
			"lan.example.org: record 1: a.lan.example.org is no name of the redirect zone's own"},
		"a CNAME beside another record": {args, []any{zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "A", "value": "192.0.2.1", "ttl": 3600},
			{"name": "a.lan.example.org", "type": "CNAME", "value": "r1.lan.example.org", "ttl": 3600}]}`)}, "lan.example.org: record 2: a.lan.example.org holds a CNAME record and another record"},
		"two CNAMEs of one name": {args, []any{zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "CNAME", "value": "r1.lan.example.org", "ttl": 3600},
			{"name": "a.lan.example.org", "type": "CNAME", "value": "r2.lan.example.org", "ttl": 3600}]}`)}, "lan.example.org: record 2: a.lan.example.org holds a CNAME record and another record"},
		"a CNAME in two zones": {args, []any{zone(`{"name": "example.org", "type": "transparent", "records": [{"name": "a.lan.example.org", "type": "CNAME", "value": "r1.lan.example.org", "ttl": 3600}]},
			{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "TXT", "value": "x", "ttl": 3600}]}`)}, "lan.example.org: record 1: a.lan.example.org holds a CNAME record and another record"},
		"listen and allow of other families": {args, []any{routerDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["192.0.2.0/24"]}`)}, "Resolver resolver: it listens on no address of a family its clients' ranges are of"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, c.args, map[string][]any{"Resolver": c.docs}, c.want) })
	}
}

// A peer in a VRF has its ports opened on its VRF's links by the VRF's device and sdifname: the
// input hook sees a packet from a VRF's link with the device as iifname and the link as
// sdifname, so links of other VRFs that the peer's globs match stay closed. A peer of an address
// in a VRF is opened by the VRF's device and its address, and a peer of the VRF named default by
// iifname as one of no VRF.
func TestVRFPeerOpenings(t *testing.T) {
	tenant, def, addr := "tenant", defaultVRF, "192.0.2.9"
	openings, err := bgpOpenings([]named[peerSpec]{
		{Name: "leaves", Spec: peerSpec{Interfaces: []string{"swp*"}, VRF: &tenant}},
		{Name: "spines", Spec: peerSpec{Interfaces: []string{"eth*"}, VRF: &def}},
		{Name: "far", Spec: peerSpec{Address: &addr, VRF: &tenant, BFD: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range openings {
		lines, err := o.lines()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, lines...)
	}
	want := []string{
		`iifname "tenant" meta sdifname { "swp*" } tcp dport { 179 } accept comment "BGPPeer leaves"`,
		`iifname { "eth*" } tcp dport { 179 } accept comment "BGPPeer spines"`,
		`iifname "tenant" ip saddr { 192.0.2.9/32 } tcp dport { 179 } accept comment "BGPPeer far"`,
		`iifname "tenant" ip saddr { 192.0.2.9/32 } udp dport { 3784, 3785, 4784 } accept comment "BGPPeer far"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// FRR's config refuses BGP and BGPPeer documents per VRF that the rules refuse, should they
// bypass them, and values that would add to the config.
func TestFRRVRFRefusals(t *testing.T) {
	timers := `"multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false}, "bfd": {"receiveMs": 300, "transmitMs": 300, "multiplier": 3}`
	bgp := func(name string, asn int, more string) any {
		return routerDoc(t, "BGP", name, fmt.Sprintf(`{"asn": %d, "routerID": "192.0.2.1", %s%s}`, asn, timers, more))
	}
	peer := func(name, more string) any {
		return routerDoc(t, "BGPPeer", name, `{"remoteAS": "external", "families": {"ipv6": {}}, `+more+`}`)
	}
	tenant := routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"]}`)
	eth2 := document(t, "Network", "20-eth2", `{"match": {"name": "eth2"}}`)
	v6 := routerDoc(t, "PrefixList", "v6", `{"family": "ipv6", "entries": [{"prefix": "2001:db8::/32", "action": "permit"}]}`)
	from := routerDoc(t, "RoutingPolicy", "from", `{"terms": [{"match": {"prefixList": "v6"}, "then": {"action": "accept"}}], "default": "reject"}`)
	prepend := routerDoc(t, "RoutingPolicy", "p", `{"terms": [{"then": {"action": "accept", "prepend": 1}}], "default": "reject"}`)
	withTenant := func(docs map[string][]any) map[string][]any {
		docs["VRF"], docs["Network"] = []any{tenant}, []any{eth2}
		return docs
	}
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"two BGP documents of one VRF": {map[string][]any{"BGP": {bgp("bgp", 64512, ""), bgp("other", 64513, "")}},
			"BGP bgp and BGP other both serve the VRF default"},
		"an instance of a VRF without its document": {map[string][]any{"BGP": {bgp("tenant", 64512, `, "vrf": "tenant"`)}},
			`BGP tenant: the VRF "tenant" has no VRF document`},
		"a VRF that adds a command": {withTenant(map[string][]any{"BGP": {bgp("x", 64512, `, "vrf": "tenant\nrouter bgp 64999"`)}}),
			`the VRF "tenant\nrouter bgp 64999" has no VRF document`},
		"a peer in a VRF without BGP": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, "")}, "BGPPeer": {peer("leaves", `"vrf": "tenant", "interface": "eth2"`)}}),
			"BGPPeer leaves runs in the VRF tenant, which no BGP document serves"},
		"an interface of a VRF's device": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, "")}, "BGPPeer": {peer("x", `"interface": "tenant"`)}}),
			"interface tenant is the device of VRF tenant"},
		"interfaces of a VRF's device": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, "")}, "BGPPeer": {peer("x", `"interfaces": ["ten*"]`)}}),
			"interfaces: ten* matches tenant, the device of VRF tenant"},
		"an interface of another VRF": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, "")}, "BGPPeer": {peer("x", `"interface": "eth2"`)}}),
			"interface eth2 is in the VRF tenant, not in the peer's VRF default"},
		"interfaces of every link": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, "")}, "BGPPeer": {peer("x", `"interfaces": ["*"]`)}}),
			"interfaces: * matches tenant, the device of VRF tenant"},
		"interfaces of another VRF's links alone": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, "")}, "BGPPeer": {peer("x", `"interfaces": ["eth1", "eth2*"]`)}}),
			"BGPPeer x: interfaces: eth2* matches no link of VRF default, only links of other VRFs"},
		"an import of its own VRF": {map[string][]any{"BGP": {bgp("bgp", 64512, `, "import": {"ipv6": {"vrfs": ["default"]}}`)}},
			"BGP bgp: import.ipv6 names its own VRF default"},
		"an import of no VRF": {map[string][]any{"BGP": {bgp("bgp", 64512, `, "import": {"ipv6": {"vrfs": []}}`)}},
			"BGP bgp: import.ipv6 names no VRF"},
		"an import of a VRF without BGP": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, `, "import": {"ipv6": {"vrfs": ["tenant"]}}`)}}),
			`BGP bgp: import.ipv6 names the VRF "tenant", which no BGP document serves`},
		"an import that adds a command": {map[string][]any{"BGP": {bgp("bgp", 64512, `, "import": {"ipv6": {"vrfs": ["x\n neighbor 192.0.2.9 remote-as 64999"]}}`)}},
			`names the VRF "x\n neighbor 192.0.2.9 remote-as 64999", which no BGP document serves`},
		"an import of one VRF twice": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, `, "import": {"ipv6": {"vrfs": ["tenant", "tenant"]}}`), bgp("tenant", 64512, `, "vrf": "tenant"`)}}),
			"BGP bgp: import.ipv6 names the VRF tenant twice"},
		"an import's policy of the other family": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, `, "import": {"ipv4": {"vrfs": ["tenant"], "policy": "from"}}`), bgp("tenant", 64512, `, "vrf": "tenant"`)},
			"PrefixList": {v6}, "RoutingPolicy": {from}}),
			"BGP bgp: import.ipv4: the RoutingPolicy from matches PrefixLists of ipv6, not ipv4"},
		"a prepend for two AS numbers": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, `, "networks": [{"prefix": "2001:db8:1::/48", "policy": "p"}]`),
			bgp("tenant", 64513, `, "vrf": "tenant", "networks": [{"prefix": "2001:db8:2::/48", "policy": "p"}]`)}, "RoutingPolicy": {prepend}}),
			"RoutingPolicy p prepends the AS number, and BGP instances of AS 64512 and 64513 use it; give each AS a policy of its own"},
		"a default-route network in a VRF": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, ""), bgp("tenant", 64512, `, "vrf": "tenant", "networks": [{"prefix": "::/0"}]`)}}),
			"BGP tenant: network ::/0 of VRF tenant would always be announced, as the VRF's table ends in an unreachable default route; use a peer family's defaultOriginate"},
		"an IPv4 default-route network in a VRF": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, ""), bgp("tenant", 64512, `, "vrf": "tenant", "networks": [{"prefix": "0.0.0.0/0"}]`)}}),
			"BGP tenant: network 0.0.0.0/0 of VRF tenant would always be announced"},
		"a default-route network with host bits in a VRF": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, ""), bgp("tenant", 64512, `, "vrf": "tenant", "networks": [{"prefix": "10.0.0.0/0"}]`)}}),
			"BGP tenant: network 10.0.0.0/0 of VRF tenant would always be announced"},
		"an import of a VRF with a conntrack zone": {map[string][]any{
			"VRF": {routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"], "conntrackZone": 1}`)}, "Network": {eth2},
			"BGP": {bgp("bgp", 64512, `, "import": {"ipv6": {"vrfs": ["tenant"]}}`), bgp("tenant", 64512, `, "vrf": "tenant"`)}},
			"BGP bgp: import.ipv6 names the VRF tenant, and VRF tenant has a conntrack zone, so the replies of leaked connections would land in another zone; drop the zone or the import"},
		"an import into a VRF with a conntrack zone": {map[string][]any{
			"VRF": {routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"], "conntrackZone": 1}`)}, "Network": {eth2},
			"BGP": {bgp("bgp", 64512, ""), bgp("tenant", 64512, `, "vrf": "tenant", "import": {"ipv6": {"vrfs": ["default"]}}`)}},
			"BGP tenant: import.ipv6 names the VRF default, and VRF tenant has a conntrack zone, so the replies of leaked connections would land in another zone; drop the zone or the import"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"frr"}, c.docs, c.want) })
	}
}

// The default VRF's table holds no unreachable default route, so its instance announces a
// default-route network only while the table holds a default route.
func TestFRRDefaultRouteNetwork(t *testing.T) {
	timers := `"multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false}, "bfd": {"receiveMs": 300, "transmitMs": 300, "multiplier": 3}`
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": map[string][]any{
		"BGP": {routerDoc(t, "BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", `+timers+`, "networks": [{"prefix": "0.0.0.0/0"}, {"prefix": "::/0"}]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	conf := renderedFile(t, []string{"frr"}, string(input), frrFile)
	for _, want := range []string{"  network 0.0.0.0/0\n", "  network ::/0\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("frr.conf lacks %q:\n%s", want, conf)
		}
	}
}

// A peer's interface is in the VRF of the first Network document networkd picks for it, which
// may match it by a glob, and the peer's session runs there.
func TestFRRVRFInterfaceByGlob(t *testing.T) {
	timers := `"multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false}, "bfd": {"receiveMs": 300, "transmitMs": 300, "multiplier": 3}`
	docs := map[string][]any{
		"BGP": {
			routerDoc(t, "BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", `+timers+`}`),
			routerDoc(t, "BGP", "tenant", `{"vrf": "tenant", "asn": 64512, "routerID": "192.0.2.1", `+timers+`}`),
		},
		"VRF":     {routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["swp*"]}`)},
		"Network": {document(t, "Network", "30-swp", `{"match": {"name": "swp*"}}`)},
	}
	peer := func(more string) []any {
		return []any{routerDoc(t, "BGPPeer", "leaf", `{"interface": "swp1", "remoteAS": "external", "families": {"ipv6": {}}`+more+`}`)}
	}
	docs["BGPPeer"] = peer(`, "vrf": "tenant"`)
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	conf := renderedFile(t, []string{"frr"}, string(input), frrFile)
	if !strings.Contains(conf, "router bgp 64512 vrf tenant\n bgp router-id 192.0.2.1\n no bgp default ipv4-unicast\n neighbor peer-leaf peer-group\n neighbor peer-leaf remote-as external\n neighbor swp1 interface peer-group peer-leaf\n") {
		t.Errorf("frr.conf runs no session on swp1 in tenant:\n%s", conf)
	}
	docs["BGPPeer"] = peer("")
	refused(t, []string{"frr"}, docs, "interface swp1 is in the VRF tenant, not in the peer's VRF default")
	// A glob of a default-VRF peer expands to the links of no VRF alone.
	docs["Network"] = append(docs["Network"],
		document(t, "Network", "20-swp1", `{"match": {"name": "swp1"}}`),
		document(t, "Network", "40-sw0", `{"match": {"name": "sw0"}}`))
	docs["BGPPeer"] = []any{routerDoc(t, "BGPPeer", "leaves", `{"interfaces": ["sw*"], "remoteAS": "external", "families": {"ipv6": {}}}`)}
	input, err = json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	conf = renderedFile(t, []string{"frr"}, string(input), frrFile)
	if !strings.Contains(conf, " neighbor sw0 interface peer-group peer-leaves\n") || strings.Contains(conf, "neighbor swp1 ") {
		t.Errorf("frr.conf runs sw*'s sessions elsewhere than on sw0:\n%s", conf)
	}
	// A Network whose other [Match] keys may not match swp1 leaves its VRF open.
	docs["Network"] = []any{document(t, "Network", "30-swp", `{"match": {"name": "swp*", "macAddress": "02:00:00:00:00:01"}}`)}
	docs["BGPPeer"] = peer(`, "vrf": "tenant"`)
	refused(t, []string{"frr"}, docs, "networkd puts swp1 in VRF tenant only if the other [Match] keys of Network 30-swp match it")
}

// A prepend repeats the AS of the instance that uses its policy, the default VRF's or another's,
// and each instance with a BFD peer has a BFD profile of its own timers.
func TestFRRVRFPrependAndBFD(t *testing.T) {
	timers := func(rx int) string {
		return fmt.Sprintf(`"multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false}, "bfd": {"receiveMs": %d, "transmitMs": 300, "multiplier": 3}`, rx)
	}
	docs := map[string][]any{
		"BGP": {
			routerDoc(t, "BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", `+timers(300)+`}`),
			routerDoc(t, "BGP", "tenant", `{"vrf": "tenant", "asn": 64513, "routerID": "192.0.2.1", "networks": [{"prefix": "2001:db8:2::/48", "policy": "p"}], `+timers(500)+`}`),
		},
		"VRF":           {routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"]}`)},
		"Network":       {document(t, "Network", "20-eth2", `{"match": {"name": "eth2"}}`)},
		"RoutingPolicy": {routerDoc(t, "RoutingPolicy", "p", `{"terms": [{"then": {"action": "accept", "prepend": 1}}], "default": "reject"}`)},
		"BGPPeer": {
			routerDoc(t, "BGPPeer", "spine", `{"address": "2001:db8::9", "bfd": true, "remoteAS": "external", "families": {"ipv6": {}}}`),
			routerDoc(t, "BGPPeer", "leaf", `{"vrf": "tenant", "interface": "eth2", "bfd": true, "remoteAS": "external", "families": {"ipv6": {}}}`),
		},
	}
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	conf := renderedFile(t, []string{"frr"}, string(input), frrFile)
	for _, want := range []string{
		" set as-path prepend 64513\n",
		" neighbor peer-spine bfd profile chalkos\n",
		" neighbor peer-leaf bfd profile chalkos-tenant\n",
		" profile chalkos\n  receive-interval 300\n",
		" profile chalkos-tenant\n  receive-interval 500\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("frr.conf lacks %q:\n%s", want, conf)
		}
	}
}

// vrfZoneDocs are an edge's documents with three VRFs: internet and tenant, whose zones hold them
// whole, and guest, whose links are in two zones of their own, and a LAN of links of no VRF.
func vrfZoneDocs(t *testing.T) map[string][]any {
	t.Helper()
	zone := func(name, spec string) any { return routerDoc(t, "FirewallZone", name, spec) }
	policy := func(name, from, to, verdict string) any {
		return routerDoc(t, "FirewallPolicy", name, `{"from": "`+from+`", "to": "`+to+`", "default": "`+verdict+`", "rules": []}`)
	}
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
		"FirewallPolicy": {
			policy("guest-lan-local", "guest-lan", "local", "accept"),
			policy("guest-out", "guest-lan", "guest-wan", "accept"),
			policy("internet-local", "internet", "local", "drop"),
			policy("lan-local", "lan", "local", "accept"),
			policy("tenant-internet", "tenant", "internet", "accept"),
		},
		"NATRule": {
			routerDoc(t, "NATRule", "guest-web", `{"type": "dnat", "family": "ipv4", "in": {"zone": "guest-wan", "interface": null}, "out": null, "match": {"source": [], "destination": [], "protocol": "tcp", "destinationPorts": [8080]}, "to": {"address": "192.0.2.10", "port": 80}}`),
			routerDoc(t, "NATRule", "internet-out", `{"type": "masquerade", "family": "ipv4", "out": {"zone": "internet", "interface": null}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`),
			routerDoc(t, "NATRule", "tenant-web", `{"type": "dnat", "family": "ipv4", "in": {"zone": "tenant", "interface": null}, "out": null, "match": {"source": [], "destination": [], "protocol": "tcp", "destinationPorts": [80]}, "to": {"address": "198.51.100.10", "port": null}}`),
		},
	}
}

// vrfRuleset renders router-nftables from the documents.
func vrfRuleset(t *testing.T, docs map[string][]any) string {
	t.Helper()
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	return renderedFile(t, []string{"router-nftables", "50000"}, string(input), rulesetFile)
}

// The input and forward hooks see a packet from a VRF's link with the VRF's device as iifname and
// the link as sdifname: a zone of a VRF comes in by the device, a zone of a VRF's link by
// sdifname, a zone of links of no VRF by iifname. A zone's traffic goes out, and NAT translates
// it, by the links themselves, those of its VRFs included.
func TestVRFZones(t *testing.T) {
	text := vrfRuleset(t, vrfZoneDocs(t))
	for _, want := range []string{
		"\t\tmeta sdifname { \"eth3\" } jump policy-guest-lan-local\n" +
			"\t\tiifname { \"internet\" } jump policy-internet-local\n" +
			"\t\tiifname { \"eth5\", \"vlan*\" } jump policy-lan-local\n\t}\n",
		"\t\tmeta sdifname { \"eth3\" } oifname { \"eth4\" } jump policy-guest-out\n" +
			"\t\tiifname { \"tenant\" } oifname { \"eth1\" } jump policy-tenant-internet\n\t}\n",
		"\t\tmeta nfproto ipv4 iifname { \"eth4\" } tcp dport { 8080 } dnat ip to 192.0.2.10:80\n" +
			"\t\tmeta nfproto ipv4 iifname { \"eth2\", \"swp*\" } tcp dport { 80 } dnat ip to 198.51.100.10\n",
		"\t\tmeta nfproto ipv4 oifname { \"eth1\" } masquerade\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the ruleset lacks\n%s\nin\n%s", want, text)
		}
	}
}

// A zone of links of no VRF forwards to a zone of a VRF by the VRF's links. A zone of interfaces
// and VRFs comes in by both and goes out, and translates, by the interfaces and the VRFs' links;
// a link of the zone's own VRF needs no sdifname, as the VRF's device covers it.
func TestVRFZonesMixed(t *testing.T) {
	docs := vrfZoneDocs(t)
	docs["FirewallZone"][2] = routerDoc(t, "FirewallZone", "internet", `{"interfaces": ["eth5", "eth1"], "vrfs": ["internet"]}`)
	docs["FirewallZone"][3] = routerDoc(t, "FirewallZone", "lan", `{"interfaces": ["vlan*"], "vrfs": []}`)
	docs["FirewallPolicy"] = append(docs["FirewallPolicy"],
		routerDoc(t, "FirewallPolicy", "lan-internet", `{"from": "lan", "to": "internet", "default": "accept", "rules": []}`))
	docs["NATRule"] = []any{
		routerDoc(t, "NATRule", "internet-out", `{"type": "snat", "family": "ipv4", "out": {"zone": "internet", "interface": null}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": {"address": "203.0.113.1", "port": null}}`),
	}
	text := vrfRuleset(t, docs)
	for _, want := range []string{
		"		iifname { \"eth5\", \"internet\" } jump policy-internet-local\n",
		"		iifname { \"vlan*\" } oifname { \"eth5\", \"eth1\" } jump policy-lan-internet\n",
		"		iifname { \"tenant\" } oifname { \"eth5\", \"eth1\" } jump policy-tenant-internet\n",
		"		meta nfproto ipv4 oifname { \"eth5\", \"eth1\" } snat ip to 203.0.113.1\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the ruleset lacks\n%s\nin\n%s", want, text)
		}
	}
	if strings.Contains(text, "sdifname { \"eth1\" }") {
		t.Errorf("the zone of VRF internet matches its link eth1 by sdifname:\n%s", text)
	}
}

// A glob that may match links of VRFs and links of no VRF comes in by iifname and by sdifname.
func TestVRFZoneOfAGlob(t *testing.T) {
	docs := vrfZoneDocs(t)
	docs["FirewallZone"] = []any{routerDoc(t, "FirewallZone", "all", `{"interfaces": ["eth*"], "vrfs": []}`)}
	docs["FirewallPolicy"] = []any{routerDoc(t, "FirewallPolicy", "all-local", `{"from": "all", "to": "local", "default": "accept", "rules": []}`)}
	docs["NATRule"] = nil
	want := "\t\tiifname { \"eth*\" } jump policy-all-local\n\t\tmeta sdifname { \"eth*\" } jump policy-all-local\n\t}\n"
	if text := vrfRuleset(t, docs); !strings.Contains(text, want) {
		t.Errorf("the ruleset lacks\n%s\nin\n%s", want, text)
	}
}

// NAT translates by the links, and an interface of a NAT rule that matches the links the Network
// documents name as well as a VRF's device, such as *, translates on those links.
func TestVRFNATOfEveryInterface(t *testing.T) {
	docs := vrfZoneDocs(t)
	docs["Network"] = append(docs["Network"], document(t, "Network", "50-transit", `{"match": {"name": "transit0"}}`))
	for _, i := range []string{"*", "t*"} {
		docs["NATRule"] = []any{routerDoc(t, "NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": {"zone": null, "interface": "`+i+`"}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)}
		want := "\t\tmeta nfproto ipv4 oifname { \"" + i + "\" } masquerade\n"
		if text := vrfRuleset(t, docs); !strings.Contains(text, want) {
			t.Errorf("the ruleset lacks\n%s\nin\n%s", want, text)
		}
	}
}

// The firewall refuses zones the rules refuse, should they bypass them, and zones whose links it
// cannot tell apart from a VRF's: an interface that matches a VRF's device, one that may be a
// link of a VRF another zone holds, and a VRF whose links nftables cannot match. A NAT rule's
// interface that matches nothing but VRF devices translates nothing.
func TestVRFZoneRefusals(t *testing.T) {
	zone := func(name, spec string) any { return routerDoc(t, "FirewallZone", name, spec) }
	tenant := zone("tenant", `{"interfaces": [], "vrfs": ["tenant"]}`)
	with := func(zones ...any) map[string][]any {
		docs := vrfZoneDocs(t)
		docs["FirewallZone"], docs["FirewallPolicy"], docs["NATRule"] = zones, nil, nil
		return docs
	}
	unmatchable := with(tenant)
	unmatchable["Network"] = append(unmatchable["Network"], document(t, "Network", "50-lab", `{"match": {"name": "lab[12]"}, "network": {"vrf": "tenant"}}`))
	empty := with(zone("x", `{"interfaces": [], "vrfs": ["empty"]}`))
	empty["VRF"] = append(empty["VRF"], routerDoc(t, "VRF", "empty", `{"table": 400, "interfaces": []}`))
	empty["NATRule"] = []any{routerDoc(t, "NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": {"zone": "x", "interface": null}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)}
	natOut := func(i string) map[string][]any {
		docs := with(tenant)
		docs["NATRule"] = []any{routerDoc(t, "NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": {"zone": null, "interface": "`+i+`"}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)}
		return docs
	}
	narrowed := with(zone("x", `{"interfaces": ["swp3"], "vrfs": []}`))
	narrowed["Network"] = append(narrowed["Network"], document(t, "Network", "25-swp", `{"match": {"name": "swp3", "macAddress": "02:00:00:00:00:01"}}`))
	management := func(i string) map[string][]any {
		docs := with(tenant)
		docs["Router"] = []any{routerDoc(t, "Router", "router", `{"forwarding": {"ipv4": true, "ipv6": true}, "rpFilter": "loose", "ecmpHash": "l4", "conntrackMax": null, "management": {"interfaces": ["eth0", "`+i+`"]}}`)}
		return docs
	}
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a zone of nothing":                  {with(zone("x", `{"interfaces": [], "vrfs": []}`)), "FirewallZone x: a FirewallZone names an interface or a VRF; give it interfaces, vrfs or both"},
		"a VRF no document declares":         {with(zone("x", `{"interfaces": [], "vrfs": ["nosuch"]}`)), `FirewallZone x names the VRF "nosuch", which no VRF document declares`},
		"a VRF in two zones":                 {with(tenant, zone("x", `{"interfaces": [], "vrfs": ["tenant"]}`)), "the VRF tenant is in more than one FirewallZone, or twice in one"},
		"a VRF's device":                     {with(zone("x", `{"interfaces": ["ten*"], "vrfs": []}`)), "FirewallZone x: ten* matches tenant, the device of VRF tenant; name the VRF in vrfs"},
		"a link of another zone's VRF":       {with(tenant, zone("x", `{"interfaces": ["eth2"], "vrfs": []}`)), "FirewallZone x names eth2, which may be a link of VRF tenant, which FirewallZone tenant holds; give each link one zone"},
		"a glob of another zone's VRF":       {with(tenant, zone("x", `{"interfaces": ["swp1*"], "vrfs": []}`)), "FirewallZone x names swp1*, which may be a link of VRF tenant, which FirewallZone tenant holds; give each link one zone"},
		"links nftables cannot match":        {unmatchable, "FirewallZone tenant: VRF tenant holds the links of Network 50-lab's Name=lab[12], which nftables cannot match; name them by a name or a prefix and *"},
		"a NAT zone of no link":              {empty, "NATRule n: the zone x holds no link"},
		"a NAT interface of a VRF's device":  {natOut("internet"), "NATRule n: internet matches no link, only internet, the device of VRF internet; name the VRF's links or a zone of the VRF"},
		"a NAT glob of a VRF's device alone": {natOut("inter*"), "NATRule n: inter* matches no link, only internet, the device of VRF internet; name the VRF's links or a zone of the VRF"},
		"a link of a narrowed Network":       {narrowed, "FirewallZone x: networkd puts swp3 in VRF tenant only if the other [Match] keys of Network 25-swp match it"},
		"a management VRF device":            {management("tenant"), "Router: the management interface tenant matches tenant, the device of VRF tenant, and chalkd answers in the default VRF alone; name the default VRF's links"},
		"a management glob of a VRF device":  {management("gu*"), "Router: the management interface gu* matches guest, the device of VRF guest, and chalkd answers in the default VRF alone; name the default VRF's links"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"router-nftables", "50000"}, c.docs, c.want) })
	}
}

// A VRF with a conntrack zone puts the connections through its links in its zone before conntrack
// sees them: in prerouting by the link a packet comes in on, whose pass decides its zone, and in
// output by the link the router sends it through. Without zones the ruleset has neither chain.
func TestConntrackZones(t *testing.T) {
	docs := vrfZoneDocs(t)
	docs["VRF"][0] = routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"], "conntrackZone": 1}`)
	want := "\tchain ct-prerouting {\n" +
		"\t\ttype filter hook prerouting priority raw; policy accept;\n" +
		"\t\tiifname { \"eth3\", \"eth4\" } ct zone set 1 comment \"VRF guest\"\n" +
		"\t}\n" +
		"\tchain ct-output {\n" +
		"\t\ttype filter hook output priority raw; policy accept;\n" +
		"\t\toifname { \"eth3\", \"eth4\" } ct zone set 1 comment \"VRF guest\"\n" +
		"\t}\n" +
		"\tchain prerouting {\n"
	if text := vrfRuleset(t, docs); !strings.Contains(text, want) {
		t.Errorf("the ruleset lacks\n%s\nin\n%s", want, text)
	}
	if text := vrfRuleset(t, vrfZoneDocs(t)); strings.Contains(text, "ct zone") {
		t.Errorf("a ruleset without conntrack zones sets one:\n%s", text)
	}
}

// Each VRF with a conntrack zone and links has one rule per chain, in the order of the VRFs, of
// the links its document and the Networks' own VRF= name; a VRF with a zone but no links has none,
// and a ruleset whose zoned VRFs have no links has no conntrack chains.
func TestConntrackZonesOfSeveralVRFs(t *testing.T) {
	docs := vrfZoneDocs(t)
	docs["VRF"][0] = routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"], "conntrackZone": 1}`)
	docs["VRF"] = append(docs["VRF"],
		routerDoc(t, "VRF", "lab", `{"table": 400, "interfaces": [], "conntrackZone": 3}`),
		routerDoc(t, "VRF", "spare", `{"table": 500, "interfaces": [], "conntrackZone": 2}`))
	docs["Network"] = append(docs["Network"], document(t, "Network", "50-lab", `{"match": {"name": "dummy*"}, "network": {"vrf": "lab"}}`))
	want := "\t\ttype filter hook prerouting priority raw; policy accept;\n" +
		"\t\tiifname { \"eth3\", \"eth4\" } ct zone set 1 comment \"VRF guest\"\n" +
		"\t\tiifname { \"dummy*\" } ct zone set 3 comment \"VRF lab\"\n" +
		"\t}\n"
	text := vrfRuleset(t, docs)
	if !strings.Contains(text, want) {
		t.Errorf("the ruleset lacks\n%s\nin\n%s", want, text)
	}
	if strings.Contains(text, "ct zone set 2") {
		t.Errorf("a zoned VRF of no link sets its zone:\n%s", text)
	}
	docs = vrfZoneDocs(t)
	docs["VRF"] = append(docs["VRF"], routerDoc(t, "VRF", "spare", `{"table": 500, "interfaces": [], "conntrackZone": 2}`))
	if text := vrfRuleset(t, docs); strings.Contains(text, "ct-prerouting") || strings.Contains(text, "ct-output") {
		t.Errorf("a ruleset whose zoned VRF has no link has conntrack chains:\n%s", text)
	}
}

// The firewall refuses conntrack zones the schema and rules refuse, should they bypass them.
func TestConntrackZoneRefusals(t *testing.T) {
	with := func(guest, internet string) map[string][]any {
		docs := vrfZoneDocs(t)
		docs["VRF"][0] = routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"]`+guest+`}`)
		docs["VRF"][1] = routerDoc(t, "VRF", "internet", `{"table": 100, "interfaces": ["eth1"]`+internet+`}`)
		return docs
	}
	unmatchable := with(`, "conntrackZone": 1`, "")
	unmatchable["Network"] = append(unmatchable["Network"], document(t, "Network", "50-lab", `{"match": {"name": "lab[12]"}, "network": {"vrf": "guest"}}`))
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"zone 0":                      {with(`, "conntrackZone": 0`, ""), "VRF guest: conntrackZone 0 is no zone from 1 to 65535"},
		"zone 65536":                  {with(`, "conntrackZone": 65536`, ""), "VRF guest: conntrackZone 65536 is no zone from 1 to 65535"},
		"one zone twice":              {with(`, "conntrackZone": 1`, `, "conntrackZone": 1`), "VRF guest and VRF internet both use conntrack zone 1"},
		"links nftables cannot match": {unmatchable, "VRF guest holds the links of Network 50-lab's Name=lab[12], which nftables cannot match"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"router-nftables", "50000"}, c.docs, c.want) })
	}
}
