package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
	"github.com/trevex/chalkos/pkg/schema"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	// routerSchemas are the router kinds' schemas, as a router image generates them.
	routerSchemas = "../../test/fixtures/kinds/router"
	// routerExamples are the router's example configs.
	routerExamples = "../../modules/node/examples/router"
	// testTrustAnchor and testZoneFile stand for the store paths the image gives chalkos-render
	// unbound.
	testTrustAnchor = "/etc/unbound/root.key"
	testZoneFile    = "/etc/unbound/ipv4only.arpa.zone"
	// testPDHook stands for the store path of the program that records delegated prefixes, which
	// the image gives chalkos-render kea.
	testPDHook = "/etc/kea/chalkos-dhcp6-pd"
)

// routerKinds are the kinds of router.chalkos.dev.
var routerKinds = []string{"Router", "FirewallZone", "FirewallPolicy", "AddressSet", "NATRule", "BGP", "BGPPeer", "PrefixList", "CommunityList", "RoutingPolicy", "NAT64", "Resolver", "VRF", "PolicyRoute", "NextHop", "DHCPSubnet", "DHCPRelay"}

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
		if d.APIVersion != derive.API {
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

// chalkos-render validate router reads the input a renderer reads, needs no $OUT, and prints the
// report of the validator router, which refuses none of the examples. Input it cannot read fails
// the validator, which then prints no report.
func TestValidateRouter(t *testing.T) {
	examples, err := filepath.Glob(filepath.Join(routerExamples, "*.yaml"))
	if err != nil || len(examples) == 0 {
		t.Fatalf("examples = %v, %v", examples, err)
	}
	for _, path := range examples {
		var out strings.Builder
		if err := run([]string{"validate", "router"}, strings.NewReader(routerInput(t, path)), &out, ""); err != nil || out.String() != "{\"refusals\":[]}\n" {
			t.Errorf("%s: validate router = %v, printed %q", filepath.Base(path), err, out.String())
		}
	}
	var out strings.Builder
	err = run([]string{"validate", "router"}, strings.NewReader(`[]`), &out, "")
	if err == nil || !strings.Contains(err.Error(), "validator router: the input is no object of documents by kind") || out.Len() != 0 {
		t.Errorf("validate router of an array = %v, printed %q", err, out.String())
	}
	// A config it refuses is a report of its refusals, and the validator succeeds.
	out.Reset()
	refused := `{"node": {"name": "r1"}, "documents": {"Network": [{"kind": "Network", "metadata": {"name": "20-tenant"}, "spec": {"match": {"name": "tenant"}}}],
		"VRF": [{"kind": "VRF", "metadata": {"name": "tenant"}, "spec": {"table": 100}}]}}`
	want := `{"refusals":[{"kind":"VRF","name":"tenant","path":"metadata.name","message":"a Network document names the link tenant, the VRF's device"}]}` + "\n"
	if err := run([]string{"validate", "router"}, strings.NewReader(refused), &out, ""); err != nil || out.String() != want {
		t.Errorf("validate router of a refused config = %v, printed %q, want %q", err, out.String(), want)
	}
	// A message's control and bidirectional runes print as spaces, as the report's format takes
	// them, whatever validator returns them.
	validators["test"] = func([]byte) ([]refusals.Refusal, error) {
		return []refusals.Refusal{{Kind: "VRF", Name: "a", Path: "spec", Message: "two\nlines\u202e"}}, nil
	}
	t.Cleanup(func() { delete(validators, "test") })
	out.Reset()
	if err := run([]string{"validate", "test"}, strings.NewReader(`{"documents": {}}`), &out, ""); err != nil || out.String() != `{"refusals":[{"kind":"VRF","name":"a","path":"spec","message":"two lines "}]}`+"\n" {
		t.Errorf("validate test = %v, printed %q", err, out.String())
	}
}

// The fallback ruleset is that of a node without documents, but its forward chain drops what the
// protected rules do not accept.
func TestRouterFallbackGolden(t *testing.T) {
	golden(t, "fallback.nft", renderedFile(t, []string{"router-nftables-fallback", "50000"}, `{"node": {"name": ""}, "documents": {}}`, rulesetFile))
}

// An opening of a service renders after chalkd's port: a line per family of its sources and
// destinations, or one without either, scoped to its interfaces; a family its sources or its
// destinations lack has no line.
func TestRouterOpenings(t *testing.T) {
	d, err := derive.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	fw, err := newFirewall(d, 50000, false)
	if err != nil {
		t.Fatal(err)
	}
	fw.openings = []opening{
		{comment: "BGPPeer fabric", protocol: "tcp", ports: []derive.PortRange{"179"}, interfaces: []string{"eth1", "eth2"}},
		{comment: "BGPPeer far", protocol: "tcp", ports: []derive.PortRange{"179"}, sources: ranges("192.0.2.9/32", "2001:db8::9/128")},
		{comment: "Resolver resolver", protocol: "udp", ports: []derive.PortRange{"53"}, sources: ranges("192.0.2.0/24", "2001:db8::/32"), destinations: ranges("2001:db8:ffff::1/128")},
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
// address or listen range, and BFD's multi-hop port from its address or listen range alone.
func TestBGPOpenings(t *testing.T) {
	addr, link := netip.MustParseAddr("192.0.2.9"), "eth1"
	got, err := bgpOpenings([]derive.Named[derive.PeerSpec]{
		{Name: "far", Spec: derive.PeerSpec{Address: &addr, BFD: true}},
		{Name: "near", Spec: derive.PeerSpec{Interface: &link, BFD: true}},
		{Name: "plain", Spec: derive.PeerSpec{Interfaces: []string{"swp*"}}},
		{Name: "hosts", Spec: derive.PeerSpec{Listen: &derive.PeerListen{Range: netip.MustParsePrefix("2001:db8:1::/64")}, BFD: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []opening{
		{comment: "BGPPeer far", protocol: "tcp", ports: []derive.PortRange{"179"}, sources: ranges("192.0.2.9/32")},
		{comment: "BGPPeer far", protocol: "udp", ports: []derive.PortRange{"3784", "3785", "4784"}, sources: ranges("192.0.2.9/32")},
		{comment: "BGPPeer near", protocol: "tcp", ports: []derive.PortRange{"179"}, interfaces: []string{"eth1"}},
		{comment: "BGPPeer near", protocol: "udp", ports: []derive.PortRange{"3784", "3785"}, interfaces: []string{"eth1"}},
		{comment: "BGPPeer plain", protocol: "tcp", ports: []derive.PortRange{"179"}, interfaces: []string{"swp*"}},
		{comment: "BGPPeer hosts", protocol: "tcp", ports: []derive.PortRange{"179"}, sources: ranges("2001:db8:1::/64")},
		{comment: "BGPPeer hosts", protocol: "udp", ports: []derive.PortRange{"3784", "3785", "4784"}, sources: ranges("2001:db8:1::/64")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bgpOpenings =\n%+v\nwant\n%+v", got, want)
	}
}

// A WireGuard opens its UDP port from anywhere, which renders as one line of the input chain.
func TestWireGuardOpenings(t *testing.T) {
	got := wireguardOpenings([]derive.Named[derive.WireGuardSpec]{
		{Name: "wg0", Spec: derive.WireGuardSpec{ListenPort: 51820}},
		{Name: "wg1", Spec: derive.WireGuardSpec{ListenPort: 51821, VRF: ptr("tenant")}},
	})
	want := []opening{
		{comment: "WireGuard wg0", protocol: "udp", ports: []derive.PortRange{"51820"}},
		{comment: "WireGuard wg1", protocol: "udp", ports: []derive.PortRange{"51821"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wireguardOpenings =\n%+v\nwant\n%+v", got, want)
	}
	if lines := got[0].lines(); !reflect.DeepEqual(lines, []string{`udp dport { 51820 } accept comment "WireGuard wg0"`}) {
		t.Errorf("lines = %q", lines)
	}
}

// A Resolver opens DNS over UDP and TCP from its clients' ranges to the addresses it listens on.
// It guards, with an internal error, values that would add to the ruleset and an empty listen or
// allow, which would open the port wider.
func TestResolverOpenings(t *testing.T) {
	got, err := resolverOpenings([]derive.Named[derive.ResolverSpec]{
		{Name: "resolver", Spec: derive.ResolverSpec{Listen: addrs("2001:db8:ffff::1", "192.0.2.1"), Allow: ranges("2001:db8::1/32", "192.0.2.0/24")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dns := opening{comment: "Resolver resolver", ports: []derive.PortRange{"53"}, sources: ranges("2001:db8::/32", "192.0.2.0/24"), destinations: ranges("2001:db8:ffff::1/128", "192.0.2.1/32")}
	udp, tcp := dns, dns
	udp.protocol, tcp.protocol = "udp", "tcp"
	if want := []opening{udp, tcp}; !reflect.DeepEqual(got, want) {
		t.Errorf("resolverOpenings =\n%+v\nwant\n%+v", got, want)
	}
	// A VRF's resolver opens its port on the VRF's device; resolverOpenings checks the VRF's name,
	// and the output unbound that a VRF document declares it.
	got, err = resolverOpenings([]derive.Named[derive.ResolverSpec]{{Name: "guest", Spec: derive.ResolverSpec{VRF: ptr("guest"), Listen: addrs("2001:db8:3::1"), Allow: ranges("2001:db8:3::/64")}}})
	if err != nil || len(got) != 2 || got[0].vrf != "guest" || got[1].vrf != "guest" {
		t.Errorf("resolverOpenings of a VRF's resolver = %+v, %v", got, err)
	}
	if _, err := resolverOpenings([]derive.Named[derive.ResolverSpec]{{Name: "bad", Spec: derive.ResolverSpec{VRF: ptr("a b"), Listen: addrs("2001:db8:3::1"), Allow: ranges("2001:db8:3::/64")}}}); err == nil || !strings.Contains(err.Error(), `internal error: Resolver bad: the VRF "a b" is no VRF's name`) {
		t.Errorf("err = %v", err)
	}
	for spec, want := range map[string]string{
		`{"listen": ["fe80::1%eth0\" } accept"], "allow": ["2001:db8::/32"]}`:                         `internal error: Resolver resolver: spec.listen[0]: "fe80::1%eth0\" } accept" has a zone`,
		`{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32\" } accept\n\t\tip6 saddr ::/0"]}`: `internal error: Resolver resolver: netip.ParsePrefix("2001:db8::/32\" } accept\n\t\tip6 saddr ::/0"): ParseAddr("2001:db8::/32\" } accept\n\t\tip6 saddr ::"): each colon-separated field must have at least one digit (at "/32\" } accept\n\t\tip6 saddr ::")`,
		`{"listen": [], "allow": ["2001:db8::/32"]}`:                                                  "internal error: Resolver resolver: listen names no address, so the firewall would accept DNS on every address of the router",
		`{"listen": ["2001:db8:ffff::1"], "allow": []}`:                                               "internal error: Resolver resolver: allow names no range, so the firewall would accept DNS from every client",
	} {
		refused(t, []string{"router-nftables", "50000"}, map[string][]any{"Resolver": {derivetest.RouterDoc(t, "Resolver", "resolver", spec)}}, want)
	}
}

// The forward chain accepts the NAT64 links' translated traffic ahead of the policies, from a
// VRF's link by sdifname: out of a link the prefix's IPv6 and IPv4 from the pool or the inner
// address, into it IPv6 to the prefix and IPv4 to an explicit pool; destination NAT translates
// nothing out of a NAT64's link or to its prefix or explicit pool; source NAT translates nothing
// into a NAT64's link or from an explicit pool and masquerades an own pool's inner address, ahead
// of the NATRules; a VRF's conntrack zone holds its NAT64's link; and a VRF's resolver opens port
// 53 in its VRF alone. Without policies between zones the forward chain accepts everything, and
// without NATRules an explicit pool needs no source NAT chain.
func TestNAT64Firewall(t *testing.T) {
	docs := map[string][]any{
		"VRF":            {derivetest.RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"], "conntrackZone": 1}`)},
		"Network":        {derivetest.Doc(t, "Network", "10-eth0", `{"match": {"name": "eth0"}}`), derivetest.Doc(t, "Network", "40-guest", `{"match": {"name": "eth3 eth4"}}`)},
		"FirewallZone":   {derivetest.RouterDoc(t, "FirewallZone", "lan", `{"interfaces": ["eth0"]}`), derivetest.RouterDoc(t, "FirewallZone", "wan", `{"interfaces": ["eth4"]}`)},
		"FirewallPolicy": {derivetest.RouterDoc(t, "FirewallPolicy", "lan-wan", `{"from": "lan", "to": "wan", "default": "accept", "rules": []}`)},
		"NATRule": {derivetest.RouterDoc(t, "NATRule", "out", `{"type": "masquerade", "family": "ipv4", "out": {"zone": "wan"}}`),
			derivetest.RouterDoc(t, "NATRule", "web", `{"type": "dnat", "family": "ipv4", "in": {"interface": "*"}, "match": {"protocol": "tcp", "destinationPorts": [80]}, "to": {"address": "192.0.2.80"}}`)},
		"NAT64": {
			derivetest.RouterDoc(t, "NAT64", "edge", `{"prefix": "64:ff9b::/96", "pool4": [{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp", "udp", "icmp"]}]}`),
			derivetest.RouterDoc(t, "NAT64", "guest", `{"vrf": "guest", "prefix": "64:ff9b::/96", "pool4": []}`),
		},
		"Resolver": {derivetest.RouterDoc(t, "Resolver", "guest", `{"vrf": "guest", "listen": ["2001:db8:3::1"], "allow": ["2001:db8:3::/64"]}`)},
	}
	ruleset := func(docs map[string][]any) string {
		t.Helper()
		input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
		if err != nil {
			t.Fatal(err)
		}
		return renderedFile(t, []string{"router-nftables", "50000"}, string(input), rulesetFile)
	}
	got := ruleset(docs)
	for _, want := range []string{
		"\t\ticmp type { destination-unreachable, time-exceeded, parameter-problem } accept\n" +
			"\t\tiifname \"n64-edge\" ip6 saddr 64:ff9b::/96 accept comment \"NAT64 edge\"\n" +
			"\t\tiifname \"n64-edge\" ip saddr { 192.0.2.8/30 } accept comment \"NAT64 edge\"\n" +
			"\t\toifname \"n64-edge\" ip6 daddr 64:ff9b::/96 accept comment \"NAT64 edge\"\n" +
			"\t\toifname \"n64-edge\" ip daddr { 192.0.2.8/30 } accept comment \"NAT64 edge\"\n" +
			"\t\tmeta sdifname \"n64-guest\" ip6 saddr 64:ff9b::/96 accept comment \"NAT64 guest\"\n" +
			"\t\tmeta sdifname \"n64-guest\" ip saddr 169.254.111.206 accept comment \"NAT64 guest\"\n" +
			"\t\toifname \"n64-guest\" ip6 daddr 64:ff9b::/96 accept comment \"NAT64 guest\"\n" +
			"\t\tct status dnat accept\n" +
			"\t\tiifname { \"eth0\" } oifname { \"eth4\" } jump policy-lan-wan\n",
		"\t\tiifname { \"eth3\", \"eth4\", \"n64-guest\" } ct zone set 1 comment \"VRF guest\"\n",
		"\t\toifname { \"eth3\", \"eth4\", \"n64-guest\" } ct zone set 1 comment \"VRF guest\"\n",
		"\t\ttype nat hook postrouting priority srcnat; policy accept;\n" +
			"\t\toifname \"n64-edge\" accept comment \"NAT64 edge\"\n" +
			"\t\tip6 saddr 64:ff9b::/96 accept comment \"NAT64 edge\"\n" +
			"\t\tip saddr { 192.0.2.8/30 } accept comment \"NAT64 edge\"\n" +
			"\t\toifname \"n64-guest\" accept comment \"NAT64 guest\"\n" +
			"\t\tip6 saddr 64:ff9b::/96 accept comment \"NAT64 guest\"\n" +
			"\t\tip saddr 169.254.111.206 meta l4proto { tcp, udp } masquerade to :61001-65535 comment \"NAT64 guest\"\n" +
			"\t\tip saddr 169.254.111.206 masquerade comment \"NAT64 guest\"\n" +
			"\t\tmeta nfproto ipv4 oifname { \"eth4\" } masquerade\n",
		// A DNAT rule leaves the translated IPv4 that leaves a NAT64's link, and an explicit
		// pool's addresses, alone.
		"\t\ttype nat hook prerouting priority dstnat; policy accept;\n" +
			"\t\tiifname \"n64-edge\" accept comment \"NAT64 edge\"\n" +
			"\t\tip6 daddr 64:ff9b::/96 accept comment \"NAT64 edge\"\n" +
			"\t\tip daddr { 192.0.2.8/30 } accept comment \"NAT64 edge\"\n" +
			"\t\tiifname \"n64-guest\" accept comment \"NAT64 guest\"\n" +
			"\t\tip6 daddr 64:ff9b::/96 accept comment \"NAT64 guest\"\n" +
			"\t\tmeta nfproto ipv4 iifname { \"*\" } tcp dport { 80 } dnat ip to 192.0.2.80\n",
		"\t\tiifname \"guest\" ip6 saddr { 2001:db8:3::/64 } ip6 daddr { 2001:db8:3::1/128 } udp dport { 53 } accept comment \"Resolver guest\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the ruleset lacks\n%s\nin\n%s", want, got)
		}
	}
	open := maps.Clone(docs)
	delete(open, "FirewallPolicy")
	delete(open, "NATRule")
	open["NAT64"] = docs["NAT64"][:1]
	if got := ruleset(open); strings.Contains(got, "NAT64") {
		t.Errorf("a ruleset that forwards everything and translates nothing has NAT64 lines:\n%s", got)
	}
	open["NAT64"] = docs["NAT64"][1:]
	if got := ruleset(open); !strings.Contains(got, "\t\tip saddr 169.254.111.206 masquerade comment \"NAT64 guest\"\n") {
		t.Errorf("a ruleset without NATRules lacks the masquerade of an own pool:\n%s", got)
	}
	// A VRF without a conntrack zone leaves its NAT64's link out of the zones, and the forward
	// chain still picks the link by sdifname.
	zoneless := maps.Clone(docs)
	zoneless["VRF"] = []any{derivetest.RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"]}`)}
	got = ruleset(zoneless)
	if strings.Contains(got, "ct zone") {
		t.Errorf("a ruleset without conntrack zones sets one:\n%s", got)
	}
	if want := "\t\tmeta sdifname \"n64-guest\" ip6 saddr 64:ff9b::/96 accept comment \"NAT64 guest\"\n"; !strings.Contains(got, want) {
		t.Errorf("the ruleset lacks\n%s\nin\n%s", want, got)
	}
}

// A dnat rule whose destination is wider than a NAT64's pool translates the connections the NAT64
// does not take; the validator router refuses a destination inside the pool or the prefix.
func TestNATRuleWiderThanNAT64Pool(t *testing.T) {
	nat64 := derivetest.RouterDoc(t, "NAT64", "edge", `{"prefix": "64:ff9b::/96", "pool4": [{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp"]}]}`)
	zone := derivetest.RouterDoc(t, "FirewallZone", "wan", `{"interfaces": ["eth0"]}`)
	rule := derivetest.RouterDoc(t, "NATRule", "web", `{"type": "dnat", "family": "ipv4", "in": {"zone": "wan"}, "match": {"destination": ["192.0.2.0/24"]}, "to": {"address": "198.51.100.1"}}`)
	docs := map[string][]any{"NAT64": {nat64}, "FirewallZone": {zone}, "NATRule": {rule}}
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	if got := renderedFile(t, []string{"router-nftables", "50000"}, string(input), rulesetFile); !strings.Contains(got, "ip daddr { 192.0.2.0/24 } dnat ip to 198.51.100.1") {
		t.Errorf("a dnat rule of a range wider than the pool is missing:\n%s", got)
	}
}

// Unbound refuses every client but those of allow, whose lines follow the refusals: of two lines of
// one range the later decides, so an allow of a whole family opens it.
func TestUnboundAccessControl(t *testing.T) {
	for allow, want := range map[string]string{
		`["2001:db8::/32"]`: "  access-control: 0.0.0.0/0 refuse\n  access-control: ::/0 refuse\n  access-control: 2001:db8::/32 allow\n",
		`["::/0"]`:          "  access-control: 0.0.0.0/0 refuse\n  access-control: ::/0 refuse\n  access-control: ::/0 allow\n",
	} {
		var r derive.ResolverSpec
		if err := json.Unmarshal([]byte(`{"listen": ["2001:db8:ffff::1"], "allow": `+allow+`}`), &r); err != nil {
			t.Fatal(err)
		}
		text, err := unboundConfig("main", r, derive.DefaultVRF, testTrustAnchor, testZoneFile)
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

// The renderers trust what the schemas and rules check, and guard what would add syntax to the
// ruleset or keep it from loading, should a document bypass them, with an internal error: names
// and ports the ruleset writes unquoted, interfaces, families, ICMP types and the hash policy.
func TestRouterRefusals(t *testing.T) {
	doc := func(kind, name, spec string) any {
		var s any
		dec := json.NewDecoder(strings.NewReader(spec))
		dec.UseNumber()
		if err := dec.Decode(&s); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"apiVersion": derive.API, "kind": kind, "metadata": map[string]any{"name": name}, "spec": s}
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
		"a port that is no number":       {[]string{"router-nftables", "chalkd"}, nil, `"chalkd" is no TCP port of chalkd`},
		"an unknown hash":                {[]string{"router-sysctl"}, map[string][]any{"Router": {doc("Router", "r", `{"forwarding": {"ipv4": true, "ipv6": true}, "rpFilter": "loose", "ecmpHash": "l5", "conntrackMax": null, "management": {"interfaces": null}}`)}}, `internal error: Router r: "l5" is none of l3, l4`},
		"an unknown reverse-path filter": {[]string{"router-nftables", "50000"}, map[string][]any{"Router": {doc("Router", "r", `{"forwarding": {"ipv4": true, "ipv6": true}, "rpFilter": "any", "ecmpHash": "l4", "conntrackMax": null, "management": {"interfaces": null}}`)}}, `internal error: Router r: "any" is none of off, loose, strict`},
		// Ports, which the ruleset writes unquoted.
		"an inverted range":       {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"protocol": "tcp", "destinationPorts": ["90-80"], "action": "accept"}`)}}, `internal error: FirewallPolicy wan-local: "90-80" is no range of ports`},
		"port zero":               {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"protocol": "tcp", "destinationPorts": [0], "action": "accept"}`)}}, "internal error: FirewallPolicy wan-local: a port is a number from 1 to 65535"},
		"a range from port zero":  {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"protocol": "tcp", "destinationPorts": ["0-80"], "action": "accept"}`)}}, `internal error: FirewallPolicy wan-local: "0-80" is no range of ports`},
		"a port that adds a rule": {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"protocol": "tcp", "destinationPorts": ["22 } accept; tcp dport { 23"], "action": "accept"}`)}}, `internal error: FirewallPolicy wan-local: "22 } accept; tcp dport { 23" is no range of ports`},
		// Values nftables would read as syntax of the ruleset.
		"an ICMP type that adds a rule": {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"protocol": "icmp", "icmpTypes": ["echo-request } ip saddr 0.0.0.0/0 accept; icmp type { echo-reply"], "action": "drop"}`)}}, `internal error: FirewallPolicy wan-local: "echo-request } ip saddr 0.0.0.0/0 accept; icmp type { echo-reply" is none of echo-request, echo-reply, destination-unreachable, time-exceeded, parameter-problem, packet-too-big`},
		"a family that adds a match":    {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {rule(`{"family": "ipv4 ip saddr 0.0.0.0/0", "action": "accept"}`)}}, `internal error: FirewallPolicy wan-local: "ipv4 ip saddr 0.0.0.0/0" is none of ipv4, ipv6`},
		"a set's family":                {[]string{"router-nftables", "50000"}, map[string][]any{"AddressSet": {doc("AddressSet", "office", `{"family": "ipv4_addr; flags interval", "prefixes": ["192.0.2.0/24"]}`)}}, `internal error: AddressSet office: "ipv4_addr; flags interval" is none of ipv4, ipv6`},
		"a policy name that adds rules": {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "FirewallPolicy": {doc("FirewallPolicy", "x { accept; }; chain y", `{"from": "wan", "to": "local", "default": "drop", "rules": []}`)}}, `internal error: a FirewallPolicy document: "x { accept; }; chain y" is no document name`},
		"a set name in capitals":        {[]string{"router-nftables", "50000"}, map[string][]any{"AddressSet": {doc("AddressSet", "Office", `{"family": "ipv4", "prefixes": ["192.0.2.0/24"]}`)}}, `internal error: a AddressSet document: "Office" is no document name`},
		"an interface that ends a set":  {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {doc("FirewallZone", "wan", `{"interfaces": ["eth0\" } accept; iifname { \"eth1"]}`)}}, `internal error: FirewallZone wan: "eth0\" } accept; iifname { \"eth1" is no interface name or glob`},
		"a glob inside a name":          {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {doc("FirewallZone", "wan", `{"interfaces": ["e*h0"]}`)}}, `internal error: FirewallZone wan: "e*h0" is no interface name or glob`},
		"a management interface":        {[]string{"router-nftables", "50000"}, map[string][]any{"Router": {doc("Router", "router", `{"forwarding": {"ipv4": true, "ipv6": true}, "rpFilter": "loose", "ecmpHash": "l4", "conntrackMax": null, "management": {"interfaces": ["eth0 accept"]}}`)}}, `internal error: Router: management.interfaces: "eth0 accept" is no interface name or glob`},
		"a NAT rule's interface":        {[]string{"router-nftables", "50000"}, map[string][]any{"NATRule": {doc("NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": {"zone": null, "interface": "eth0\"; accept"}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)}}, `internal error: NATRule n: "eth0\"; accept" is no interface name or glob`},
		"no management interface":       {[]string{"router-nftables", "50000"}, map[string][]any{"Router": {doc("Router", "router", `{"forwarding": {"ipv4": true, "ipv6": true}, "rpFilter": "loose", "ecmpHash": "l4", "conntrackMax": null, "management": {"interfaces": []}}`)}}, "internal error: Router: management.interfaces names no interface"},
		// What would panic: a NAT rule of no place and a translation of no address.
		"a NAT rule of no place": {[]string{"router-nftables", "50000"}, map[string][]any{"NATRule": {doc("NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": null, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)}}, "internal error: NATRule n: a masquerade rule names no place"},
		"snat without to":        {[]string{"router-nftables", "50000"}, map[string][]any{"FirewallZone": {zone}, "NATRule": {doc("NATRule", "n", `{"type": "snat", "family": "ipv4", "out": {"zone": "wan", "interface": null}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)}}, "internal error: NATRule n: snat has no to"},
		// A BGPPeer's ports open on its interfaces or from its address alone.
		"a peer's interface that ends a set": {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"remoteAS": "external", "interface": "eth1\" } accept; iifname { \"eth2"}`)}}, `internal error: BGPPeer a: "eth1\" } accept; iifname { \"eth2" is no interface name or glob`},
		"a peer's address with a zone":       {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"address": "fe80::1%eth1"}`)}}, `internal error: BGPPeer a: spec.address: "fe80::1%eth1" has a zone`},
		"a peer's address range":             {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"address": "0.0.0.0/0"}`)}}, `internal error: BGPPeer a: ParseAddr("0.0.0.0/0"): unexpected character (at "/0")`},
		"a peer of no interface":             {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"remoteAS": "external", "interfaces": []}`)}}, "internal error: BGPPeer a names no interface"},
		// Without address and interface, the opening is of the interfaces, which would open the
		// port on every link.
		"a peer of nothing":            {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"remoteAS": "external"}`)}}, "internal error: BGPPeer a names no interface"},
		"a peer's VRF that ends a set": {[]string{"router-nftables", "50000"}, map[string][]any{"BGPPeer": {doc("BGPPeer", "a", `{"remoteAS": "external", "vrf": "x\" accept", "interface": "eth1"}`)}}, `internal error: BGPPeer a: the VRF "x\" accept" is no VRF's name`},
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

