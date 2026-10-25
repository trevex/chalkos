package chalkctl

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// routerNode serves n1 as a router whose FRR answers with the recordings of package router's
// tests, and whose kernel holds a default route over two links and a blackhole.
func routerNode(t *testing.T, ta *testApp) string {
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
	r := &tooltest.Runner{Rules: []tooltest.Rule{
		{Prefix: "vtysh -c show bgp neighbors json", Out: frr("neighbors.json")},
		{Prefix: "vtysh -c show bgp ipv4 unicast detail json", Out: frr("ipv4-detail.json")},
		{Prefix: "vtysh -c show bgp ipv6 unicast detail json", Out: frr("ipv6-detail.json")},
	}}
	s.Router = &chalkd.Router{Run: r, KernelRoutes: func() ([]*nodev1.KernelRoute, error) {
		return []*nodev1.KernelRoute{
			{Destination: "::/0", Type: "unicast", Protocol: "bgp", Table: 254, Metric: 20, NextHops: []*nodev1.NextHop{
				{Gateway: "fe80::1", Interface: "eth2", Weight: 1}, {Gateway: "fe80::2", Interface: "eth1", Weight: 1}}},
			{Destination: "2001:db8:2::/48", Type: "blackhole", Protocol: "boot", Table: 254, Metric: 1024},
		}, nil
	}}
	return ta.startNode(t, s)
}

// node routes <node> [<prefix>] prints the kernel's routes and BGP's paths within the prefix.
func TestNodeRoutes(t *testing.T) {
	ta := newTestApp(t)
	addr := routerNode(t, ta)
	if err := ta.run(context.Background(), ta.args([]string{"node", "routes", "n1", "::/0"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "DESTINATION      TYPE       PROTOCOL  TABLE  METRIC  NEXT HOPS\n" +
		"::/0             unicast    bgp       254    20      fe80::1 on eth2 (weight 1), fe80::2 on eth1 (weight 1)\n" +
		"2001:db8:2::/48  blackhole  boot      254    1024    -\n" +
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
	addr := routerNode(t, ta)
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
