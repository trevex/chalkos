package vxlan

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

func TestDestinationOf(t *testing.T) {
	ip := netip.MustParseAddr("10.0.0.11")
	for _, tc := range []struct {
		link nodeip.Link
		any  bool
	}{
		{nodeip.Link{Name: "eth0"}, false},
		{nodeip.Link{Name: "vlan5", Kind: "vlan"}, false},
		{nodeip.Link{Name: "lo", Loopback: true}, true},
		{nodeip.Link{Name: "bgp0", Kind: "dummy"}, true},
	} {
		if d := DestinationOf(nodeip.Address{Interface: tc.link.Name, IP: ip, Link: tc.link}); d != (Destination{IP: ip, AnyInterface: tc.any}) {
			t.Errorf("%s: %+v", tc.link.Name, d)
		}
	}
}

func TestAcceptRoundTrip(t *testing.T) {
	a := Accept{
		Destinations: []Destination{
			{IP: netip.MustParseAddr("10.0.0.11")},
			{IP: netip.MustParseAddr("fd00::11"), AnyInterface: true},
		},
		Sources: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("fd00::/64")},
	}
	data := a.Marshal()
	if want := "destination 10.0.0.11 interface\ndestination fd00::11 any\nsource 10.0.0.0/24\nsource fd00::/64\n"; string(data) != want {
		t.Errorf("Marshal() = %q, want %q", data, want)
	}
	got, err := parseAccept(data)
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Errorf("parseAccept() = %+v, %v", got, err)
	}
}

// IPv6 addresses written out in full, with an IPv4 address at the end, and a last line without
// a newline are read.
func TestParseAccept(t *testing.T) {
	for content, want := range map[string]string{
		"destination fd00:0:0:0:0:0:0:11 interface\n": "fd00::11",
		"destination ::ffff:10.0.0.11 interface\n":    "::ffff:10.0.0.11",
		"destination 10.0.0.11 any":                   "10.0.0.11",
	} {
		a, err := parseAccept([]byte(content))
		if err != nil || len(a.Destinations) != 1 || a.Destinations[0].IP.String() != want {
			t.Errorf("%q: %+v, %v", content, a, err)
		}
	}
	// A range with host bits is read as its network.
	a, err := parseAccept([]byte("destination 10.0.0.11 any\nsource 10.0.0.1/24\n"))
	if err != nil || fmtPrefixes(a.Sources) != "10.0.0.0/24" {
		t.Errorf("host bits: %+v, %v", a, err)
	}
}

// Anything but a destination line per family, of a bare address and a mode, and source lines of
// a range each, is refused: nft would read it as part of the ruleset, or resolve a name.
func TestParseAcceptRefuses(t *testing.T) {
	v4, v6 := "destination 10.0.0.11 interface\n", "destination fd00::11 interface\n"
	for _, content := range []string{
		"", "\n", "10.0.0.11\n", "destination 10.0.0.11\n", "destination 10.0.0.11 eth0\n",
		"destination 10.0.0.0/8 interface\n", v4 + "destination 10.0.0.12 any\n", v6 + "destination fd00::12 interface\n",
		v4 + "destination fd00::11 interface accept\n", "destination 10.0.0.11 interface accept\n",
		"destination fd00::11 }\n", "destination  10.0.0.11 interface\n", "destination 10.0.0.11 interface \n",
		"Destination 10.0.0.11 interface\n", "source 10.0.0.0/24\n", v4 + "\n", "\n" + v4, "destination ::ffff: interface\n",
		"destination eth0 interface\n", "destination cafe.be interface\n", "destination cafe any\n",
		"destination 10.0.0 interface\n", "destination 10.0.0.11.12 interface\n", "destination 10.0.0.256 interface\n",
		"destination 10.0.0.0x1 interface\n", "destination 10.0..11 interface\n", "destination ::: interface\n",
		v4 + "source 10.0.0.0\n", v4 + "source 10.0.0.0/33\n", v4 + "source fd00::/129\n", v4 + "source 10.0.0.0/024\n",
		v4 + "source 10.0.0.0/\n", v4 + "source cafe/8\n", v4 + "source 10.0.0.0/24 accept\n", v4 + "source 10.0.0.0/24 }\n",
		v4 + "source  10.0.0.0/24\n", v4 + "source 10.0.0.0/8/8\n", v4 + "source 10.0.0.256/24\n",
		v4 + "source fe80::/64%v0\n", v4 + "source 10.0.0.0/24\r\n",
		"destination a:b interface\n", "destination cafe:babe interface\n", "destination 1::2::3 interface\n",
		"destination 12345:: interface\n", "destination fe80::1%v0 interface\n", "destination 1:2:3:4:5:6:7:8:9 interface\n",
		"destination 1:2:3:4:5:6:7::8 interface\n", "destination :1::2 interface\n", "destination 1::2: interface\n",
		"destination ::10.0.0 interface\n", "destination fd00:10.0.0.11 interface\n", "destination ::10.0.0.11:1 interface\n",
		"destination 10.0.0.011 interface\n", v4 + "source a:b/64\n", v4 + "source 1::2::3/64\n",
	} {
		if a, err := parseAccept([]byte(content)); err == nil {
			t.Errorf("accepted %q: %+v", content, a)
		}
	}
}