// The FRR renderer refuses what it alone can see: an interface named as another peer's group and
// an AS path bgpd would not compile. It guards, with an internal error, values frr.conf would read
// as further commands or arguments, references to no document and a prepend's count, should a
// document bypass the schemas, the rules and the validator router.
func TestFRRRefusals(t *testing.T) {
	doc := func(kind, name, spec string) any {
		var s any
		dec := json.NewDecoder(strings.NewReader(spec))
		dec.UseNumber()
		if err := dec.Decode(&s); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"apiVersion": derive.API, "kind": kind, "metadata": map[string]any{"name": name}, "spec": s}
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
	description := func(d string) map[string][]any {
		return map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "description": `+strconv.Quote(d)+`, "families": {"ipv6": {}}}`)}}
	}
	const notDescription = "internal error: BGPPeer a: the description is printable ASCII characters but @, with single spaces between words"
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"an interface of a peer group's name": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {
			peer("fabric", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {}}}`),
			peer("x", `{"interface": "peer-fabric", "remoteAS": "external", "families": {"ipv6": {}}}`)}}, "BGPPeer/fabric: metadata.name: BGPPeer x names peer-fabric, the peer group of BGPPeer fabric"},
		// AS paths bgpd's regcomp refuses, which would leave the term matching no route.
		"an AS path that does not compile":       {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "(1"}, "then": {"action": "accept"}}`)}}, `RoutingPolicy/p: spec.terms[0].match.asPath: asPath "(1" is no regular expression bgpd compiles`},
		"an AS path with a parenthesis too many": {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "1)"}, "then": {"action": "accept"}}`)}}, `asPath "1)" is no regular expression`},
		"an AS path that starts with a star":     {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "*1"}, "then": {"action": "accept"}}`)}}, `asPath "*1" is no regular expression`},
		"an AS path that repeats an anchor":      {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "^*1"}, "then": {"action": "accept"}}`)}}, `asPath "^*1" is no regular expression`},
		"an AS path that repeats an alternative": {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "(1|+2)"}, "then": {"action": "accept"}}`)}}, `asPath "(1|+2)" is no regular expression`},
		"an AS path with an AS set's brace":      {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "_{64512"}, "then": {"action": "accept"}}`)}}, `asPath "_{64512" is no regular expression`},
		"an AS path with an open interval":       {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "1{,5}"}, "then": {"action": "accept"}}`)}}, `asPath "1{,5}" is no regular expression`},
		"an AS path with an inverted interval":   {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "1{3,2}"}, "then": {"action": "accept"}}`)}}, `asPath "1{3,2}" is no regular expression`},
		"an AS path with an unclosed interval":   {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "1{2"}, "then": {"action": "accept"}}`)}}, `asPath "1{2" is no regular expression`},
		"a peer without a BGP document": {map[string][]any{"BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {}}}`)}},
			"internal error: the config holds BGPPeer documents but no BGP document"},
		// Guards of values FRR would read as further commands or arguments of the config.
		"an IPv6 router ID":               {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "2001:db8::1"}`)}}, `internal error: BGP bgp: routerID 2001:db8::1 is no IPv4 address`},
		"a router ID of five parts":       {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1.1"}`)}}, `internal error: BGP bgp: ParseAddr("192.0.2.1.1"): IPv4 address too long`},
		"a router ID that adds a command": {map[string][]any{"BGP": {doc("BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1\n neighbor 192.0.2.9 remote-as 64999"}`)}}, `internal error: BGP bgp: ParseAddr("192.0.2.1\n neighbor 192.0.2.9 remote-as 64999"): unexpected character (at "\n neighbor 192.0.2.9 remote-as 64999")`},
		"an unknown remote AS":            {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "anyone", "families": {"ipv6": {}}}`)}}, `internal error: BGPPeer a: remoteAS is external, internal or an AS number from 1 to 4294967295, not "anyone"`},
		"a remote AS that adds a neighbour": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external\n neighbor 192.0.2.9 remote-as 64999", "families": {"ipv6": {}}}`)}},
			"internal error: BGPPeer a: remoteAS is external, internal or an AS number"},
		"a fractional remote AS":       {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": 64512.5, "families": {"ipv6": {}}}`)}}, "internal error: BGPPeer a: remoteAS is external, internal or an AS number from 1 to 4294967295, not 64512.5"},
		"a community of 17 bits":       {map[string][]any{"BGP": {bgp("")}, "CommunityList": {doc("CommunityList", "c", `{"communities": ["65536:1"]}`)}}, `internal error: CommunityList c: "65536:1" is no community: its parts are numbers of 16 bits`},
		"a large community of 33 bits": {map[string][]any{"BGP": {bgp("")}, "CommunityList": {doc("CommunityList", "c", `{"communities": ["1:4294967296:1"]}`)}}, `internal error: CommunityList c: "1:4294967296:1" is no community: its parts are numbers of 32 bits`},
		"a community with a sign":      {map[string][]any{"BGP": {bgp("")}, "CommunityList": {doc("CommunityList", "c", `{"communities": ["+1:1"]}`)}}, `internal error: CommunityList c: "+1:1" is no community`},
		"a community that adds an argument": {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"then": {"action": "accept", "addCommunities": ["64512:1 no-export"]}}`)}},
			`internal error: RoutingPolicy p: "64512:1 no-export" is no community: its parts are numbers of 16 bits`},
		"an AS path that adds a command": {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"asPath": "^1$\n neighbor x shutdown"}, "then": {"action": "accept"}}`)}},
			`internal error: RoutingPolicy p: term 1: asPath "^1$\n neighbor x shutdown" holds more than digits, single spaces and _^$.*+?()|{},-`},
		"a description of two lines":           {description("a\nb"), notDescription},
		"a description of spaces":              {description("  "), notDescription},
		"a description of two spaces in a row": {description("a  b"), notDescription},
		"a description that ends in a space":   {description("a "), notDescription},
		"a description that adds a neighbour":  {description("x\n neighbor 192.0.2.9 remote-as 64999"), notDescription},
		"a description that ends the router":   {description("x\rexit\r"), notDescription},
		// The credentials helper fills in a placeholder of a password line, and vtysh shows a
		// description to every reader.
		"a description that holds a placeholder": {description("x password @secret_bgp.fabric@"), notDescription},
		"a password's key with a space": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "password": {"secretRef": {"name": "bgp", "key": "fab ric"}}, "families": {"ipv6": {}}}`)}},
			`internal error: BGPPeer a: the password's Secret "bgp" and key "fab ric" are letters, digits, '.', '_' and '-' alone`},
		"a password that adds a command": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "password": {"secretRef": {"name": "bgp@\n neighbor 192.0.2.9 remote-as 64999\n", "key": "k"}}, "families": {"ipv6": {}}}`)}},
			"internal error: BGPPeer a: the password's Secret"},
		"a password's key with an @": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "password": {"secretRef": {"name": "bgp", "key": "a@b"}}, "families": {"ipv6": {}}}`)}},
			`internal error: BGPPeer a: the password's Secret "bgp" and key "a@b"`},
		"a network that adds an argument": {map[string][]any{"BGP": {bgp(`{"prefix": "192.0.2.0/24 route-map x"}`)}},
			`internal error: BGP bgp: netip.ParsePrefix("192.0.2.0/24 route-map x"): bad bits after slash: "24 route-map x"`},
		"a prefix that adds an argument": {map[string][]any{"BGP": {bgp("")}, "PrefixList": {list("l", "ipv4", `{"prefix": "192.0.2.0/24 le 32", "action": "permit"}`)}},
			`internal error: PrefixList l: netip.ParsePrefix("192.0.2.0/24 le 32"): bad bits after slash: "24 le 32"`},
		"a prefix list action that adds a prefix": {map[string][]any{"BGP": {bgp("")}, "PrefixList": {list("l", "ipv4", `{"prefix": "192.0.2.0/24", "action": "permit 0.0.0.0/0\nip prefix-list pl-l seq 1 permit"}`)}},
			`internal error: PrefixList l: "permit 0.0.0.0/0\nip prefix-list pl-l seq 1 permit" is none of permit, deny`},
		"a policy name that adds a command": {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {doc("RoutingPolicy", "p 10\nexit\nrouter bgp 64999", `{"terms": [], "default": "reject"}`)}},
			`internal error: a RoutingPolicy document: "p 10\nexit\nrouter bgp 64999" is no document name`},
		"a peer name that adds a command": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a remote-as 64999", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {}}}`)}},
			`internal error: a BGPPeer document: "a remote-as 64999" is no document name`},
		"a list a term names that adds a command": {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"match": {"prefixList": "v4\n set metric 1"}, "then": {"action": "accept"}}`)}},
			`internal error: RoutingPolicy p: term 1: the input holds no PrefixList "v4\n set metric 1"`},
		"a policy a family names that adds a command": {map[string][]any{"BGP": {bgp("")}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {"import": "p in\n neighbor 192.0.2.9 remote-as 64999"}}}`)}},
			`internal error: BGPPeer a: families.ipv6: the input holds no RoutingPolicy "p in\n neighbor 192.0.2.9 remote-as 64999"`},
		"a network's policy that adds a command": {map[string][]any{"BGP": {bgp(`{"prefix": "192.0.2.0/24", "policy": "p\n neighbor 192.0.2.9 remote-as 64999"}`)}},
			`internal error: BGP bgp: network 192.0.2.0/24: the input holds no RoutingPolicy`},
		"a tracked list that adds a command": {map[string][]any{"BGP": {bgp("")}, "PrefixList": {v6}, "BGPPeer": {peer("a", `{"interface": "eth1", "remoteAS": "external", "families": {"ipv6": {"defaultOriginate": true, "defaultOriginateWhen": "v6\n neighbor 192.0.2.9 remote-as 64999"}}}`)}},
			"internal error: BGPPeer a: families.ipv6: defaultOriginateWhen: the input holds no PrefixList"},
		// A prepend's count sizes a list: below one it would panic, and a large one would take the
		// memory.
		"a prepend of 11":     {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"then": {"action": "accept", "prepend": 11}}`)}}, "internal error: RoutingPolicy p: term 1: prepend 11 lies outside 1 to 10"},
		"a prepend below one": {map[string][]any{"BGP": {bgp("")}, "RoutingPolicy": {policy("p", `{"then": {"action": "accept", "prepend": -1}}`)}}, "internal error: RoutingPolicy p: term 1: prepend -1 lies outside 1 to 10"},
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

// The Jool renderer guards, with an internal error, what jool file check passes and the kernel
// refuses once chalkos-nat64 loads an instance, and values that would add to the JSON Jool reads,
// were it written by hand. The validator router refuses NAT64s that share a prefix, a pool's
// address or a link.
func TestJoolRefusals(t *testing.T) {
	nat64 := func(name, spec string) any { return derivetest.RouterDoc(t, "NAT64", name, spec) }
	pool := func(entries string) any { return nat64("edge", `{"prefix": "64:ff9b::/96", "pool4": [`+entries+`]}`) }
	const notPrefix = "is no IPv6 range of 32, 40, 48, 56, 64 or 96 bits whose bits 64 to 71 are zero; chalkd checks the config against the schemas, the rules and the validators before it renders"
	for name, c := range map[string]struct {
		docs []any
		want string
	}{
		// What jool file check passes and the kernel refuses once chalkos-nat64 loads the instance.
		"a name of 12 characters": {[]any{nat64("abcdefghijkl", `{"prefix": "64:ff9b::/96", "pool4": []}`)},
			"internal error: NAT64 abcdefghijkl: its link n64-abcdefghijkl is longer than an interface name's 15 characters; chalkd checks the config against the schemas, the rules and the validators before it renders"},
		"a prefix of 95 bits": {[]any{nat64("edge", `{"prefix": "64:ff9b::/95", "pool4": []}`)}, `internal error: NAT64 edge: the prefix 64:ff9b::/95 ` + notPrefix},
		"an IPv4 prefix":      {[]any{nat64("edge", `{"prefix": "192.0.2.0/24", "pool4": []}`)}, `internal error: NAT64 edge: the prefix 192.0.2.0/24 ` + notPrefix},
		"bits 64 to 71 set":   {[]any{nat64("edge", `{"prefix": "2001:db8:0:0:ff00::/96", "pool4": []}`)}, `internal error: NAT64 edge: the prefix 2001:db8:0:0:ff00::/96 ` + notPrefix},
		"an IPv6 pool":        {[]any{pool(`{"prefix": "2001:db8::/64", "ports": "61001-65535", "protocols": ["tcp"]}`)}, `internal error: NAT64 edge: pool entry 1: 2001:db8::/64 is no IPv4 range of 16 bits or more`},
		"a pool of a /15":     {[]any{pool(`{"prefix": "198.18.0.0/15", "ports": "61001-65535", "protocols": ["tcp"]}`)}, `internal error: NAT64 edge: pool entry 1: 198.18.0.0/15 is no IPv4 range of 16 bits or more`},
		"ephemeral ports":     {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "1-65535", "protocols": ["tcp"]}`)}, "internal error: NAT64 edge: pool entry 1: the ports 1-65535 take the kernel's ephemeral ports, 32768 to 60999, which Jool refuses"},
		"one ephemeral port":  {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": 40000, "protocols": ["icmp"]}`)}, "internal error: NAT64 edge: pool entry 1: the ports 40000 take the kernel's ephemeral ports"},
		"port zero":           {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "0-100", "protocols": ["tcp"]}`)}, `internal error: NAT64 edge: "0-100" is no range of ports`},
		"an inverted range":   {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "65535-61001", "protocols": ["tcp"]}`)}, `internal error: NAT64 edge: "65535-61001" is no range of ports`},
		// Values that would add to the JSON Jool reads, were it written by hand.
		"a prefix that adds a key":  {[]any{nat64("edge", `{"prefix": "64:ff9b::/96\", \"framework\": \"iptables", "pool4": []}`)}, "internal error: NAT64 edge: netip.ParsePrefix"},
		"a pool that adds an entry": {[]any{pool(`{"prefix": "192.0.2.8/30\"}, {\"prefix\": \"198.51.100.0/24", "ports": "61001-65535", "protocols": ["tcp"]}`)}, "internal error: NAT64 edge: netip.ParsePrefix"},
		"ports that add a key":      {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535\", \"mark\": \"1", "protocols": ["tcp"]}`)}, "is no range of ports"},
		"a name that adds a key":    {[]any{nat64("e\", \"x", `{"prefix": "64:ff9b::/96", "pool4": []}`)}, "internal error: a NAT64 document: \"e\\\", \\\"x\" is no document name"},
		"a prefix of a line break":  {[]any{nat64("edge", `{"prefix": "64:ff9b::/96\n", "pool4": []}`)}, "internal error: NAT64 edge: netip.ParsePrefix"},
		"a pool that ends the list": {[]any{pool(`{"prefix": "192.0.2.8/30\"}]}", "ports": "61001-65535", "protocols": ["tcp"]}`)}, "internal error: NAT64 edge: netip.ParsePrefix"},
		"ports of a backslash":      {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535\\", "protocols": ["tcp"]}`)}, "is no range of ports"},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"jool"}, map[string][]any{"NAT64": c.docs}, c.want) })
	}
}

