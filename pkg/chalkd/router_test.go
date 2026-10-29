package chalkd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
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
	withPlugins(t, s, layers.Of(layers.Router), &Router{Run: r, KernelRoutes: func() ([]*router.KernelRoute, error) { return kernel, nil }, ResolverConfig: filepath.Join(t.TempDir(), "unbound.conf"), NAT64Dir: t.TempDir()})
	return s, r
}

// routerLayerOf is the router layer of s.
func routerLayerOf(s *Server) *routerLayer {
	for _, p := range s.Plugins.Compiled() {
		if r, ok := p.(*routerLayer); ok {
			return r
		}
	}
	return nil
}

// routerOf is the router side of s, which its router layer serves.
func routerOf(s *Server) *Router { return routerLayerOf(s).r }

// routerStatus is the state of s's router as its layer serves it; nil on an image without the
// router layer.
func routerStatus(t *testing.T, s *Server) *routerv1.StatusResponse {
	t.Helper()
	if !s.Plugins.Layers().Has(layers.Router) {
		return nil
	}
	resp, err := routerLayerOf(s).Status(context.Background(), connect.NewRequest(&routerv1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg
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

// A router's status holds its BGP sessions, as vtysh prints them, asked without its standard error
// reaching the console; when vtysh fails, the router's part says why and the rest of the status
// stands.
func TestRouterStatus(t *testing.T) {
	s, r := routerServer(t)
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "vtysh -c show bgp neighbors json", Out: routerRecording(t, "neighbors.json"), Times: 1})
	st := routerStatus(t, s)
	if st == nil || st.BgpError != "" || len(st.Peers) != 3 || st.Peers[0].Name != "fabric" || st.Peers[0].State != "Established" {
		t.Fatalf("router status = %v", st)
	}
	if !r.Quiet["vtysh -c show bgp neighbors json"] {
		t.Error("vtysh's standard error reaches the console")
	}
	// Without NAT64 documents, Jool is not asked.
	if st.Nat64Error != "" || slices.ContainsFunc(r.Calls, func(c string) bool { return strings.HasPrefix(c, "jool") }) {
		t.Errorf("a router without NAT64 asked jool: %q, %v", st.Nat64Error, r.Calls)
	}
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "vtysh -c show bgp neighbors json", Err: &tool.Error{Command: "vtysh", Code: 1, Stderr: "Exiting: failed to connect to any daemons.\n"}})
	st = routerStatus(t, s)
	if st == nil || len(st.Peers) != 0 || !strings.Contains(st.BgpError, "failed to connect to any daemons") {
		t.Errorf("router status while FRR is down = %v", st)
	}
	// A node without the router layer reports no router.
	plain, _ := installedServer(t, section("", ""), false)
	if st := routerStatus(t, plain); st != nil {
		t.Errorf("router status of a node without the layer = %v", st)
	}
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

// hangingRunner is a vtysh that never answers before its context ends.
type hangingRunner struct{ tool.Runner }

