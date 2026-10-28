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

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// routerServer is an installed router whose FRR answers vtysh as the runner's rules say, and whose
// kernel holds the routes given.
func routerServer(t *testing.T, kernel ...*nodev1.KernelRoute) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := installedServer(t, section("", ""), false)
	s.Layers = layers.Of(layers.Router)
	s.Router = &Router{Run: r, KernelRoutes: func() ([]*nodev1.KernelRoute, error) { return kernel, nil }, ResolverConfig: filepath.Join(t.TempDir(), "unbound.conf")}
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

// A router's status holds its BGP sessions, as vtysh prints them, asked without its standard error
// reaching the console; when vtysh fails, the router's part says why and the rest of the status
// stands.
func TestRouterStatus(t *testing.T) {
	s, r := routerServer(t)
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "vtysh -c show bgp neighbors json", Out: routerRecording(t, "neighbors.json"), Times: 1})
	st := status(t, s).Router
	if st == nil || st.BgpError != "" || len(st.Peers) != 3 || st.Peers[0].Name != "fabric" || st.Peers[0].State != "Established" {
		t.Fatalf("router status = %v", st)
	}
	if !r.Quiet["vtysh -c show bgp neighbors json"] {
		t.Error("vtysh's standard error reaches the console")
	}
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "vtysh -c show bgp neighbors json", Err: &tool.Error{Command: "vtysh", Code: 1, Stderr: "Exiting: failed to connect to any daemons.\n"}})
	st = status(t, s).Router
	if st == nil || len(st.Peers) != 0 || !strings.Contains(st.BgpError, "failed to connect to any daemons") {
		t.Errorf("router status while FRR is down = %v", st)
	}
	// A node without the router layer reports no router.
	plain, _ := installedServer(t, section("", ""), false)
	if st := status(t, plain).Router; st != nil {
		t.Errorf("router status of a node without the layer = %v", st)
	}
}