// Jool's instance takes no protocol the schema does not: a pool entry renders an entry per
// protocol of Jool's own words, whatever else the document names, and an entry of none of them is
// an internal error, not an entry the pool drops.
func TestJoolProtocols(t *testing.T) {
	nat64 := func(protocols string) map[string][]any {
		return map[string][]any{"NAT64": {derivetest.RouterDoc(t, "NAT64", "edge", `{"prefix": "64:ff9b::/96", "pool4": [{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": [`+protocols+`]}]}`)}}
	}
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": nat64(`"icmp", "tcp\", \"mark\": \"1", "tcp"`)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := render(t, []string{"jool"}, string(input))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "edge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got joolInstance
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []joolPool4{{"TCP", "192.0.2.8/30", "61001-65535"}, {"ICMP", "192.0.2.8/30", "61001-65535"}}
	if !slices.Equal(got.Pool4, want) {
		t.Errorf("pool4 = %v, want %v", got.Pool4, want)
	}
	refused(t, []string{"jool"}, nat64(`"tcp\", \"mark\": \"1"`), "internal error: NAT64 edge: pool entry 1 names no protocol Jool takes")
}

// Each NAT64 renders Jool's file and its link's: an explicit pool's prefixes are routed to the
// inner end, and an empty pool4 takes the inner address with the ports 61001-65535. The /30 of a
// NAT64's link comes from the hash of its name alone, so a NAT64 keeps it while others come and
// go; a VRF's NAT64 names the VRF and its table.
func TestNAT64Links(t *testing.T) {
	edge := derivetest.RouterDoc(t, "NAT64", "edge", `{"prefix": "64:ff9b::/96", "pool4": [{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp", "udp"]}, {"prefix": "192.0.2.8/30", "ports": "1-1000", "protocols": ["icmp"]}]}`)
	guest := derivetest.RouterDoc(t, "NAT64", "guest", `{"vrf": "guest", "prefix": "64:ff9b::/96", "pool4": []}`)
	vrf := map[string][]any{
		"VRF":     {derivetest.RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3"]}`)},
		"Network": {derivetest.Doc(t, "Network", "40-guest", `{"match": {"name": "eth3"}}`)},
	}
	// rendered renders the output jool of the NAT64 documents nat64 beside the VRF guest.
	rendered := func(nat64 ...any) string {
		t.Helper()
		docs := map[string][]any{"NAT64": nat64}
		maps.Copy(docs, vrf)
		input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
		if err != nil {
			t.Fatal(err)
		}
		out, err := render(t, []string{"jool"}, string(input))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	read := func(out, name string, v any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]nat64Link{
		"edge":  {Table: 254, Outer: "169.254.96.241/30", Inner: "169.254.96.242/30", Routes: []string{"192.0.2.8/30"}},
		"guest": {VRF: ptr("guest"), Table: 300, Outer: "169.254.111.205/30", Inner: "169.254.111.206/30", Routes: []string{}},
	}
	both := rendered(edge, guest)
	for name, want := range links {
		var got nat64Link
		read(both, name+".veth", &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s.veth = %+v, want %+v", name, got, want)
		}
	}
	var g joolInstance
	read(both, "guest.json", &g)
	want := []joolPool4{{"TCP", "169.254.111.206/32", "61001-65535"}, {"UDP", "169.254.111.206/32", "61001-65535"}, {"ICMP", "169.254.111.206/32", "61001-65535"}}
	if !slices.Equal(g.Pool4, want) {
		t.Errorf("guest's pool4 = %v, want %v", g.Pool4, want)
	}
	// guest keeps its link without edge.
	var alone nat64Link
	read(rendered(guest), "guest.veth", &alone)
	if !reflect.DeepEqual(alone, links["guest"]) {
		t.Errorf("guest.veth alone = %+v, want %+v", alone, links["guest"])
	}
}

// The Unbound renderer refuses what the rules leave to it, as they would compare the records in
// pairs: a record twice and a CNAME record beside another record of its name, in any zone. It
// guards, with an internal error, values that Unbound's config would read as syntax, a zone type
// it writes unquoted, and an empty listen or allow, which would open the firewall wider; the
// output's unbound-checkconf refuses the rest of what Unbound cannot load.
func TestUnboundRefusals(t *testing.T) {
	resolver := func(more string) any {
		return derivetest.RouterDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "forwarders": [], "dnssec": true, "dns64": null`+more+`}`)
	}
	zone := func(z string) any { return resolver(`, "localZones": [` + z + `]`) }
	record := func(typ, value string) any {
		return zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "` + typ + `", "value": ` + strconv.Quote(value) + `, "ttl": 3600}]}`)
	}
	// data is the internal error of a record whose data is not of its type.
	data := func(typ, value string) string {
		return "internal error: Resolver resolver: local zone 1: lan.example.org: record 1: " + strconv.Quote(value) + " is no " + typ + " record's data; chalkd checks the config against the schemas, the rules and the validators before it renders"
	}
	args := []string{"unbound", testTrustAnchor, testZoneFile}
	for name, c := range map[string]struct {
		args []string
		docs []any
		want string
	}{
		"a record twice": {args, []any{zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "AAAA", "value": "2001:db8::1", "ttl": 3600},
			{"name": "a.lan.example.org", "type": "AAAA", "value": "2001:DB8::1", "ttl": 300}]}`)}, "Resolver/resolver: spec.localZones[0].records[1]: a.lan.example.org. AAAA 2001:db8::1 appears twice; Unbound keeps one, so drop the other"},
		"a record in two zones": {args, []any{zone(`{"name": "example.org", "type": "transparent", "records": [{"name": "a.lan.example.org", "type": "A", "value": "192.0.2.1", "ttl": 3600}]},
			{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "A", "value": "192.0.2.1", "ttl": 3600}]}`)}, "Resolver/resolver: spec.localZones[1].records[0]: a.lan.example.org. A 192.0.2.1 appears twice; Unbound keeps one, so drop the other"},
		"a CNAME beside another record": {args, []any{zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "A", "value": "192.0.2.1", "ttl": 3600},
			{"name": "a.lan.example.org", "type": "CNAME", "value": "r1.lan.example.org", "ttl": 3600}]}`)}, "Resolver/resolver: spec.localZones[0].records[1]: a.lan.example.org holds a CNAME record and another record; a CNAME's name holds it alone"},
		"two CNAMEs of one name": {args, []any{zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "CNAME", "value": "r1.lan.example.org", "ttl": 3600},
			{"name": "a.lan.example.org", "type": "CNAME", "value": "r2.lan.example.org", "ttl": 3600}]}`)}, "Resolver/resolver: spec.localZones[0].records[1]: a.lan.example.org holds a CNAME record and another record"},
		"a CNAME in two zones": {args, []any{zone(`{"name": "example.org", "type": "transparent", "records": [{"name": "a.lan.example.org", "type": "CNAME", "value": "r1.lan.example.org", "ttl": 3600}]},
			{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org", "type": "TXT", "value": "x", "ttl": 3600}]}`)}, "Resolver/resolver: spec.localZones[1].records[0]: a.lan.example.org holds a CNAME record and another record"},
		// The files of the image that the config names between quotes.
		"a relative trust anchor":            {[]string{"unbound", "root.key", testZoneFile}, []any{resolver("")}, `"root.key" is no absolute path`},
		"a trust anchor with a dot dot":      {[]string{"unbound", "/etc/../root.key", testZoneFile}, []any{resolver("")}, `"/etc/../root.key" is no absolute path`},
		"a trust anchor that adds an option": {[]string{"unbound", "/etc/root.key\"\n  include: \"/etc/passwd", testZoneFile}, []any{resolver("")}, "is no absolute path"},
		"a trust anchor of a hash":           {[]string{"unbound", "/etc/root.key#x", testZoneFile}, []any{resolver("")}, "is no absolute path"},
		// What would open the firewall wider.
		"no address": {args, []any{derivetest.RouterDoc(t, "Resolver", "resolver", `{"listen": [], "allow": ["2001:db8::/32"]}`)},
			"internal error: Resolver resolver: listen names no address, so the firewall would accept DNS on every address of the router"},
		"no client": {args, []any{derivetest.RouterDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": []}`)},
			"internal error: Resolver resolver: allow names no range, so the firewall would accept DNS from every client"},
		// Values Unbound's config would read as syntax, and a zone type it writes unquoted.
		"an address with a zone": {args, []any{derivetest.RouterDoc(t, "Resolver", "resolver", `{"listen": ["fe80::1%eth0"], "allow": ["2001:db8::/32"]}`)},
			`internal error: Resolver resolver: spec.listen[0]: "fe80::1%eth0" has a zone`},
		"an address that adds an option": {args, []any{derivetest.RouterDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1\n  interface: 0.0.0.0"], "allow": ["2001:db8::/32"]}`)}, `internal error: Resolver resolver: ParseAddr("2001:db8:ffff::1\n  interface: 0.0.0.0"): unexpected character, want colon (at "\n  interface: 0.0.0.0")`},
		"a client that adds an option":   {args, []any{derivetest.RouterDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32 allow\naccess-control: ::/0"]}`)}, `internal error: Resolver resolver: netip.ParsePrefix("2001:db8::/32 allow\naccess-control: ::/0"): ParseAddr("2001:db8::/32 allow\naccess-control: ::"): each colon-separated field must have at least one digit (at "/32 allow\naccess-control: ::")`},
		"a client of a hash":             {args, []any{derivetest.RouterDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32#x"]}`)}, `internal error: Resolver resolver: netip.ParsePrefix("2001:db8::/32#x"): bad bits after slash: "32#x"`},
		"a forwarder that adds a server": {args, []any{resolver(`, "forwarders": ["192.0.2.53\nserver:\n  do-not-query-localhost: no"]`)}, `internal error: Resolver resolver: ParseAddr("192.0.2.53\nserver:\n  do-not-query-localhost: no"): unexpected character (at "\nserver:\n  do-not-query-localhost: no")`},
		"a DNS64 prefix of 95 bits": {args, []any{resolver(`, "dns64": {"prefix": "64:ff9b::/95"}`)},
			`internal error: Resolver resolver: dns64: the prefix 64:ff9b::/95 is no IPv6 range of 32, 40, 48, 56, 64 or 96 bits whose bits 64 to 71 are zero`},
		"a DNS64 prefix that adds an option": {args, []any{resolver(`, "dns64": {"prefix": "64:ff9b::/96\nserver:\n  do-not-query-localhost: no"}`)}, `internal error: Resolver resolver: netip.ParsePrefix("64:ff9b::/96\nserver:\n  do-not-query-localhost: no"): bad bits after slash: "96\nserver:\n  do-not-query-localhost: no"`},
		"a zone in capitals": {args, []any{zone(`{"name": "Lan.example.org", "type": "static", "records": []}`)},
			`internal error: Resolver resolver: local zone 1: "Lan.example.org" is no DNS name in lower case without the root's dot`},
		"a zone that adds an option":       {args, []any{zone(`{"name": "lan.example.org.\" static\n  local-zone: \"x", "type": "static", "records": []}`)}, "is no DNS name"},
		"a zone that adds a server option": {args, []any{zone(`{"name": "lan.example.org\nserver:\n  do-not-query-localhost: no", "type": "static", "records": []}`)}, "is no DNS name"},
		"a zone of a hash":                 {args, []any{zone(`{"name": "lan.example.org#x", "type": "static", "records": []}`)}, "is no DNS name"},
		"an unknown zone type": {args, []any{zone(`{"name": "lan.example.org", "type": "inform", "records": []}`)},
			`internal error: Resolver resolver: "inform" is none of static, transparent, typetransparent, redirect, refuse, deny, always_nxdomain`},
		"a zone type that adds an option": {args, []any{zone(`{"name": "lan.example.org", "type": "static\n  verbosity: 5", "records": []}`)}, "is none of static"},
		"a record's name that adds data": {args, []any{zone(`{"name": "lan.example.org", "type": "static", "records": [{"name": "a.lan.example.org. 1 IN A 192.0.2.9'\n  local-data: 'b.lan.example.org", "type": "A", "value": "192.0.2.1", "ttl": 3600}]}`)},
			"internal error: Resolver resolver: local zone 1: lan.example.org: record 1: \"a.lan.example.org. 1 IN A 192.0.2.9'\\n  local-data: 'b.lan.example.org\" is no DNS name"},
		"an A record of IPv6":              {args, []any{record("A", "2001:db8::1")}, data("A", "2001:db8::1")},
		"an IPv4-mapped AAAA record":       {args, []any{record("AAAA", "::ffff:192.0.2.1")}, data("AAAA", "::ffff:192.0.2.1")},
		"an A record that adds data":       {args, []any{record("A", "192.0.2.1'\n  local-data: 'b.lan.example.org. A 192.0.2.2")}, data("A", "192.0.2.1'\n  local-data: 'b.lan.example.org. A 192.0.2.2")},
		"an AAAA record of a hash":         {args, []any{record("AAAA", "2001:db8::1 # x")}, data("AAAA", "2001:db8::1 # x")},
		"a CNAME that adds data":           {args, []any{record("CNAME", "b.lan.example.org.'\n  local-data: 'c")}, data("CNAME", "b.lan.example.org.'\n  local-data: 'c")},
		"a CNAME of the root's dot":        {args, []any{record("CNAME", "mail.lan.example.org.")}, data("CNAME", "mail.lan.example.org.")},
		"a PTR in capitals":                {args, []any{record("PTR", "Mail.lan.example.org")}, data("PTR", "Mail.lan.example.org")},
		"an MX record without a name":      {args, []any{record("MX", "10")}, data("MX", "10")},
		"an MX record that adds a server":  {args, []any{record("MX", "10 mail.lan.example.org.'\nserver:\n  do-not-query-localhost: no")}, data("MX", "10 mail.lan.example.org.'\nserver:\n  do-not-query-localhost: no")},
		"an MX record of a leading zero":   {args, []any{record("MX", "010 mail.lan.example.org")}, data("MX", "010 mail.lan.example.org")},
		"an SRV record of a big port":      {args, []any{record("SRV", "0 1 65536 mail.lan.example.org")}, data("SRV", "0 1 65536 mail.lan.example.org")},
		"an SRV record of two spaces":      {args, []any{record("SRV", "0 1  993 mail.lan.example.org")}, data("SRV", "0 1  993 mail.lan.example.org")},
		"a TXT record of a backslash":      {args, []any{record("TXT", `a\b`)}, data("TXT", `a\b`)},
		"a TXT record that ends the quote": {args, []any{record("TXT", "x' \n  include: '/etc/passwd")}, data("TXT", "x' \n  include: '/etc/passwd")},
		"a TXT record of a line break":     {args, []any{record("TXT", "a\nb")}, data("TXT", "a\nb")},
		"a TXT record that adds a server":  {args, []any{record("TXT", "x\"'\nserver:\n  do-not-query-localhost: no")}, data("TXT", "x\"'\nserver:\n  do-not-query-localhost: no")},
		"a TXT record of a quote":          {args, []any{record("TXT", `say "hi"`)}, data("TXT", `say "hi"`)},
		"a TXT record of a single quote":   {args, []any{record("TXT", "it's")}, data("TXT", "it's")},
		"a TXT record past 255 characters": {args, []any{record("TXT", strings.Repeat("x", 256))}, data("TXT", strings.Repeat("x", 256))},
		"an unknown record type":           {args, []any{record("NS", "ns.lan.example.org")}, data("NS", "ns.lan.example.org")},
	} {
		t.Run(name, func(t *testing.T) { refused(t, c.args, map[string][]any{"Resolver": c.docs}, c.want) })
	}
}

// Each VRF's resolver renders a config of its own, unbound-<vrf>.conf, with a control socket of
// its own; the default VRF's stays unbound.conf. A listen address the resolver's VRF holds renders,
// also when another VRF holds it too.
func TestUnboundPerVRF(t *testing.T) {
	docs := func(listen string) map[string][]any {
		return map[string][]any{
			"VRF": {derivetest.RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3"]}`)},
			"Network": {
				derivetest.Doc(t, "Network", "10-eth0", `{"match": {"name": "eth0"}, "network": {"address": ["2001:db8:ffff::1/128", "2001:db8:3::9/64"]}}`),
				derivetest.Doc(t, "Network", "40-guest", `{"match": {"name": "eth3"}, "network": {"address": ["2001:db8:3::1/64", "2001:db8:3::9/64"]}}`),
			},
			"Resolver": {
				derivetest.RouterDoc(t, "Resolver", "guest", `{"vrf": "guest", "listen": ["`+listen+`"], "allow": ["2001:db8:3::/64"], "forwarders": [], "dnssec": false, "dns64": {"prefix": "64:ff9b::/96"}, "localZones": []}`),
				derivetest.RouterDoc(t, "Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "forwarders": [], "dnssec": false, "dns64": null, "localZones": []}`),
			},
		}
	}
	args := []string{"unbound", testTrustAnchor, testZoneFile}
	for _, listen := range []string{"2001:db8:3::1", "2001:db8:3::9"} {
		got, err := unitFiles(t, args, docs(listen))
		if err != nil {
			t.Fatal(err)
		}
		guest := got["unbound-guest.conf"]
		for _, want := range []string{
			"# Unbound's config, rendered by chalkos-render unbound from the Resolver of VRF guest.\n",
			"  interface: " + listen + "\n",
			"  dns64-prefix: 64:ff9b::/96\n",
			"remote-control:\n  control-enable: yes\n  control-interface: /run/unbound-guest/unbound.ctl\n",
		} {
			if !strings.Contains(guest, want) {
				t.Errorf("unbound-guest.conf lacks %q:\n%s", want, guest)
			}
		}
		if !strings.Contains(got["unbound.conf"], "  control-interface: /run/unbound/unbound.ctl\n") || len(got) != 2 {
			t.Errorf("files = %v", got)
		}
	}
}

// A peer in a VRF has its ports opened on its VRF's links by the VRF's device and sdifname: the
// input hook sees a packet from a VRF's link with the device as iifname and the link as
// sdifname, so links of other VRFs that the peer's globs match stay closed. A peer of an address
// in a VRF is opened by the VRF's device and its address, as is a peer of a listen range by its
// range, and a peer of the VRF named default by iifname as one of no VRF.
func TestVRFPeerOpenings(t *testing.T) {
	tenant, def, addr := "tenant", derive.DefaultVRF, netip.MustParseAddr("192.0.2.9")
	openings, err := bgpOpenings([]derive.Named[derive.PeerSpec]{
		{Name: "leaves", Spec: derive.PeerSpec{Interfaces: []string{"swp*"}, VRF: &tenant}},
		{Name: "spines", Spec: derive.PeerSpec{Interfaces: []string{"eth*"}, VRF: &def}},
		{Name: "far", Spec: derive.PeerSpec{Address: &addr, VRF: &tenant, BFD: true}},
		{Name: "hosts", Spec: derive.PeerSpec{Listen: &derive.PeerListen{Range: netip.MustParsePrefix("2001:db8:1::/64")}, VRF: &tenant}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range openings {
		got = append(got, o.lines()...)
	}
	want := []string{
		`iifname "tenant" meta sdifname { "swp*" } tcp dport { 179 } accept comment "BGPPeer leaves"`,
		`iifname { "eth*" } tcp dport { 179 } accept comment "BGPPeer spines"`,
		`iifname "tenant" ip saddr { 192.0.2.9/32 } tcp dport { 179 } accept comment "BGPPeer far"`,
		`iifname "tenant" ip saddr { 192.0.2.9/32 } udp dport { 3784, 3785, 4784 } accept comment "BGPPeer far"`,
		`iifname "tenant" ip6 saddr { 2001:db8:1::/64 } tcp dport { 179 } accept comment "BGPPeer hosts"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// FRR's config refuses a prepend that BGP instances of two AS numbers share. It guards, with an
// internal error, values that would add to the config and a peer whose VRF no instance serves.
// The validator router refuses a peer's sessions where the links networkd puts in each VRF decide.
func TestFRRVRFRefusals(t *testing.T) {
	timers := `"multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false}, "bfd": {"receiveMs": 300, "transmitMs": 300, "multiplier": 3}`
	bgp := func(name string, asn int, more string) any {
		return derivetest.RouterDoc(t, "BGP", name, fmt.Sprintf(`{"asn": %d, "routerID": "192.0.2.1", %s%s}`, asn, timers, more))
	}
	peer := func(name, more string) any {
		return derivetest.RouterDoc(t, "BGPPeer", name, `{"remoteAS": "external", "families": {"ipv6": {}}, `+more+`}`)
	}
	tenant := derivetest.RouterDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"]}`)
	eth2 := derivetest.Doc(t, "Network", "20-eth2", `{"match": {"name": "eth2"}}`)
	prepend := derivetest.RouterDoc(t, "RoutingPolicy", "p", `{"terms": [{"then": {"action": "accept", "prepend": 1}}], "default": "reject"}`)
	withTenant := func(docs map[string][]any) map[string][]any {
		docs["VRF"], docs["Network"] = []any{tenant}, []any{eth2}
		return docs
	}
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a prepend for two AS numbers": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, `, "networks": [{"prefix": "2001:db8:1::/48", "policy": "p"}]`),
			bgp("tenant", 64513, `, "vrf": "tenant", "networks": [{"prefix": "2001:db8:2::/48", "policy": "p"}]`)}, "RoutingPolicy": {prepend}}),
			"RoutingPolicy/p: spec.terms[0].then.prepend: RoutingPolicy p prepends the AS number, and BGP instances of AS 64512 and 64513 use it; give each AS a policy of its own"},
		// Guards of what frr.conf writes unquoted, and of a peer without its instance's block.
		"a VRF that adds a command": {withTenant(map[string][]any{"BGP": {bgp("x", 64512, `, "vrf": "tenant\nrouter bgp 64999"`)}}),
			`internal error: BGP x: the input holds no VRF "tenant\nrouter bgp 64999"`},
		"a peer in a VRF without BGP": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, "")}, "BGPPeer": {peer("leaves", `"vrf": "tenant", "interface": "eth2"`)}}),
			"internal error: BGPPeer leaves runs in the VRF tenant, which no BGP document serves"},
		"an import of a VRF without BGP": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, `, "import": {"ipv6": {"vrfs": ["tenant"]}}`)}}),
			`internal error: BGP bgp: import.ipv6 names the VRF "tenant", which no BGP document serves`},
		"an import that adds a command": {map[string][]any{"BGP": {bgp("bgp", 64512, `, "import": {"ipv6": {"vrfs": ["x\n neighbor 192.0.2.9 remote-as 64999"]}}`)}},
			`internal error: BGP bgp: import.ipv6 names the VRF "x\n neighbor 192.0.2.9 remote-as 64999", which no BGP document serves`},
		"an import's policy that adds a command": {withTenant(map[string][]any{"BGP": {bgp("bgp", 64512, `, "import": {"ipv6": {"vrfs": ["tenant"], "policy": "x\n neighbor 192.0.2.9 remote-as 64999"}}`), bgp("tenant", 64512, `, "vrf": "tenant"`)}}),
			`internal error: BGP bgp: import.ipv6: the input holds no RoutingPolicy "x\n neighbor 192.0.2.9 remote-as 64999"`},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"frr"}, c.docs, c.want) })
	}
}

