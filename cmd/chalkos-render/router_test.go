package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
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
	// routerExamples are the examples of the outputs router-sysctl and router-nftables.
	routerExamples = "../../modules/node/examples/router"
)

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
	for _, kind := range []string{"Router", "FirewallZone", "FirewallPolicy", "AddressSet", "NATRule"} {
		byKind[kind] = []any{}
	}
	slices.SortStableFunc(docs, func(a, b config.Document) int { return cmp.Compare(a.Name, b.Name) })
	for _, d := range docs {
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

// Each example of the router outputs renders its golden sysctl file and ruleset: no documents, a
// leaf that only routes, a fabric edge and a LAN router.
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