func TestRuleset(t *testing.T) {
	accept := Accept{
		Destinations: []Destination{
			{IP: netip.MustParseAddr("10.0.0.11")},
			{IP: netip.MustParseAddr("fd00::11"), AnyInterface: true},
		},
	}
	forward := []string{"udp dport 8472 drop"}
	want := `table inet chalkos-vxlan
delete table inet chalkos-vxlan
table inet chalkos-vxlan {
  chain input {
    type filter hook input priority filter - 1; policy accept;
    meta mark & 0x01000000 == 0x01000000 meta mark set meta mark ^ 0x01000000
    ip daddr 10.0.0.11 udp dport 8472 fib daddr . iif type local meta mark set meta mark | 0x01000000
    ip6 daddr fd00::11 udp dport 8472 iifname != { "cni*", "flannel*", "kube-*", "veth*" } meta mark set meta mark | 0x01000000
  }
  chain clear {
    type filter hook input priority filter + 1; policy accept;
    meta mark & 0x01000000 == 0x01000000 meta mark set meta mark ^ 0x01000000
  }
  chain forward {
    type filter hook forward priority filter; policy accept;
    udp dport 8472 drop
  }
}
`
	if got := ruleset(accept, forward); got != want {
		t.Errorf("ruleset() =\n%s\nwant\n%s", got, want)
	}

	// With source ranges VXLAN comes from them alone; a family without one takes none. Without
	// forward rules there is no forward chain.
	accept.Sources = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("192.168.0.0/16")}
	got := ruleset(accept, nil)
	if !strings.Contains(got, "    ip saddr { 10.0.0.0/24, 192.168.0.0/16 } ip daddr 10.0.0.11 udp dport 8472 fib daddr . iif type local meta mark set meta mark | 0x01000000\n") ||
		strings.Contains(got, "ip6 daddr") || strings.Contains(got, "chain forward") {
		t.Errorf("with IPv4 sources:\n%s", got)
	}
	// Without destinations the input chain only clears the mark.
	if got := ruleset(Accept{}, nil); strings.Contains(got, "meta mark set meta mark |") {
		t.Errorf("without destinations:\n%s", got)
	}
}

func TestForward(t *testing.T) {
	dual := func(sources ...string) kubernetes.Cluster {
		c := kubernetes.Cluster{
			IPFamilies: []nodeip.Family{nodeip.IPv4, nodeip.IPv6},
			PodCIDRs: kubernetes.ByFamily[netip.Prefix]{
				IPv4: netip.MustParsePrefix("10.244.0.0/16"),
				IPv6: netip.MustParsePrefix("fd00:10:244::/56"),
			},
			Flannel: &kubernetes.Flannel{},
		}
		for _, s := range sources {
			c.Flannel.VXLANSourceSubnets = append(c.Flannel.VXLANSourceSubnets, netip.MustParsePrefix(s))
		}
		return c
	}
	rules, err := forwardRules(dual("10.0.0.0/24", "fd00::/64", "192.168.0.0/16"))
	want := []string{
		"ip saddr { 10.244.0.0/16 } ip daddr { 10.0.0.0/24, 192.168.0.0/16 } udp dport 8472 drop",
		"ip6 saddr { fd00:10:244::/56 } ip6 daddr { fd00::/64 } udp dport 8472 drop",
	}
	if err != nil || !reflect.DeepEqual(rules, want) {
		t.Errorf("dual stack: %q, %v", rules, err)
	}
	// A family without source ranges drops nothing, as without flannel or without any.
	if rules, err := forwardRules(dual("10.0.0.0/24")); err != nil || !reflect.DeepEqual(rules, []string{"ip saddr { 10.244.0.0/16 } ip daddr { 10.0.0.0/24 } udp dport 8472 drop"}) {
		t.Errorf("IPv4 sources: %q, %v", rules, err)
	}
	for name, c := range map[string]kubernetes.Cluster{"no sources": dual(), "no flannel": {PodCIDRs: dual().PodCIDRs}} {
		if rules, err := forwardRules(c); err != nil || rules != nil {
			t.Errorf("%s: %q, %v", name, rules, err)
		}
	}
	// Sources of a family the cluster lacks are skipped; without ipFamilies it is IPv4's.
	c := dual("10.0.0.0/24", "fd00::/64")
	c.IPFamilies = nil
	if rules, err := forwardRules(c); err != nil || !reflect.DeepEqual(rules, []string{"ip saddr { 10.244.0.0/16 } ip daddr { 10.0.0.0/24 } udp dport 8472 drop"}) {
		t.Errorf("IPv4 cluster: %q, %v", rules, err)
	}
	// A family without a pod range of its own is an error.
	for name, pods := range map[string]kubernetes.ByFamily[netip.Prefix]{
		"no IPv6 range":      {IPv4: netip.MustParsePrefix("10.244.0.0/16")},
		"IPv6 range as IPv4": {IPv4: netip.MustParsePrefix("fd00::/56"), IPv6: netip.MustParsePrefix("fd00::/56")},
	} {
		c := dual("10.0.0.0/24", "fd00::/64")
		c.PodCIDRs = pods
		if rules, err := forwardRules(c); err == nil {
			t.Errorf("%s: %q", name, rules)
		}
	}
}

