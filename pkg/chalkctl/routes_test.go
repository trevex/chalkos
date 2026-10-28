package chalkctl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// routerNode serves n1 as a router whose FRR answers with the recordings of package router's
// tests, and whose kernel holds the default route and an edge's address that BGP installed over
// two links, to the recordings' next hops, and a blackhole. rules answer further commands, such as
// jool's and unbound-control's, before the recordings; resolver tells whether the node has
// Unbound's config.
func routerNode(t *testing.T, ta *testApp, resolver bool, rules ...tooltest.Rule) string {
	t.Helper()
	current, _ := json.Marshal(ta.identity(t))
	s, _ := installedNode(t, ta, current)
	s.Layers = layers.Of(layers.Router)
	frr := func(name string) string {
		data, err := os.ReadFile("../router/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	r := &tooltest.Runner{Rules: append(rules,
		tooltest.Rule{Prefix: "vtysh -c show bgp neighbors json", Out: frr("neighbors.json")},
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv4 unicast detail json", Out: frr("ipv4-detail.json")},
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv6 unicast detail json", Out: frr("ipv6-detail.json")},
	)}
	conf := filepath.Join(t.TempDir(), "unbound.conf")
	if resolver {
		if err := os.WriteFile(conf, []byte("server:\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.Router = &chalkd.Router{Run: r, ResolverConfig: conf, KernelRoutes: func() ([]*nodev1.KernelRoute, error) {
		hops := func() []*nodev1.NextHop {
			return []*nodev1.NextHop{
				{Gateway: "fe80::e06b:dcff:fe54:f465", Interface: "eth2", Weight: 1}, {Gateway: "fe80::b817:4cff:fe8b:9cf", Interface: "eth1", Weight: 1}}
		}
		return []*nodev1.KernelRoute{
			{Destination: "::/0", Type: "unicast", Protocol: "bgp", Table: 254, Metric: 20, NextHops: hops()},
			{Destination: "2001:db8:2::/48", Type: "blackhole", Protocol: "boot", Table: 254, Metric: 1024},
			{Destination: "2001:db8:ffff::1/128", Type: "unicast", Protocol: "bgp", Table: 254, Metric: 20, NextHops: hops()},
		}, nil
	}}
	return ta.startNode(t, s)
}

// node routes <node> [<prefix>] prints the kernel's routes and BGP's paths within the prefix.
func TestNodeRoutes(t *testing.T) {
	ta := newTestApp(t)
	addr := routerNode(t, ta, false)
	if err := ta.run(context.Background(), ta.args([]string{"node", "routes", "n1", "::/0"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "DESTINATION           TYPE       PROTOCOL  TABLE  METRIC  NEXT HOPS\n" +
		"::/0                  unicast    bgp       254    20      fe80::e06b:dcff:fe54:f465 on eth2 (weight 1), fe80::b817:4cff:fe8b:9cf on eth1 (weight 1)\n" +
		"2001:db8:2::/48       blackhole  boot      254    1024    -\n" +
		"2001:db8:ffff::1/128  unicast    bgp       254    20      fe80::e06b:dcff:fe54:f465 on eth2 (weight 1), fe80::b817:4cff:fe8b:9cf on eth1 (weight 1)\n" +
		"\n" +
		"BGP PREFIX            BEST  AS PATH  PEER   NEXT HOPS                  LOCAL PREF  MED  COMMUNITIES\n" +
		"::/0                  *     64512    eth2   fe80::e06b:dcff:fe54:f465  200         0    64512:1 64512:1:2\n" +
		"::/0                        64512    eth1   fe80::b817:4cff:fe8b:9cf   200         0    -\n" +
		"2001:db8:2::/48       *     -        local  ::                         -           0    -\n" +
		"2001:db8:ffff::1/128  *     64512    eth2   fe80::e06b:dcff:fe54:f465  200         0    -\n" +
		"2001:db8:ffff::1/128        64512    eth1   fe80::b817:4cff:fe8b:9cf   200         0    -\n"
	if got := ta.stdout.String(); got != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, want)
	}
}

// status prints a router's BGP sessions, the uptime of each established one.
func TestStatusRouter(t *testing.T) {
	ta := newTestApp(t)
	addr := routerNode(t, ta, false)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "bgp:\n" +
		"  PEER     NEIGHBOR       STATE        UPTIME  RECEIVED  SENT  BFD\n" +
		"  fabric   eth1           Established  1h2m3s  1         2     up\n" +
		"  fabric   eth2           Idle         -       0         0     down\n" +
		"  transit  203.0.113.254  Active       -       0         0     -\n" +
		"certificates:\n"
	if got := ta.stdout.String(); !strings.Contains(got, want) {
		t.Errorf("stdout:\n%s\nlacks:\n%s", got, want)
	}
}

// A node of an image without the router layer says so.
func TestNodeRoutesWithoutTheLayer(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.identity(t))
	s, _ := installedNode(t, ta, current)
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"node", "routes", "n1"}, addr))
	if connect.CodeOf(err) != connect.CodeUnimplemented || !strings.Contains(err.Error(), "the router layer is not in this image") {
		t.Errorf("err = %v", err)
	}
}

