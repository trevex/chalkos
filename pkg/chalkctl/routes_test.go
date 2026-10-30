package chalkctl

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/chalkd"
	routerplugin "github.com/trevex/chalkos/pkg/chalkd/layers/router"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// routerNode serves n1 as a router whose FRR answers with the recordings of package router's
// tests, and whose kernel holds the default route and an edge's address that BGP installed over
// two links, to the recordings' next hops, and a blackhole. rules answer further commands, such as
// jool's and unbound-control's, before the recordings; documents tells whether the node has a
// NAT64 document edge and a Resolver, whose rendered files chalkd looks for. Its VRF tenant, of
// table 100, holds a route over swp1, and tenant's instance imports a path from the VRF fabric.
func routerNode(t *testing.T, ta *testApp, documents bool, rules ...tooltest.Rule) string {
	t.Helper()
	current, _ := json.Marshal(ta.identity(t))
	s, _ := installedNode(t, ta, current)
	frr := func(name string) string {
		data, err := os.ReadFile("../router/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	r := &tooltest.Runner{Rules: append(rules,
		tooltest.Rule{Prefix: "vtysh -c " + router.NeighborsCommand, Out: frr("neighbors.json")},
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv4 unicast detail json", Out: frr("ipv4-detail.json")},
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv6 unicast detail json", Out: frr("ipv6-detail.json")},
		tooltest.Rule{Prefix: "vtysh -c show bgp vrf tenant ipv4 unicast detail json", Out: "{}"},
		tooltest.Rule{Prefix: "vtysh -c show bgp vrf tenant ipv6 unicast detail json", Out: frr("ipv6-detail-imported.json")},
	)}
	conf, nat64, vrfs, resolvers := filepath.Join(t.TempDir(), "unbound.conf"), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(vrfs, "tenant.json"), []byte(`{"spec": {"table": 100}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if documents {
		for _, file := range []string{conf, filepath.Join(nat64, "edge.json"), filepath.Join(resolvers, "unbound-guest.conf")} {
			if err := os.WriteFile(file, []byte("\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	withPlugins(t, s, layers.Of(layers.Router), routerplugin.New(runsWith{s, r}, routerplugin.Options{ResolverConfig: conf, ResolverDir: resolvers, NAT64Dir: nat64, VRFDir: vrfs, KernelRoutes: func() ([]*router.KernelRoute, error) {
		hops := func() []*router.NextHop {
			return []*router.NextHop{
				{Gateway: "fe80::e06b:dcff:fe54:f465", Interface: "eth2", Weight: 1}, {Gateway: "fe80::b817:4cff:fe8b:9cf", Interface: "eth1", Weight: 1}}
		}
		return []*router.KernelRoute{
			{Destination: "::/0", Type: "unicast", Protocol: "bgp", Table: 254, Metric: 20, NextHops: hops()},
			{Destination: "2001:db8:2::/48", Type: "blackhole", Protocol: "boot", Table: 254, Metric: 1024},
			{Destination: "2001:db8:ffff::1/128", Type: "unicast", Protocol: "bgp", Table: 254, Metric: 20, NextHops: hops()},
			{Destination: "2001:db8:ffff::2/128", Type: "unicast", Protocol: "bgp", Table: 100, Metric: 20, NextHops: []*router.NextHop{{Gateway: "fe80::5054:ff:fe12:102", Interface: "swp1"}}},
		}, nil
	}}))
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

// status prints a router's BGP sessions by VRF, the uptime of each established one.
func TestStatusRouter(t *testing.T) {
	ta := newTestApp(t)
	addr := routerNode(t, ta, false)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "bgp:\n" +
		"  VRF      PEER     NEIGHBOR       STATE        UPTIME  RECEIVED  SENT  BFD\n" +
		"  default  fabric   eth1           Established  1h2m3s  1         2     up\n" +
		"  default  fabric   eth2           Idle         -       0         0     down\n" +
		"  default  transit  203.0.113.254  Active       -       0         0     -\n" +
		"  tenant   leaves   swp1           Established  21s     1         2     -\n" +
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

// status prints a router's NAT64 instances, a line each with its sessions, bindings and the size of
// each protocol's pool, and whether its resolvers run, the default VRF's first.
func TestStatusRouterNAT64AndResolver(t *testing.T) {
	ta := newTestApp(t)
	pool := "0,TCP,192.0.2.8,61001,65535,1059,0\n"
	addr := routerNode(t, ta, true,
		tooltest.Rule{Prefix: "jool instance display", Out: "8fb43ac0,edge,netfilter\n"},
		tooltest.Rule{Prefix: "nsenter --net=/run/netns/nat64-edge jool -i edge stats display --csv --no-headers --all", Out: "JSTAT_RECEIVED6,152\nJSTAT_BIB_ENTRIES,2\nJSTAT_SESSIONS,3\n"},
		tooltest.Rule{Prefix: "nsenter --net=/run/netns/nat64-edge jool -i edge pool4 display --csv --no-headers --tcp", Out: pool},
		tooltest.Rule{Prefix: "nsenter --net=/run/netns/nat64-edge jool -i edge pool4 display --csv --no-headers --udp", Out: pool},
	)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "nat64:\n" +
		"  INSTANCE  SESSIONS  BINDINGS  POOL TCP  POOL UDP  POOL ICMP\n" +
		"  edge      3         2         4535      4535      -\n" +
		"resolver: running\n" +
		"resolver guest: running\n" +
		"certificates:\n"
	if got := ta.stdout.String(); !strings.Contains(got, want) {
		t.Errorf("stdout:\n%s\nlacks:\n%s", got, want)
	}
}

// status says why a router's NAT64 instances are unknown and why its resolvers do not run.
func TestStatusRouterNAT64AndResolverDown(t *testing.T) {
	ta := newTestApp(t)
	addr := routerNode(t, ta, true,
		tooltest.Rule{Prefix: "jool instance display", Err: &tool.Error{Command: "jool", Code: 1, Stderr: "Error: Jool's kernel module is not loaded.\n"}},
		tooltest.Rule{Prefix: "unbound-control status", Err: &tool.Error{Command: "unbound-control", Code: 1, Stderr: "error: connect: No such file or directory for /run/unbound/unbound.ctl\n"}},
		tooltest.Rule{Prefix: "systemctl is-active unbound@guest.service", Out: "inactive\n", Err: &tool.Error{Command: "systemctl is-active unbound@guest.service", Code: 3}},
	)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	got := ta.stdout.String()
	for _, want := range []string{"\nnat64: unknown: ", "Jool's kernel module is not loaded", "\nresolver: not running: ", "No such file or directory", "\nresolver guest: not running: unbound@guest.service is inactive\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout:\n%s\nlacks %q", got, want)
		}
	}
}

// runsWith is the node of s, whose programs r runs.
type runsWith struct {
	*chalkd.Server
	r tool.Runner
}

func (n runsWith) Runner() tool.Runner { return n.r }

// withoutVRFs answers Routes as the router layer of an image without VRFs does: it ignores the
// request's VRF, lists the main table and the default instance's paths, and names no VRF.
type withoutVRFs struct{ *routerplugin.Plugin }

func (p withoutVRFs) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return routerv1connect.NewRouterServiceHandler(p, opts...)
}

func (p withoutVRFs) Routes(ctx context.Context, req *connect.Request[routerv1.RoutesRequest]) (*connect.Response[routerv1.RoutesResponse], error) {
	req.Msg.Vrf = ""
	resp, err := p.Plugin.Routes(ctx, req)
	if err == nil {
		resp.Msg.Vrf = ""
	}
	return resp, err
}

// node routes --vrf refuses the answer of a node whose image does not know VRFs, which lists the
// main table instead of the VRF's.
func TestNodeRoutesOfAVRFWithoutVRFs(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.identity(t))
	s, _ := installedNode(t, ta, current)
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "vtysh", Out: "{}"}}}
	withPlugins(t, s, layers.Of(layers.Router), withoutVRFs{routerplugin.New(runsWith{s, r}, routerplugin.Options{
		KernelRoutes: func() ([]*router.KernelRoute, error) { return nil, nil },
	})})
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"node", "routes", "n1", "--vrf", "tenant"}, addr))
	if want := "the node's image does not know VRFs"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want %q", err, want)
	}
	if out := ta.stdout.String(); out != "" {
		t.Errorf("stdout:\n%s\nwant nothing", out)
	}
}

// node routes --vrf prints a VRF's table and its instance's paths; a path the instance imports
// names the VRF it came from as its peer.
func TestNodeRoutesOfAVRF(t *testing.T) {
	ta := newTestApp(t)
	addr := routerNode(t, ta, false)
	if err := ta.run(context.Background(), ta.args([]string{"node", "routes", "n1", "--vrf", "tenant"}, addr)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(ta.stdout.String()), "\n") {
		got = append(got, strings.Join(strings.Fields(l), " "))
	}
	want := []string{
		"DESTINATION TYPE PROTOCOL TABLE METRIC NEXT HOPS",
		"2001:db8:ffff::2/128 unicast bgp 100 20 fe80::5054:ff:fe12:102 on swp1",
		"",
		"BGP PREFIX BEST AS PATH PEER NEXT HOPS LOCAL PREF MED COMMUNITIES",
		"2001:db8:ffff::1/128 * - local :: - 0 -",
		"2001:db8:ffff::2/128 * 64513 vrf fabric fe80::5054:ff:fe12:102 - 0 -",
	}
	if !slices.Equal(got, want) {
		t.Errorf("stdout:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