// nft records what a Rule runs: each ruleset given, and each other command; it refuses a ruleset
// while refuse is above zero, counting down, and every command once fail is set.
type nft struct {
	rulesets, commands []string
	refuse             int
	fail               bool
}

func (n *nft) run(input string, args ...string) error {
	if input != "" {
		if strings.Join(args, " ") != "-f -" {
			return fmt.Errorf("a ruleset with the arguments %q", args)
		}
		n.rulesets = append(n.rulesets, input)
		if n.refuse > 0 {
			n.refuse--
			return errors.New("exit status 1")
		}
		return nil
	}
	n.commands = append(n.commands, strings.Join(args, " "))
	if n.fail {
		return errors.New("exit status 1")
	}
	return nil
}

func testRule(t *testing.T) (Rule, *nft, string) {
	dir := t.TempDir()
	n := &nft{}
	r := Rule{LockFile: filepath.Join(dir, "lock"), ClusterFile: filepath.Join(dir, "cluster.json"), NFT: n.run}
	writeFile(t, r.ClusterFile, `{"ipFamilies":["ipv4"],"podCIDRs":{"ipv4":"10.244.0.0/16"},"flannel":{"vxlanSourceSubnets":["10.0.0.0/24"]}}`)
	file := filepath.Join(dir, "vxlan")
	writeFile(t, file, "destination 10.0.0.11 interface\n")
	return r, n, file
}

const (
	marks       = "ip daddr 10.0.0.11 udp dport 8472 fib daddr . iif type local meta mark set"
	dropsPods   = "ip saddr { 10.244.0.0/16 } ip daddr { 10.0.0.0/24 } udp dport 8472 drop"
	dropsAll    = "    udp dport 8472 drop\n"
	listTable   = "list table inet chalkos-vxlan"
	deleteTable = "delete table inet chalkos-vxlan"
)

func TestApply(t *testing.T) {
	r, n, file := testRule(t)
	if err := r.Apply(file); err != nil {
		t.Fatal(err)
	}
	if len(n.rulesets) != 1 || !strings.Contains(n.rulesets[0], marks) || !strings.Contains(n.rulesets[0], dropsPods) ||
		!reflect.DeepEqual(n.commands, []string{listTable}) {
		t.Errorf("rulesets %q, commands %q", n.rulesets, n.commands)
	}

	// Without the file, or without one given, the input chain is emptied; the forward chain
	// stands.
	for _, f := range []string{filepath.Join(t.TempDir(), "missing"), ""} {
		r, n, _ := testRule(t)
		if err := r.Apply(f); err != nil {
			t.Fatalf("%q: %v", f, err)
		}
		if len(n.rulesets) != 1 || strings.Contains(n.rulesets[0], "meta mark set meta mark |") || !strings.Contains(n.rulesets[0], dropsPods) {
			t.Errorf("%q: rulesets %q", f, n.rulesets)
		}
	}
}