// status prints a router's NAT64 instances, a line per protocol with its sessions, bindings and
// pool size, and whether its resolver runs.
func TestStatusRouterNAT64AndResolver(t *testing.T) {
	ta := newTestApp(t)
	pool := "0,TCP,192.0.2.8,61001,65535,1059,0\n"
	addr := routerNode(t, ta, true,
		tooltest.Rule{Prefix: "jool instance display", Out: "8fb43ac0,edge,netfilter\n"},
		tooltest.Rule{Prefix: "jool -i edge session display --csv --no-headers --tcp", Out: "TCP,2001:db8:ffff::2,33830,64:ff9b::c633:6402,18080,192.0.2.8,62105,198.51.100.2,18080,0:03:59.041,V6_INIT\n"},
		tooltest.Rule{Prefix: "jool -i edge bib display --csv --no-headers --tcp", Out: "TCP,2001:db8:ffff::2,33830,192.0.2.8,62105,0\n"},
		tooltest.Rule{Prefix: "jool -i edge pool4 display --csv --no-headers --tcp", Out: pool},
		tooltest.Rule{Prefix: "jool -i edge pool4 display --csv --no-headers --udp", Out: pool},
	)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "nat64:\n" +
		"  INSTANCE  PROTOCOL  SESSIONS  BINDINGS  POOL\n" +
		"  edge      tcp       1         1         4535\n" +
		"  edge      udp       0         0         4535\n" +
		"  edge      icmp      0         0         -\n" +
		"resolver: running\n" +
		"certificates:\n"
	if got := ta.stdout.String(); !strings.Contains(got, want) {
		t.Errorf("stdout:\n%s\nlacks:\n%s", got, want)
	}
}

// status says why a router's NAT64 instances are unknown and why its resolver does not run.
func TestStatusRouterNAT64AndResolverDown(t *testing.T) {
	ta := newTestApp(t)
	addr := routerNode(t, ta, true,
		tooltest.Rule{Prefix: "jool instance display", Err: &tool.Error{Command: "jool", Code: 1, Stderr: "Error: Jool's kernel module is not loaded.\n"}},
		tooltest.Rule{Prefix: "unbound-control status", Err: &tool.Error{Command: "unbound-control", Code: 1, Stderr: "error: connect: No such file or directory for /run/unbound/unbound.ctl\n"}},
	)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	got := ta.stdout.String()
	for _, want := range []string{"\nnat64: unknown: ", "Jool's kernel module is not loaded", "\nresolver: not running: ", "No such file or directory"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout:\n%s\nlacks %q", got, want)
		}
	}
}