// Routes answers readers on a router: the kernel's routes and BGP's paths of both families, both
// within the prefix asked for, and the kernel's of one protocol and table; BGP's paths fail apart
// from the kernel's.
func TestRoutes(t *testing.T) {
	s, r := routerServer(t,
		&nodev1.KernelRoute{Destination: "::/0", Type: "unicast", Protocol: "bgp", Table: 254, Metric: 20},
		&nodev1.KernelRoute{Destination: "2001:db8:2::/48", Type: "blackhole", Protocol: "boot", Table: 254},
		&nodev1.KernelRoute{Destination: "192.0.2.1/32", Type: "unicast", Protocol: "bgp", Table: 254},
	)
	r.Rules = append(r.Rules,
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv4 unicast detail json", Out: routerRecording(t, "ipv4-detail.json"), Times: 2},
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv6 unicast detail json", Out: routerRecording(t, "ipv6-detail.json"), Times: 2},
	)
	c := newCreds(t)
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleReader])
	routes := func(prefix, protocol, table string) (*nodev1.RoutesResponse, error) {
		resp, err := conn.Routes(context.Background(), connect.NewRequest(&nodev1.RoutesRequest{Prefix: prefix, Protocol: protocol, Table: table}))
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
	s.Router.Run = hangingRunner{r}
	defer func(d time.Duration) { askTimeout = d }(askTimeout)
	askTimeout = 200 * time.Millisecond
	if err := os.WriteFile(s.Router.ResolverConfig, []byte("server:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	st := status(t, s).Router
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

// slowJool is a jool that lists one instance at once and takes a while for each of its tables.
type slowJool struct {
	tool.Runner
	delay time.Duration
}

func (j slowJool) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "jool" && args[0] == "instance" {
		return []byte("8fb43ac0,edge,netfilter\n"), nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(j.delay):
		return nil, nil
	}
}

// jool's questions share one timeout: tables that each answer in time but together take longer
// leave the NAT64 part unknown.
func TestRouterStatusWhileJoolIsSlow(t *testing.T) {
	s, r := routerServer(t)
	s.Router.Run = slowJool{r, 30 * time.Millisecond}
	defer func(d time.Duration) { askTimeout = d }(askTimeout)
	askTimeout = 100 * time.Millisecond
	start := time.Now()
	st := status(t, s).Router
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
	s.Router.Run = hangingRunner{r}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Router.vtysh(ctx, router.NeighborsCommand); err == nil || strings.Contains(err.Error(), "did not answer") {
		t.Errorf("err = %v", err)
	}
}

// A caller's deadline shorter than vtysh's timeout is the caller's, not vtysh's.
func TestVtyshCallerDeadline(t *testing.T) {
	s, r := routerServer(t)
	s.Router.Run = hangingRunner{r}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.Router.vtysh(ctx, router.NeighborsCommand); !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "did not answer") {
		t.Errorf("err = %v", err)
	}
}

// A router's status holds each of Jool's instances, recorded from Jool 4.1.15, with each
// protocol's sessions, bindings and pool size, asked without jool's standard error reaching the
// console; when jool fails, the NAT64 part says why and the rest of the status stands.
func TestRouterNAT64Status(t *testing.T) {
	s, r := routerServer(t)
	r.Rules = append(r.Rules,
		tooltest.Rule{Prefix: "jool instance display --csv --no-headers", Out: "8fb43ac0,edge,netfilter\n", Times: 1},
		tooltest.Rule{Prefix: "jool -i edge session display --csv --no-headers --tcp", Out: routerRecording(t, "jool-sessions-tcp.csv")},
		tooltest.Rule{Prefix: "jool -i edge session display --csv --no-headers --icmp", Out: routerRecording(t, "jool-sessions-icmp.csv")},
		tooltest.Rule{Prefix: "jool -i edge bib display --csv --no-headers --tcp", Out: "TCP,2001:db8:ffff::2,33830,192.0.2.8,62105,0\n"},
		tooltest.Rule{Prefix: "jool -i edge pool4 display --csv --no-headers --tcp", Out: routerRecording(t, "jool-pool4-tcp.csv")},
	)
	st := status(t, s).Router
	if st == nil || st.Nat64Error != "" || len(st.Nat64) != 1 || st.Nat64[0].Name != "edge" {
		t.Fatalf("router status = %v", st)
	}
	var got []string
	for _, p := range st.Nat64[0].Protocols {
		got = append(got, fmt.Sprintf("%s %d %d %d", p.Protocol, p.Sessions, p.Bindings, p.PoolSize))
	}
	if want := []string{"tcp 2 1 41837", "udp 0 0 0", "icmp 1 0 0"}; !slices.Equal(got, want) {
		t.Errorf("protocols = %v, want %v", got, want)
	}
	if !r.Quiet["jool instance display --csv --no-headers"] {
		t.Error("jool's standard error reaches the console")
	}
	r.Rules = append([]tooltest.Rule{{Prefix: "jool instance display", Err: &tool.Error{Command: "jool", Code: 1, Stderr: "Error: Jool's kernel module is not loaded.\n"}}}, r.Rules...)
	st = status(t, s).Router
	if st == nil || len(st.Nat64) != 0 || !strings.Contains(st.Nat64Error, "Jool's kernel module is not loaded") {
		t.Errorf("router status while Jool is down = %v", st)
	}
}

// A router without a rendered Unbound config reports no resolver; with one, unbound-control says
// whether it runs.
func TestRouterResolverStatus(t *testing.T) {
	s, r := routerServer(t)
	if st := status(t, s).Router; st == nil || st.Resolver != nil {
		t.Fatalf("router status without a resolver = %v", st)
	}
	if err := os.WriteFile(s.Router.ResolverConfig, []byte("server:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := status(t, s).Router; st.GetResolver() == nil || !st.Resolver.Running || st.Resolver.Error != "" {
		t.Errorf("router status of a running resolver = %v", st)
	}
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "unbound-control status", Err: &tool.Error{Command: "unbound-control", Code: 1, Stderr: "error: connect: No such file or directory for /run/unbound/unbound.ctl\n"}})
	if st := status(t, s).Router; st.GetResolver() == nil || st.Resolver.Running || !strings.Contains(st.Resolver.Error, "No such file or directory") {
		t.Errorf("router status of a stopped resolver = %v", st)
	}
}