// Ranges that cannot be read drop all VXLAN the node forwards, while the node still takes VXLAN.
func TestApplyWithoutRanges(t *testing.T) {
	for name, cluster := range map[string]string{
		"missing":           "",
		"not JSON":          "{",
		"no pod range":      `{"ipFamilies":["ipv4","ipv6"],"podCIDRs":{"ipv4":"10.244.0.0/16"}}`,
		"not a pod range":   `{"podCIDRs":{"ipv4":"10.244.0.0/16 10.0.0.0/8"}}`,
		"not a source":      `{"podCIDRs":{"ipv4":"10.244.0.0/16"},"flannel":{"vxlanSourceSubnets":["cafe.be/8"]}}`,
		"a source ruleset":  `{"podCIDRs":{"ipv4":"10.244.0.0/16"},"flannel":{"vxlanSourceSubnets":["10.0.0.0/24\nflush 10.0.0.0/8"]}}`,
		"pod range is null": `{"podCIDRs":{"ipv4":null}}`,
	} {
		r, n, file := testRule(t)
		if cluster == "" {
			os.Remove(r.ClusterFile)
		} else {
			writeFile(t, r.ClusterFile, cluster)
		}
		if err := r.Apply(file); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(n.rulesets) != 1 || !strings.Contains(n.rulesets[0], "ip daddr 10.0.0.11 udp dport 8472") || !strings.Contains(n.rulesets[0], dropsAll) {
			t.Errorf("%s: rulesets %q", name, n.rulesets)
		}
	}
	// Without flannel nothing is dropped.
	r, n, file := testRule(t)
	writeFile(t, r.ClusterFile, `{"podCIDRs":{"ipv4":"10.244.0.0/16"}}`)
	if err := r.Apply(file); err != nil || len(n.rulesets) != 1 || strings.Contains(n.rulesets[0], "chain forward") {
		t.Errorf("without flannel: %v, %q", err, n.rulesets)
	}
}

// A file that holds anything else, or cannot be read, empties the input chain and fails; the
// forward chain stands.
func TestApplyRefusesFile(t *testing.T) {
	r, n, file := testRule(t)
	writeFile(t, file, "destination 10.0.0.11 eth0\n")
	if err := r.Apply(file); err == nil || !strings.Contains(err.Error(), "does not hold the node's addresses") {
		t.Errorf("err = %v", err)
	}
	if len(n.rulesets) != 1 || strings.Contains(n.rulesets[0], "meta mark set meta mark |") || !strings.Contains(n.rulesets[0], dropsPods) ||
		!reflect.DeepEqual(n.commands, []string{listTable}) {
		t.Errorf("rulesets %q, commands %q", n.rulesets, n.commands)
	}

	r, n, file = testRule(t)
	os.Remove(file)
	if err := os.Mkdir(file, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(file); err == nil {
		t.Error("read a directory")
	}
	if len(n.rulesets) != 1 || strings.Contains(n.rulesets[0], "meta mark set meta mark |") || !strings.Contains(n.rulesets[0], dropsPods) {
		t.Errorf("rulesets %q", n.rulesets)
	}
}

// A lock that cannot be taken empties the table, dropping all forwarded VXLAN, and fails.
func TestApplyWithoutLock(t *testing.T) {
	r, n, file := testRule(t)
	if err := os.Mkdir(r.LockFile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(file); err == nil {
		t.Error("filled the table without the lock")
	}
	if len(n.rulesets) != 1 || strings.Contains(n.rulesets[0], "meta mark set meta mark |") || !strings.Contains(n.rulesets[0], dropsAll) {
		t.Errorf("rulesets %q", n.rulesets)
	}
}

// A ruleset nft refuses changes nothing, so the input chain is emptied and forwarded VXLAN
// dropped; should nft refuse that too, the table goes.
func TestApplyRefusedByNFT(t *testing.T) {
	r, n, file := testRule(t)
	n.refuse = 1
	if err := r.Apply(file); err == nil {
		t.Error("succeeded with a refused ruleset")
	}
	if len(n.rulesets) != 2 || strings.Contains(n.rulesets[1], "meta mark set meta mark |") || !strings.Contains(n.rulesets[1], dropsAll) ||
		!reflect.DeepEqual(n.commands, []string{listTable}) {
		t.Errorf("rulesets %q, commands %q", n.rulesets, n.commands)
	}

	r, n, file = testRule(t)
	n.refuse = 2
	if err := r.Apply(file); err == nil {
		t.Error("succeeded with refused rulesets")
	}
	if len(n.rulesets) != 2 || !reflect.DeepEqual(n.commands, []string{deleteTable, listTable}) {
		t.Errorf("rulesets %q, commands %q", n.rulesets, n.commands)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fmtPrefixes(ps []netip.Prefix) string { return join(ps) }