// The default VRF's table holds no unreachable default route, so its instance announces a
// default-route network only while the table holds a default route.
func TestFRRDefaultRouteNetwork(t *testing.T) {
	timers := `"multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false}, "bfd": {"receiveMs": 300, "transmitMs": 300, "multiplier": 3}`
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": map[string][]any{
		"BGP": {derivetest.RouterDoc(t, "BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", `+timers+`, "networks": [{"prefix": "0.0.0.0/0"}, {"prefix": "::/0"}]}`)},
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

// A peer family's defaultOriginateWhen sends the default route through the route-map do-<list>,
// one per list, and its instance redistributes the kernel's routes and the links' prefixes of the
// family that its peers' lists match, through track-<vrf>-<family>: blackhole routes, such as a
// VRF table's unreachable default, never, the others with no-advertise and the tracked large
// community. An import from a VRF that tracks routes of its family denies that community first,
// then calls the import's policy. An instance or family that tracks nothing, and an import from
// one, render as without defaultOriginateWhen.
func TestFRRDefaultOriginateWhen(t *testing.T) {
	timers := `"multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false}, "bfd": {"receiveMs": 300, "transmitMs": 300, "multiplier": 3}`
	list := func(name, prefix string) any {
		return derivetest.RouterDoc(t, "PrefixList", name, `{"family": "ipv6", "entries": [{"prefix": "`+prefix+`", "action": "permit"}]}`)
	}
	peers := func(leaf, spine, spine2 string) []any {
		return []any{
			derivetest.RouterDoc(t, "BGPPeer", "leaf", `{"vrf": "tenant", "interface": "eth2", "remoteAS": "external", "families": {"ipv4": {"defaultOriginate": true}, "ipv6": {"defaultOriginate": true`+leaf+`}}}`),
			derivetest.RouterDoc(t, "BGPPeer", "spine", `{"address": "2001:db8::9", "remoteAS": "external", "families": {"ipv6": {"defaultOriginate": true`+spine+`}}}`),
			derivetest.RouterDoc(t, "BGPPeer", "spine2", `{"address": "2001:db8::10", "remoteAS": "external", "families": {"ipv6": {"defaultOriginate": true`+spine2+`}}}`),
		}
	}
	docs := map[string][]any{
		"BGP": {
			derivetest.RouterDoc(t, "BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", `+timers+`, "import": {"ipv4": {"vrfs": ["tenant"]}, "ipv6": {"vrfs": ["tenant"], "policy": "from-tenant"}}}`),
			derivetest.RouterDoc(t, "BGP", "tenant", `{"vrf": "tenant", "asn": 64512, "routerID": "192.0.2.1", `+timers+`, "import": {"ipv6": {"vrfs": ["default"]}}}`),
		},
		"VRF":           {derivetest.RouterDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"]}`)},
		"Network":       {derivetest.Doc(t, "Network", "20-eth2", `{"match": {"name": "eth2"}}`)},
		"PrefixList":    {list("tenant", "2001:db8:100::/40"), list("uplink", "::/0"), list("wan", "2001:db8:fff0::/48")},
		"RoutingPolicy": {derivetest.RouterDoc(t, "RoutingPolicy", "from-tenant", `{"terms": [{"match": {"prefixList": "tenant"}, "then": {"action": "accept"}}], "default": "reject"}`)},
		"BGPPeer":       peers(`, "defaultOriginateWhen": "uplink"`, `, "defaultOriginateWhen": "wan"`, `, "defaultOriginateWhen": "uplink"`),
	}
	conf := func() string {
		t.Helper()
		input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
		if err != nil {
			t.Fatal(err)
		}
		return renderedFile(t, []string{"frr"}, string(input), frrFile)
	}
	tracked := conf()
	for _, want := range []string{
		"bgp large-community-list standard chalkos-tracked seq 5 permit 4200000000:0:1\n!\n",
		"route-map accept-all permit 10\nexit\n" +
			"route-map do-uplink permit 10\n match ipv6 address prefix-list pl-uplink\nexit\n" +
			"route-map do-wan permit 10\n match ipv6 address prefix-list pl-wan\nexit\n" +
			"route-map track-default-ipv6 deny 5\n match ipv6 next-hop type blackhole\nexit\n" +
			"route-map track-default-ipv6 permit 10\n match ipv6 address prefix-list pl-uplink\n set community no-advertise additive\n set distance 255\n set large-community 4200000000:0:1 additive\n set weight 0\nexit\n" +
			"route-map track-default-ipv6 permit 20\n match ipv6 address prefix-list pl-wan\n set community no-advertise additive\n set distance 255\n set large-community 4200000000:0:1 additive\n set weight 0\nexit\n" +
			"route-map leak-default-ipv6 deny 5\n match large-community chalkos-tracked\nexit\n" +
			"route-map leak-default-ipv6 permit 10\n call rp-from-tenant\nexit\n" +
			"route-map track-tenant-ipv6 deny 5\n match ipv6 next-hop type blackhole\nexit\n" +
			"route-map track-tenant-ipv6 permit 10\n match ipv6 address prefix-list pl-uplink\n set community no-advertise additive\n set distance 255\n set large-community 4200000000:0:1 additive\n set weight 0\nexit\n" +
			"route-map leak-tenant-ipv6 deny 5\n match large-community chalkos-tracked\nexit\n" +
			"route-map leak-tenant-ipv6 permit 10\nexit\n!\n",
		// The default VRF's IPv4 tracks nothing and imports from a VRF that tracks no IPv4.
		" address-family ipv4 unicast\n  maximum-paths 1\n  maximum-paths ibgp 1\n  import vrf tenant\n exit-address-family\n",
		" address-family ipv6 unicast\n  redistribute kernel route-map track-default-ipv6\n  redistribute connected route-map track-default-ipv6\n  maximum-paths 1\n",
		"  neighbor peer-spine default-originate route-map do-wan\n",
		"  neighbor peer-spine2 default-originate route-map do-uplink\n",
		"  import vrf route-map leak-default-ipv6\n  import vrf tenant\n exit-address-family\n",
		// tenant's IPv4 default stays unconditional.
		" address-family ipv4 unicast\n  maximum-paths 1\n  maximum-paths ibgp 1\n  neighbor peer-leaf activate\n  neighbor peer-leaf default-originate\n  neighbor peer-leaf route-map accept-all in\n",
		"  redistribute kernel route-map track-tenant-ipv6\n  redistribute connected route-map track-tenant-ipv6\n",
		"  neighbor peer-leaf default-originate route-map do-uplink\n",
		"  import vrf route-map leak-tenant-ipv6\n  import vrf default\n",
	} {
		if !strings.Contains(tracked, want) {
			t.Errorf("frr.conf lacks %q:\n%s", want, tracked)
		}
	}
	if n := strings.Count(tracked, "route-map do-uplink permit 10\n"); n != 1 {
		t.Errorf("frr.conf renders do-uplink %d times:\n%s", n, tracked)
	}
	// Without defaultOriginateWhen nothing of the tracking remains, and the import keeps its policy.
	docs["BGPPeer"] = peers("", "", "")
	plain := conf()
	for _, gone := range []string{"chalkos-tracked", "route-map do-", "track-", "leak-", "redistribute", "default-originate route-map"} {
		if strings.Contains(plain, gone) {
			t.Errorf("frr.conf without defaultOriginateWhen holds %q:\n%s", gone, plain)
		}
	}
	if !strings.Contains(plain, "  import vrf route-map rp-from-tenant\n  import vrf tenant\n") {
		t.Errorf("frr.conf without defaultOriginateWhen lost the import's policy:\n%s", plain)
	}
	// tenant alone tracks: the default VRF's import from tenant denies the community, tenant's
	// import from the default VRF stays bare.
	docs["BGPPeer"] = peers(`, "defaultOriginateWhen": "uplink"`, "", "")
	one := conf()
	if !strings.Contains(one, "  import vrf route-map leak-default-ipv6\n  import vrf tenant\n") ||
		strings.Contains(one, "leak-tenant-ipv6") || !strings.Contains(one, " exit-address-family\n !\n address-family ipv6 unicast\n  redistribute kernel route-map track-tenant-ipv6\n") ||
		strings.Contains(one, "track-default-ipv6") {
		t.Errorf("frr.conf with tenant's tracking alone:\n%s", one)
	}
	// tenant's IPv4 tracks too: its routes of IPv4 go through track-tenant-ipv4 and the default
	// VRF's IPv4 import from tenant through leak-default-ipv4. The image's check parses the
	// config with vtysh, as no example tracks IPv4.
	docs["PrefixList"] = append(docs["PrefixList"], derivetest.RouterDoc(t, "PrefixList", "uplink4", `{"family": "ipv4", "entries": [{"prefix": "0.0.0.0/0", "action": "permit"}]}`))
	docs["BGPPeer"] = []any{derivetest.RouterDoc(t, "BGPPeer", "leaf", `{"vrf": "tenant", "interface": "eth2", "remoteAS": "external", "families": {"ipv4": {"defaultOriginate": true, "defaultOriginateWhen": "uplink4"}, "ipv6": {"defaultOriginate": true, "defaultOriginateWhen": "uplink"}}}`)}
	both := conf()
	for _, want := range []string{
		"ip prefix-list pl-uplink4 seq 5 permit 0.0.0.0/0\n",
		"route-map do-uplink4 permit 10\n match ip address prefix-list pl-uplink4\nexit\n",
		"route-map leak-default-ipv4 deny 5\n match large-community chalkos-tracked\nexit\nroute-map leak-default-ipv4 permit 10\nexit\n",
		"route-map track-tenant-ipv4 deny 5\n match ip next-hop type blackhole\nexit\n" +
			"route-map track-tenant-ipv4 permit 10\n match ip address prefix-list pl-uplink4\n set community no-advertise additive\n set distance 255\n set large-community 4200000000:0:1 additive\n set weight 0\nexit\n",
		"  import vrf route-map leak-default-ipv4\n  import vrf tenant\n",
		"  redistribute kernel route-map track-tenant-ipv4\n  redistribute connected route-map track-tenant-ipv4\n",
		"  neighbor peer-leaf default-originate route-map do-uplink4\n",
	} {
		if !strings.Contains(both, want) {
			t.Errorf("frr.conf with both families tracked lacks %q:\n%s", want, both)
		}
	}
	golden(t, "tracked.frr", both)
}

