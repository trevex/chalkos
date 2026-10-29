package chalkd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	routerplugin "github.com/trevex/chalkos/pkg/chalkd/layers/router"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// routerServer is an installed router whose FRR answers vtysh as the runner's rules say, and whose
// kernel holds the routes given.
func routerServer(t *testing.T, kernel ...*router.KernelRoute) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := installedServer(t, section("", ""), false)
	withPlugins(t, s, layers.Of(layers.Router), routerplugin.New(s, routerplugin.Options{KernelRoutes: func() ([]*router.KernelRoute, error) { return kernel, nil }, ResolverConfig: filepath.Join(t.TempDir(), "unbound.conf"), NAT64Dir: t.TempDir()}))
	return s, r
}

// routerRecording is a recording of FRR's JSON or jool's CSV from package router's tests.
func routerRecording(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../router/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Routes answers readers on a router: the kernel's routes and BGP's paths of both families, both
// within the prefix asked for, and the kernel's of one protocol and table; BGP's paths fail apart
// from the kernel's.
func TestRoutes(t *testing.T) {
	s, r := routerServer(t,
		&router.KernelRoute{Destination: "::/0", Type: "unicast", Protocol: "bgp", Table: 254, Metric: 20},
		&router.KernelRoute{Destination: "2001:db8:2::/48", Type: "blackhole", Protocol: "boot", Table: 254},
		&router.KernelRoute{Destination: "192.0.2.1/32", Type: "unicast", Protocol: "bgp", Table: 254},
	)
	r.Rules = append(r.Rules,
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv4 unicast detail json", Out: routerRecording(t, "ipv4-detail.json"), Times: 2},
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv6 unicast detail json", Out: routerRecording(t, "ipv6-detail.json"), Times: 2},
	)
	c := newCreds(t)
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleReader])
	routes := func(prefix, protocol, table string) (*routerv1.RoutesResponse, error) {
		resp, err := routerv1connect.NewRouterServiceClient(conn.HTTPClient(), conn.BaseURL()).Routes(context.Background(), connect.NewRequest(&routerv1.RoutesRequest{Prefix: prefix, Protocol: protocol, Table: table}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	resp, err := routes("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Kernel) != 3 || len(resp.Bgp) != 7 || resp.BgpError != "" {
		t.Errorf("all routes: %d kernel, %d BGP, %q", len(resp.Kernel), len(resp.Bgp), resp.BgpError)
	}
	resp, err = routes("::/0", "bgp", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Kernel) != 1 || resp.Kernel[0].Destination != "::/0" || len(resp.Bgp) != 5 || resp.Bgp[0].Prefix != "::/0" || !resp.Bgp[0].Best {
		t.Errorf("the IPv6 BGP routes: %v, %v", resp.Kernel, resp.Bgp)
	}
	resp, err = routes("", "boot", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Kernel) != 1 || len(resp.Bgp) != 0 {
		t.Errorf("the boot routes: %v, %v", resp.Kernel, resp.Bgp)
	}
	if _, err := routes("", "", "mine"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("an unknown table: %v", err)
	}
	r.Rules = append([]tooltest.Rule{{Prefix: "vtysh -c show bgp ipv4", Err: &tool.Error{Command: "vtysh", Code: 1, Stderr: "Exiting: failed to connect to any daemons.\n"}}}, r.Rules...)
	resp, err = routes("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Kernel) != 3 || len(resp.Bgp) != 0 || !strings.Contains(resp.BgpError, "failed to connect to any daemons") {
		t.Errorf("while FRR is down: %d kernel, %v, %q", len(resp.Kernel), resp.Bgp, resp.BgpError)
	}
}
