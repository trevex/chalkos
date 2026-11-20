package chalkctl

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/chalkdtest"
	routerplugin "github.com/trevex/chalkos/pkg/chalkd/layers/router"
	frrstatus "github.com/trevex/chalkos/pkg/frr/status"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// routerNode serves n1 as a router whose FRR answers with the recordings of package router's
// tests and whose kernel holds BGP routes over two links, a blackhole, and a route over swp1 in
// VRF tenant (table 100), whose instance imports a path from the VRF fabric.
func routerNode(t *testing.T, ta *testApp) string {
	t.Helper()
	s, _ := installedNode(t, ta, nil)
	frr := func(name string) string {
		data, err := os.ReadFile("../router/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	r := &tooltest.Runner{Rules: []tooltest.Rule{
		{Prefix: "vtysh -c " + frrstatus.NeighborsCommand, Out: frr("neighbors.json")},
		{Prefix: "vtysh -c show bgp ipv4 unicast detail json", Out: frr("ipv4-detail.json")},
		{Prefix: "vtysh -c show bgp ipv6 unicast detail json", Out: frr("ipv6-detail.json")},
		{Prefix: "vtysh -c show bgp vrf tenant ipv4 unicast detail json", Out: "{}"},
		{Prefix: "vtysh -c show bgp vrf tenant ipv6 unicast detail json", Out: frr("ipv6-detail-imported.json")},
	}}
	vrfs := t.TempDir()
	if err := os.WriteFile(filepath.Join(vrfs, "tenant.json"), []byte(`{"spec": {"table": 100}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// FRR's config link, to a config FRR has not filled in, as /etc/frr/frr.conf is before FRR
	// starts.
	frrConf := filepath.Join(t.TempDir(), "frr.conf")
	if err := os.Symlink(filepath.Join(t.TempDir(), "frr.conf"), frrConf); err != nil {
		t.Fatal(err)
	}
	s.Plugins = chalkdtest.Plugins(t, s, layers.Of(layers.Router), routerplugin.New(runsWith{s, r}, routerplugin.Options{FRRConfig: frrConf, VRFDir: vrfs, KernelRoutes: func() ([]*router.KernelRoute, error) {
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
	addr := routerNode(t, ta)
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

// A node of an image without the router layer says so.
func TestNodeRoutesWithoutTheLayer(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, nil)
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"node", "routes", "n1"}, addr))
	if connect.CodeOf(err) != connect.CodeUnimplemented || !strings.Contains(err.Error(), "the router layer is not in this image") {
		t.Errorf("err = %v", err)
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

func (p withoutVRFs) Register(server *connect.Server) {
	routerv1connect.RegisterRouterServiceHandler(server, p)
}

func (p withoutVRFs) Routes(ctx context.Context, req *routerv1.RoutesRequest) (*routerv1.RoutesResponse, error) {
	req.Vrf = ""
	resp, err := p.Plugin.Routes(ctx, req)
	if err == nil {
		resp.Vrf = ""
	}
	return resp, err
}

// node routes --vrf refuses the answer of a node whose image does not know VRFs, which lists the
// main table instead of the VRF's.
func TestNodeRoutesOfAVRFWithoutVRFs(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, nil)
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "vtysh", Out: "{}"}}}
	s.Plugins = chalkdtest.Plugins(t, s, layers.Of(layers.Router), withoutVRFs{routerplugin.New(runsWith{s, r}, routerplugin.Options{
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
	addr := routerNode(t, ta)
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