// A peer's interface is in the VRF of the first Network document networkd picks for it, which
// may match it by a glob, and the peer's session runs there.
func TestFRRVRFInterfaceByGlob(t *testing.T) {
	timers := `"multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false}, "bfd": {"receiveMs": 300, "transmitMs": 300, "multiplier": 3}`
	docs := map[string][]any{
		"BGP": {
			derivetest.RouterDoc(t, "BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", `+timers+`}`),
			derivetest.RouterDoc(t, "BGP", "tenant", `{"vrf": "tenant", "asn": 64512, "routerID": "192.0.2.1", `+timers+`}`),
		},
		"VRF":     {derivetest.RouterDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["swp*"]}`)},
		"Network": {derivetest.Doc(t, "Network", "30-swp", `{"match": {"name": "swp*"}}`)},
	}
	peer := func(more string) []any {
		return []any{derivetest.RouterDoc(t, "BGPPeer", "leaf", `{"interface": "swp1", "remoteAS": "external", "families": {"ipv6": {}}`+more+`}`)}
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
	// A glob of a default-VRF peer expands to the links of no VRF alone.
	docs["Network"] = append(docs["Network"],
		derivetest.Doc(t, "Network", "20-swp1", `{"match": {"name": "swp1"}}`),
		derivetest.Doc(t, "Network", "40-sw0", `{"match": {"name": "sw0"}}`))
	docs["BGPPeer"] = []any{derivetest.RouterDoc(t, "BGPPeer", "leaves", `{"interfaces": ["sw*"], "remoteAS": "external", "families": {"ipv6": {}}}`)}
	input, err = json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	conf = renderedFile(t, []string{"frr"}, string(input), frrFile)
	if !strings.Contains(conf, " neighbor sw0 interface peer-group peer-leaves\n") || strings.Contains(conf, "neighbor swp1 ") {
		t.Errorf("frr.conf runs sw*'s sessions elsewhere than on sw0:\n%s", conf)
	}
}