func (hangingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Daemons that hang cost Status one timeout altogether, as chalkd asks them side by side, and the
// router's part says which hang.
func TestRouterStatusWhileDaemonsHang(t *testing.T) {
	s, r := routerServer(t)
	routerOf(s).Run = hangingRunner{r}
	defer func(d time.Duration) { askTimeout = d }(askTimeout)
	askTimeout = 200 * time.Millisecond
	for _, file := range []string{routerOf(s).ResolverConfig, filepath.Join(routerOf(s).NAT64Dir, "edge.json")} {
		if err := os.WriteFile(file, []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	st := routerStatus(t, s)
	if took := time.Since(start); took >= 2*askTimeout {
		t.Errorf("Status took %v while the daemons hang, not about %v", took, askTimeout)
	}
	if st == nil || len(st.Peers) != 0 || !strings.Contains(st.BgpError, "vtysh did not answer within 200ms") {
		t.Fatalf("router status while FRR hangs = %v", st)
	}
	if len(st.Nat64) != 0 || !strings.Contains(st.Nat64Error, "jool did not answer within 200ms") {
		t.Errorf("router status while Jool hangs = %v", st)
	}
	if st.Resolver == nil || st.Resolver.Running || !strings.Contains(st.Resolver.Error, "unbound-control did not answer within 200ms") {
		t.Errorf("router status while Unbound hangs = %v", st)
	}
}

// slowJool is a jool that lists one instance and its statistics at once and takes a while for each
// of its pools.
type slowJool struct {
	tool.Runner
	delay time.Duration
}

func (j slowJool) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "jool" && args[0] == "instance" {
		return []byte("8fb43ac0,edge,netfilter\n"), nil
	}
	if name == "jool" && slices.Contains(args, "stats") {
		return []byte("JSTAT_BIB_ENTRIES,0\nJSTAT_SESSIONS,0\n"), nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(j.delay):
		return nil, nil
	}
}

// jool's questions share one timeout: pools that each answer in time but together take longer
// leave the NAT64 part unknown.
func TestRouterStatusWhileJoolIsSlow(t *testing.T) {
	s, r := routerServer(t)
	routerOf(s).Run = slowJool{r, 40 * time.Millisecond}
	if err := os.WriteFile(filepath.Join(routerOf(s).NAT64Dir, "edge.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func(d time.Duration) { askTimeout = d }(askTimeout)
	askTimeout = 100 * time.Millisecond
	start := time.Now()
	st := routerStatus(t, s)
	if took := time.Since(start); took >= 2*askTimeout {
		t.Errorf("Status took %v while Jool is slow, not about %v", took, askTimeout)
	}
	if st == nil || len(st.Nat64) != 0 || !strings.Contains(st.Nat64Error, "jool did not answer within 100ms") {
		t.Errorf("router status while Jool is slow = %v", st)
	}
}

// A request that ends before vtysh answers is no timeout of vtysh's.
func TestVtyshCancelled(t *testing.T) {
	s, r := routerServer(t)
	routerOf(s).Run = hangingRunner{r}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := routerOf(s).vtysh(ctx, router.NeighborsCommand); err == nil || strings.Contains(err.Error(), "did not answer") {
		t.Errorf("err = %v", err)
	}
}

// A caller's deadline shorter than vtysh's timeout is the caller's, not vtysh's.
func TestVtyshCallerDeadline(t *testing.T) {
	s, r := routerServer(t)
	routerOf(s).Run = hangingRunner{r}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := routerOf(s).vtysh(ctx, router.NeighborsCommand); !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "did not answer") {
		t.Errorf("err = %v", err)
	}
}

// A router's status holds each Jool instance that a NAT64 document of the node names, with its
// sessions, bindings and each protocol's pool size, recorded from Jool 4.1.15, asked without jool's
// standard error reaching the console; an instance of another namespace or one removed meanwhile
// is left out, and when jool fails, the NAT64 part says why and the rest of the status stands.
func TestRouterNAT64Status(t *testing.T) {
	s, r := routerServer(t)
	for _, name := range []string{"edge", "gone"} {
		if err := os.WriteFile(filepath.Join(routerOf(s).NAT64Dir, name+".json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.Rules = append(r.Rules,
		tooltest.Rule{Prefix: "jool instance display --csv --no-headers", Out: "8fb43ac0,edge,netfilter\n8fb43ac0,gone,netfilter\n51c2d0e7,other,netfilter\n51c2d0e7,Other Box,netfilter\n", Times: 1},
		tooltest.Rule{Prefix: "jool -i edge stats display --csv --no-headers --all", Out: routerRecording(t, "jool-stats.csv")},
		tooltest.Rule{Prefix: "jool -i edge pool4 display --csv --no-headers --tcp", Out: routerRecording(t, "jool-pool4-tcp.csv")},
		tooltest.Rule{Prefix: "jool -i gone ", Err: &tool.Error{Command: "jool", Code: 1, Stderr: "Error: The kernel module returned error 3: This namespace lacks an instance named 'gone'.\n"}},
	)
	st := routerStatus(t, s)
	if st == nil || st.Nat64Error != "" || len(st.Nat64) != 1 || st.Nat64[0].Name != "edge" || st.Nat64[0].Sessions != 3 || st.Nat64[0].Bindings != 2 {
		t.Fatalf("router status = %v", st)
	}
	var got []string
	for _, p := range st.Nat64[0].Pools {
		got = append(got, fmt.Sprintf("%s %d", p.Protocol, p.Size))
	}
	if want := []string{"tcp 41837", "udp 0", "icmp 0"}; !slices.Equal(got, want) {
		t.Errorf("pools = %v, want %v", got, want)
	}
	if !r.Quiet["jool instance display --csv --no-headers"] {
		t.Error("jool's standard error reaches the console")
	}
	for _, call := range r.Calls {
		if strings.HasPrefix(call, "jool -i other") || strings.Contains(call, "Other Box") {
			t.Errorf("chalkd asked about an instance no NAT64 document of the node names: %s", call)
		}
	}
	r.Rules = append([]tooltest.Rule{{Prefix: "jool instance display", Err: &tool.Error{Command: "jool", Code: 1, Stderr: "Error: Jool's kernel module is not loaded.\n"}}}, r.Rules...)
	st = routerStatus(t, s)
	if st == nil || len(st.Nat64) != 0 || !strings.Contains(st.Nat64Error, "Jool's kernel module is not loaded") {
		t.Errorf("router status while Jool is down = %v", st)
	}
}

// A router without a rendered Unbound config reports no resolver; with one, unbound-control says
// whether it runs.
func TestRouterResolverStatus(t *testing.T) {
	s, r := routerServer(t)
	if st := routerStatus(t, s); st == nil || st.Resolver != nil {
		t.Fatalf("router status without a resolver = %v", st)
	}
	if err := os.WriteFile(routerOf(s).ResolverConfig, []byte("server:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := routerStatus(t, s); st.GetResolver() == nil || !st.Resolver.Running || st.Resolver.Error != "" {
		t.Errorf("router status of a running resolver = %v", st)
	}
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "unbound-control status", Err: &tool.Error{Command: "unbound-control", Code: 1, Stderr: "error: connect: No such file or directory for /run/unbound/unbound.ctl\n"}})
	if st := routerStatus(t, s); st.GetResolver() == nil || st.Resolver.Running || !strings.Contains(st.Resolver.Error, "No such file or directory") {
		t.Errorf("router status of a stopped resolver = %v", st)
	}
}