// A prepend repeats the AS of the instance that uses its policy, the default VRF's or another's,
// and each instance with a BFD peer has a BFD profile of its own timers.
func TestFRRVRFPrependAndBFD(t *testing.T) {
	timers := func(rx int) string {
		return fmt.Sprintf(`"multipath": {"ebgp": 1, "ibgp": 1, "asPathRelax": false}, "bfd": {"receiveMs": %d, "transmitMs": 300, "multiplier": 3}`, rx)
	}
	docs := map[string][]any{
		"BGP": {
			derivetest.RouterDoc(t, "BGP", "bgp", `{"asn": 64512, "routerID": "192.0.2.1", `+timers(300)+`}`),
			derivetest.RouterDoc(t, "BGP", "tenant", `{"vrf": "tenant", "asn": 64513, "routerID": "192.0.2.1", "networks": [{"prefix": "2001:db8:2::/48", "policy": "p"}], `+timers(500)+`}`),
		},
		"VRF":           {derivetest.RouterDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"]}`)},
		"Network":       {derivetest.Doc(t, "Network", "20-eth2", `{"match": {"name": "eth2"}}`)},
		"RoutingPolicy": {derivetest.RouterDoc(t, "RoutingPolicy", "p", `{"terms": [{"then": {"action": "accept", "prepend": 1}}], "default": "reject"}`)},
		"BGPPeer": {
			derivetest.RouterDoc(t, "BGPPeer", "spine", `{"address": "2001:db8::9", "bfd": true, "remoteAS": "external", "families": {"ipv6": {}}}`),
			derivetest.RouterDoc(t, "BGPPeer", "leaf", `{"vrf": "tenant", "interface": "eth2", "bfd": true, "remoteAS": "external", "families": {"ipv6": {}}}`),
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

// vrfZoneDocs are an edge's documents: derivetest.VRFZoneDocs with the edge's firewall policies and
// NAT rules.
func vrfZoneDocs(t *testing.T) map[string][]any {
	t.Helper()
	policy := func(name, from, to, verdict string) any {
		return derivetest.RouterDoc(t, "FirewallPolicy", name, `{"from": "`+from+`", "to": "`+to+`", "default": "`+verdict+`", "rules": []}`)
	}
	docs := derivetest.VRFZoneDocs(t)
	docs["FirewallPolicy"] = []any{
		policy("guest-lan-local", "guest-lan", "local", "accept"),
		policy("guest-out", "guest-lan", "guest-wan", "accept"),
		policy("internet-local", "internet", "local", "drop"),
		policy("lan-local", "lan", "local", "accept"),
		policy("tenant-internet", "tenant", "internet", "accept"),
	}
	docs["NATRule"] = []any{
		derivetest.RouterDoc(t, "NATRule", "guest-web", `{"type": "dnat", "family": "ipv4", "in": {"zone": "guest-wan", "interface": null}, "out": null, "match": {"source": [], "destination": [], "protocol": "tcp", "destinationPorts": [8080]}, "to": {"address": "192.0.2.10", "port": 80}}`),
		derivetest.RouterDoc(t, "NATRule", "internet-out", `{"type": "masquerade", "family": "ipv4", "out": {"zone": "internet", "interface": null}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`),
		derivetest.RouterDoc(t, "NATRule", "tenant-web", `{"type": "dnat", "family": "ipv4", "in": {"zone": "tenant", "interface": null}, "out": null, "match": {"source": [], "destination": [], "protocol": "tcp", "destinationPorts": [80]}, "to": {"address": "198.51.100.10", "port": null}}`),
	}
	return docs
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
	docs["FirewallZone"][2] = derivetest.RouterDoc(t, "FirewallZone", "internet", `{"interfaces": ["eth5", "eth1"], "vrfs": ["internet"]}`)
	docs["FirewallZone"][3] = derivetest.RouterDoc(t, "FirewallZone", "lan", `{"interfaces": ["vlan*"], "vrfs": []}`)
	docs["FirewallPolicy"] = append(docs["FirewallPolicy"],
		derivetest.RouterDoc(t, "FirewallPolicy", "lan-internet", `{"from": "lan", "to": "internet", "default": "accept", "rules": []}`))
	docs["NATRule"] = []any{
		derivetest.RouterDoc(t, "NATRule", "internet-out", `{"type": "snat", "family": "ipv4", "out": {"zone": "internet", "interface": null}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": {"address": "203.0.113.1", "port": null}}`),
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
	docs["FirewallZone"] = []any{derivetest.RouterDoc(t, "FirewallZone", "all", `{"interfaces": ["eth*"], "vrfs": []}`)}
	docs["FirewallPolicy"] = []any{derivetest.RouterDoc(t, "FirewallPolicy", "all-local", `{"from": "all", "to": "local", "default": "accept", "rules": []}`)}
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
	docs["Network"] = append(docs["Network"], derivetest.Doc(t, "Network", "50-transit", `{"match": {"name": "transit0"}}`))
	for _, i := range []string{"*", "t*"} {
		docs["NATRule"] = []any{derivetest.RouterDoc(t, "NATRule", "n", `{"type": "masquerade", "family": "ipv4", "out": {"zone": null, "interface": "`+i+`"}, "in": null, "match": {"source": [], "destination": [], "protocol": null, "destinationPorts": []}, "to": null}`)}
		want := "\t\tmeta nfproto ipv4 oifname { \"" + i + "\" } masquerade\n"
		if text := vrfRuleset(t, docs); !strings.Contains(text, want) {
			t.Errorf("the ruleset lacks\n%s\nin\n%s", want, text)
		}
	}
}

// A VRF with a conntrack zone puts the connections through its links in its zone before conntrack
// sees them: in prerouting by the link a packet comes in on, whose pass decides its zone, and in
// output by the link the router sends it through. Without zones the ruleset has neither chain.
func TestConntrackZones(t *testing.T) {
	docs := vrfZoneDocs(t)
	docs["VRF"][0] = derivetest.RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"], "conntrackZone": 1}`)
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
	docs["VRF"][0] = derivetest.RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"], "conntrackZone": 1}`)
	docs["VRF"] = append(docs["VRF"],
		derivetest.RouterDoc(t, "VRF", "lab", `{"table": 400, "interfaces": [], "conntrackZone": 3}`),
		derivetest.RouterDoc(t, "VRF", "spare", `{"table": 500, "interfaces": [], "conntrackZone": 2}`))
	docs["Network"] = append(docs["Network"], derivetest.Doc(t, "Network", "50-lab", `{"match": {"name": "dummy*"}, "network": {"vrf": "lab"}}`))
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
	docs["VRF"] = append(docs["VRF"], derivetest.RouterDoc(t, "VRF", "spare", `{"table": 500, "interfaces": [], "conntrackZone": 2}`))
	if text := vrfRuleset(t, docs); strings.Contains(text, "ct-prerouting") || strings.Contains(text, "ct-output") {
		t.Errorf("a ruleset whose zoned VRF has no link has conntrack chains:\n%s", text)
	}
}

// ptr is a pointer to s.
func ptr(s string) *string { return &s }

// addrs are the addresses l.
func addrs(l ...string) []netip.Addr {
	out := make([]netip.Addr, len(l))
	for i, s := range l {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

// ranges are the address ranges l.
func ranges(l ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(l))
	for i, s := range l {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}
