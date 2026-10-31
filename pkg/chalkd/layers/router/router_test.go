package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	routing "github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// runnerNode is the common chalkd as the router layer uses it: the runner of its programs.
type runnerNode struct {
	layer.Node
	run tool.Runner
}

func (n runnerNode) Runner() tool.Runner { return n.run }

// routerNode is the router layer of a node whose FRR answers vtysh as the runner's rules say,
// whose kernel holds the routes given, and whose running config holds the VRF tenant of table
// 100.
func routerNode(t *testing.T, kernel ...*routing.KernelRoute) (*Plugin, *tooltest.Runner) {
	t.Helper()
	r := &tooltest.Runner{}
	vrfs, resolvers := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(vrfs, "tenant.json"), []byte(`{"apiVersion": "router.chalkos.dev/v1alpha1", "kind": "VRF", "metadata": {"name": "tenant"}, "spec": {"table": 100, "interfaces": ["swp1"]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(runnerNode{run: r}, Options{KernelRoutes: func() ([]*routing.KernelRoute, error) { return kernel, nil }, ResolverConfig: filepath.Join(t.TempDir(), "unbound.conf"), NAT64Dir: t.TempDir(), VRFDir: vrfs, ResolverDir: resolvers, KeaDir: t.TempDir(), RunDir: t.TempDir(), NetDir: t.TempDir()}), r
}

// writeIndex writes the device index index into dir/ifindex, as the kernel shows a device's in
// sysfs and a VRF's resolver records the one it is bound to.
func writeIndex(t *testing.T, dir, index string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ifindex"), []byte(index+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// routerStatus is the state of the router as the layer serves it.
func routerStatus(t *testing.T, p *Plugin) *routerv1.StatusResponse {
	t.Helper()
	resp, err := p.Status(context.Background(), connect.NewRequest(&routerv1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg
}

// routerRecording is a recording of FRR's JSON or jool's CSV from package router's tests.
func routerRecording(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../../router/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A router's status holds its BGP sessions, as vtysh prints them, asked without its standard error
// reaching the console; when vtysh fails, the router's part says why and the rest of the status
// stands.
func TestRouterStatus(t *testing.T) {
	s, r := routerNode(t)
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "vtysh -c " + routing.NeighborsCommand, Out: routerRecording(t, "neighbors.json"), Times: 1})
	st := routerStatus(t, s)
	if st == nil || st.BgpError != "" || len(st.Peers) != 4 || st.Peers[0].Name != "fabric" || st.Peers[0].State != "Established" || st.Peers[0].Vrf != "default" || st.Peers[3].Vrf != "tenant" {
		t.Fatalf("router status = %v", st)
	}
	if !r.Quiet["vtysh -c "+routing.NeighborsCommand] {
		t.Error("vtysh's standard error reaches the console")
	}
	// Without NAT64 documents, Jool is not asked.
	if st.Nat64Error != "" || slices.ContainsFunc(r.Calls, func(c string) bool { return strings.HasPrefix(c, "jool") }) {
		t.Errorf("a router without NAT64 asked jool: %q, %v", st.Nat64Error, r.Calls)
	}
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "vtysh -c " + routing.NeighborsCommand, Err: &tool.Error{Command: "vtysh", Code: 1, Stderr: "Exiting: failed to connect to any daemons.\n"}})
	st = routerStatus(t, s)
	if st == nil || len(st.Peers) != 0 || !strings.Contains(st.BgpError, "failed to connect to any daemons") {
		t.Errorf("router status while FRR is down = %v", st)
	}
}

// hangingRunner is a vtysh that never answers before its context ends.
type hangingRunner struct{ tool.Runner }

func (hangingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Daemons that hang, systemd too, cost Status one timeout altogether, as chalkd asks them side by
// side, and the router's part says which hang.
func TestRouterStatusWhileDaemonsHang(t *testing.T) {
	s, r := routerNode(t)
	s.r.run = hangingRunner{r}
	defer func(d time.Duration) { askTimeout = d }(askTimeout)
	askTimeout = 200 * time.Millisecond
	for _, file := range []string{s.r.ResolverConfig, filepath.Join(s.r.NAT64Dir, "edge.json"), filepath.Join(s.r.ResolverDir, "unbound-guest.conf")} {
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
	if len(st.VrfResolvers) != 1 || st.VrfResolvers[0].Running || !strings.Contains(st.VrfResolvers[0].Error, "systemctl did not answer within 200ms") {
		t.Errorf("router status while systemd hangs = %v", st.VrfResolvers)
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
	if name == "nsenter" && slices.Contains(args, "stats") {
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
	s, r := routerNode(t)
	s.r.run = slowJool{r, 40 * time.Millisecond}
	if err := os.WriteFile(filepath.Join(s.r.NAT64Dir, "edge.json"), []byte("{}\n"), 0o644); err != nil {
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
	s, r := routerNode(t)
	s.r.run = hangingRunner{r}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.r.vtysh(ctx, routing.NeighborsCommand); err == nil || strings.Contains(err.Error(), "did not answer") {
		t.Errorf("err = %v", err)
	}
}

// A caller's deadline shorter than vtysh's timeout is the caller's, not vtysh's.
func TestVtyshCallerDeadline(t *testing.T) {
	s, r := routerNode(t)
	s.r.run = hangingRunner{r}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.r.vtysh(ctx, routing.NeighborsCommand); !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "did not answer") {
		t.Errorf("err = %v", err)
	}
}

// A router's status holds each Jool instance that a NAT64 document of the node names, with its
// sessions, bindings and each protocol's pool size, recorded from Jool 4.1.15, asked without jool's
// standard error reaching the console; an instance no NAT64 document names, one whose namespace or
// instance went meanwhile is left out; chalkd asks Jool in each instance's namespace; and when jool
// fails, the NAT64 part says why and the rest of the status stands.
func TestRouterNAT64Status(t *testing.T) {
	s, r := routerNode(t)
	for _, name := range []string{"edge", "gone", "stale"} {
		if err := os.WriteFile(filepath.Join(s.r.NAT64Dir, name+".json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.Rules = append(r.Rules,
		tooltest.Rule{Prefix: "jool instance display --csv --no-headers", Out: "8fb43ac0,edge,netfilter\n8fb43ac0,gone,netfilter\n8fb43ac0,stale,netfilter\n51c2d0e7,other,netfilter\n51c2d0e7,Other Box,netfilter\n", Times: 1},
		tooltest.Rule{Prefix: "nsenter --net=/run/netns/nat64-edge jool -i edge stats display --csv --no-headers --all", Out: routerRecording(t, "jool-stats.csv")},
		tooltest.Rule{Prefix: "nsenter --net=/run/netns/nat64-edge jool -i edge pool4 display --csv --no-headers --tcp", Out: routerRecording(t, "jool-pool4-tcp.csv")},
		tooltest.Rule{Prefix: "nsenter --net=/run/netns/nat64-gone ", Err: &tool.Error{Command: "nsenter", Code: 1, Stderr: "nsenter: cannot open /run/netns/nat64-gone: No such file or directory\n"}},
		tooltest.Rule{Prefix: "nsenter --net=/run/netns/nat64-stale ", Err: &tool.Error{Command: "nsenter", Code: 1, Stderr: "Error: The kernel module returned error 3: This namespace lacks an instance named 'stale'.\n"}},
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
		if strings.Contains(call, "nat64-other") || strings.Contains(call, "Other Box") || strings.HasPrefix(call, "jool -i") {
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
	s, r := routerNode(t)
	if st := routerStatus(t, s); st == nil || st.Resolver != nil {
		t.Fatalf("router status without a resolver = %v", st)
	}
	if err := os.WriteFile(s.r.ResolverConfig, []byte("server:\n"), 0o644); err != nil {
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

// A router's status holds the resolver of each VRF whose config the output unbound rendered,
// sorted by VRF, each the state of its unit unbound@<vrf>, whose name escapes the VRF's dashes as
// systemd-escape does; a stopped one says what state its unit is in. A file of no VRF's name is
// left out and reaches no command.
func TestRouterVRFResolverStatus(t *testing.T) {
	s, r := routerNode(t)
	for _, vrf := range []string{"tenant", "dmz-a", "Bad_Name", "x;y"} {
		if err := os.WriteFile(filepath.Join(s.r.ResolverDir, "unbound-"+vrf+".conf"), []byte("server:\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.r.ResolverDir, "unbound.conf"), []byte("server:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeIndex(t, filepath.Join(s.r.RunDir, "unbound-dmz-a"), "7")
	writeIndex(t, filepath.Join(s.r.NetDir, "dmz-a"), "7")
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "systemctl is-active unbound@tenant.service", Out: "failed\n", Err: &tool.Error{Command: "systemctl is-active unbound@tenant.service", Code: 3}})
	st := routerStatus(t, s)
	if st.GetResolver() != nil || len(st.VrfResolvers) != 2 {
		t.Fatalf("router status = %v", st)
	}
	dmz, tenant := st.VrfResolvers[0], st.VrfResolvers[1]
	if dmz.Vrf != "dmz-a" || !dmz.Running || dmz.Error != "" {
		t.Errorf("the resolver of dmz-a = %v", dmz)
	}
	if tenant.Vrf != "tenant" || tenant.Running || tenant.Error != `unbound@tenant.service is failed` {
		t.Errorf("the resolver of tenant = %v", tenant)
	}
	if !slices.Contains(r.Calls, `systemctl is-active unbound@dmz\x2da.service`) {
		t.Errorf("dmz-a's unit was not asked by its escaped name: %q", r.Calls)
	}
	if !r.Quiet["systemctl is-active unbound@tenant.service"] {
		t.Error("systemctl's standard error reaches the console")
	}
	if slices.ContainsFunc(r.Calls, func(c string) bool { return strings.Contains(c, "Bad_Name") || strings.Contains(c, "x;y") }) {
		t.Errorf("a file of no VRF's name reached a command: %q", r.Calls)
	}
}

// An active resolver counts as running only while bound to its VRF's device: one bound to an
// index the VRF's device no longer has, one whose VRF has no device and one that recorded no
// device do not, and say so.
func TestRouterVRFResolverStale(t *testing.T) {
	s, _ := routerNode(t)
	for _, vrf := range []string{"current", "moved", "gone", "unrecorded"} {
		if err := os.WriteFile(filepath.Join(s.r.ResolverDir, "unbound-"+vrf+".conf"), []byte("server:\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for vrf, index := range map[string]string{"current": "5", "moved": "6", "gone": "8"} {
		writeIndex(t, filepath.Join(s.r.RunDir, "unbound-"+vrf), index)
	}
	for vrf, index := range map[string]string{"current": "5", "moved": "9", "unrecorded": "10"} {
		writeIndex(t, filepath.Join(s.r.NetDir, vrf), index)
	}
	want := map[string]string{
		"current":    "",
		"moved":      "unbound@moved.service is active, but it is bound to a removed device of the VRF",
		"gone":       "unbound@gone.service is active, but the VRF has no device",
		"unrecorded": "unbound@unrecorded.service is active, but it recorded no device",
	}
	st := routerStatus(t, s)
	if len(st.VrfResolvers) != len(want) {
		t.Fatalf("router status = %v", st)
	}
	for _, v := range st.VrfResolvers {
		if v.Error != want[v.Vrf] || v.Running != (want[v.Vrf] == "") {
			t.Errorf("the resolver of %s = %v, want the error %q", v.Vrf, v, want[v.Vrf])
		}
	}
}

// Routes of a VRF list its table, which the running VRF document gives, and its instance's paths,
// and name the VRF; default is the main table and the default instance, asked without a VRF, and
// a request without a VRF lists default. A request of a VRF and a table, of a VRF the node lacks
// or of a name no VRF document takes is refused, and asks vtysh nothing; a running VRF document
// without a table, or no JSON, is chalkd's error.
func TestRoutesOfAVRF(t *testing.T) {
	p, r := routerNode(t,
		&routing.KernelRoute{Destination: "2001:db8:ffff::2/128", Type: "unicast", Protocol: "bgp", Table: 100, Metric: 20},
		&routing.KernelRoute{Destination: "2001:db8:ffff::2/128", Type: "unicast", Protocol: "bgp", Table: 254, Metric: 20},
	)
	// The recording is of a default instance that imports fabric; it stands for tenant's paths,
	// whose import of fabric has the same shape.
	r.Rules = append(r.Rules,
		tooltest.Rule{Prefix: "vtysh -c show bgp vrf tenant ipv4 unicast detail json", Out: "{}"},
		tooltest.Rule{Prefix: "vtysh -c show bgp vrf tenant ipv6 unicast detail json", Out: routerRecording(t, "ipv6-detail-imported.json")},
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv4 unicast detail json", Out: "{}"},
		tooltest.Rule{Prefix: "vtysh -c show bgp ipv6 unicast detail json", Out: "{}"},
	)
	routes := func(req *routerv1.RoutesRequest) (*routerv1.RoutesResponse, error) {
		resp, err := p.Routes(context.Background(), connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	got, err := routes(&routerv1.RoutesRequest{Vrf: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Vrf != "tenant" || len(got.Kernel) != 1 || got.Kernel[0].Table != 100 || len(got.Bgp) != 2 || got.Bgp[1].ImportedFrom != "fabric" || got.Bgp[1].AsPath != "64513" {
		t.Errorf("routes of tenant = %v", got)
	}
	calls := len(r.Calls)
	got, err = routes(&routerv1.RoutesRequest{Vrf: "default"})
	if err != nil || got.Vrf != "default" || len(got.Kernel) != 1 || got.Kernel[0].Table != 254 {
		t.Errorf("routes of default = %v, %v", got, err)
	}
	if asked := r.Calls[calls:]; !slices.Equal(asked, []string{"vtysh -c show bgp ipv4 unicast detail json", "vtysh -c show bgp ipv6 unicast detail json"}) {
		t.Errorf("routes of default asked %v, want the default instance's paths", asked)
	}
	if got, err := routes(&routerv1.RoutesRequest{}); err != nil || got.Vrf != "default" {
		t.Errorf("routes without a VRF = %v, %v, want those of default", got, err)
	}
	for name, doc := range map[string]string{"notable": `{"spec": {"interfaces": ["swp2"]}}`, "broken": "{"} {
		if err := os.WriteFile(filepath.Join(p.r.VRFDir, name+".json"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	calls = len(r.Calls)
	for _, tc := range []struct {
		req  *routerv1.RoutesRequest
		code connect.Code
		want string
	}{
		{&routerv1.RoutesRequest{Vrf: "tenant", Table: "100"}, connect.CodeInvalidArgument, "give a VRF or a table, not both"},
		{&routerv1.RoutesRequest{Vrf: "lab"}, connect.CodeNotFound, "the node has no VRF lab"},
		{&routerv1.RoutesRequest{Vrf: "../tenant"}, connect.CodeInvalidArgument, `vrf "../tenant" is no VRF document's name`},
		{&routerv1.RoutesRequest{Vrf: "tenant summary json\nshow"}, connect.CodeInvalidArgument, "is no VRF document's name"},
		{&routerv1.RoutesRequest{Vrf: "notable"}, connect.CodeInternal, "read the VRF notable: its document holds no table"},
		{&routerv1.RoutesRequest{Vrf: "broken"}, connect.CodeInternal, "read the VRF broken: unexpected end of JSON input"},
	} {
		if _, err := routes(tc.req); connect.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %v with %q", tc.req, err, tc.code, tc.want)
		}
	}
	if len(r.Calls) != calls {
		t.Errorf("refused requests asked %v", r.Calls[calls:])
	}
}

// fakeKea answers Kea's commands on the control socket socket with the answers given, by command,
// and with an unknown command's answer otherwise, until the test ends; with hang it reads the
// command and never answers.
func fakeKea(t *testing.T, socket string, hang bool, answers map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socket), 0o750); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		l.Close()
	})
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var c struct {
					Command string `json:"command"`
				}
				if err := json.NewDecoder(conn).Decode(&c); err != nil {
					return
				}
				if hang {
					<-done
					return
				}
				answer, ok := answers[c.Command]
				if !ok {
					answer = `{"result": 2, "text": "'` + c.Command + `' command not supported."}`
				}
				_, _ = conn.Write([]byte(answer))
			}()
		}
	}()
}

// keaConfig is a config of the output kea of the subnets given, each a name, a range and Kea's
// number for it.
func keaConfig(t *testing.T, path string, subnets ...[3]string) {
	t.Helper()
	var s []string
	for _, sub := range subnets {
		s = append(s, `{"id": `+sub[2]+`, "subnet": "`+sub[1]+`", "user-context": {"name": "`+sub[0]+`"}}`)
	}
	if err := os.WriteFile(path, []byte(`{"Dhcp4": {"subnet4": [`+strings.Join(s, ", ")+`]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A router's status holds the DHCP server of each VRF with a rendered config, the default VRF's
// first: a running server's subnets with Kea's counts of their leased and pool addresses, by its
// document's name, and a server whose unit is not active, which is bound to a removed device of
// its VRF, which does not answer or which refuses the command, with why. A file of no VRF's name
// reaches no command.
func TestRouterDHCPStatus(t *testing.T) {
	s, r := routerNode(t)
	dir, run := s.r.KeaDir, filepath.Join(s.r.RunDir, "kea")
	keaConfig(t, filepath.Join(dir, "kea-dhcp4.json"), [3]string{"office", "192.0.2.0/24", "3221225984"}, [3]string{"lab", "203.0.113.0/26", "3405803776"})
	for _, vrf := range []string{"guest", "dmz-a", "moved", "silent", "refusing", "Bad_Name"} {
		keaConfig(t, filepath.Join(dir, "kea-dhcp4-"+vrf+".json"), [3]string{"guests", "198.51.100.0/24", "3325256704"})
	}
	stats := `{"result": 0, "arguments": {
		"subnet[3221225984].assigned-addresses": [[2, "2026-11-01 10:00:00.000000"], [1, "2026-11-01 09:00:00.000000"]],
		"subnet[3221225984].total-addresses": [[100, "2026-11-01 09:00:00.000000"]],
		"subnet[3405803776].assigned-addresses": [[0, "2026-11-01 09:00:00.000000"]],
		"subnet[3405803776].total-addresses": [[62, "2026-11-01 09:00:00.000000"]],
		"pkt4-received": [[7, "2026-11-01 10:00:00.000000"]]}}`
	fakeKea(t, filepath.Join(run, "dhcp4.sock"), false, map[string]string{"statistic-get-all": stats})
	fakeKea(t, filepath.Join(run, "dhcp4-dmz-a.sock"), false, map[string]string{"statistic-get-all": `{"result": 0, "arguments": {
		"subnet[3325256704].assigned-addresses": [[5, "2026-11-01 10:00:00.000000"]],
		"subnet[3325256704].total-addresses": [[241, "2026-11-01 09:00:00.000000"]]}}`})
	fakeKea(t, filepath.Join(run, "dhcp4-refusing.sock"), false, nil)
	for vrf, index := range map[string][2]string{"dmz-a": {"7", "7"}, "moved": {"6", "9"}, "silent": {"4", "4"}, "refusing": {"5", "5"}} {
		if err := os.WriteFile(filepath.Join(run, "dhcp4-"+vrf+".ifindex"), []byte(index[0]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeIndex(t, filepath.Join(s.r.NetDir, vrf), index[1])
	}
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "systemctl is-active kea-dhcp4@guest.service", Out: "failed\n", Err: &tool.Error{Command: "systemctl is-active kea-dhcp4@guest.service", Code: 3}})
	st := routerStatus(t, s)
	got := map[string]*routerv1.DHCPServerStatus{}
	var order []string
	for _, d := range st.Dhcp {
		got[d.Vrf] = d
		order = append(order, d.Vrf)
	}
	if !slices.Equal(order, []string{"default", "dmz-a", "guest", "moved", "refusing", "silent"}) {
		t.Fatalf("DHCP servers = %v", st.Dhcp)
	}
	def := got["default"]
	if !def.Running || def.Error != "" || def.Family != "ipv4" || len(def.Subnets) != 2 ||
		def.Subnets[0].Name != "office" || def.Subnets[0].Subnet != "192.0.2.0/24" || def.Subnets[0].Leased != 2 || def.Subnets[0].Pool != 100 ||
		def.Subnets[1].Name != "lab" || def.Subnets[1].Leased != 0 || def.Subnets[1].Pool != 62 {
		t.Errorf("the default VRF's server = %v", def)
	}
	if d := got["dmz-a"]; !d.Running || len(d.Subnets) != 1 || d.Subnets[0].Leased != 5 || d.Subnets[0].Pool != 241 {
		t.Errorf("dmz-a's server = %v", d)
	}
	for vrf, want := range map[string]string{
		"guest":    "kea-dhcp4@guest.service is failed",
		"moved":    "kea-dhcp4@moved.service is active, but it is bound to a removed device of the VRF",
		"silent":   "connect: no such file or directory",
		"refusing": "Kea answered statistic-get-all with result 2: 'statistic-get-all' command not supported.",
	} {
		if d := got[vrf]; d.Running || len(d.Subnets) != 0 || !strings.Contains(d.Error, want) {
			t.Errorf("%s's server = %v, want an error with %q", vrf, d, want)
		}
	}
	if !slices.Contains(r.Calls, `systemctl is-active kea-dhcp4@dmz\x2da.service`) || !slices.Contains(r.Calls, "systemctl is-active kea-dhcp4.service") {
		t.Errorf("the units were not asked by their names: %q", r.Calls)
	}
	if slices.ContainsFunc(r.Calls, func(c string) bool { return strings.Contains(c, "Bad_Name") }) {
		t.Errorf("a file of no VRF's name reached a command: %q", r.Calls)
	}
}

// A DHCP server that hangs costs Status one timeout, and its part says so.
func TestRouterDHCPStatusWhileKeaHangs(t *testing.T) {
	s, _ := routerNode(t)
	defer func(d time.Duration) { askTimeout = d }(askTimeout)
	askTimeout = 200 * time.Millisecond
	keaConfig(t, filepath.Join(s.r.KeaDir, "kea-dhcp4.json"), [3]string{"office", "192.0.2.0/24", "3221225984"})
	fakeKea(t, filepath.Join(s.r.RunDir, "kea", "dhcp4.sock"), true, nil)
	start := time.Now()
	st := routerStatus(t, s)
	if took := time.Since(start); took >= 2*askTimeout {
		t.Errorf("Status took %v while Kea hangs, not about %v", took, askTimeout)
	}
	if len(st.Dhcp) != 1 || st.Dhcp[0].Running || st.Dhcp[0].Error != "Kea did not answer within 200ms" {
		t.Errorf("DHCP servers while Kea hangs = %v", st.Dhcp)
	}
}

// A router whose image runs no Kea, or that serves no subnet, has no output kea's configs: its
// status holds no DHCP server and asks about none.
func TestRouterDHCPStatusWithoutKea(t *testing.T) {
	s, r := routerNode(t)
	s.r.KeaDir = filepath.Join(t.TempDir(), "missing")
	st := routerStatus(t, s)
	if len(st.Dhcp) != 0 {
		t.Errorf("DHCP servers without Kea = %v", st.Dhcp)
	}
	if slices.ContainsFunc(r.Calls, func(c string) bool { return strings.Contains(c, "kea") }) {
		t.Errorf("a router without Kea asked %q", r.Calls)
	}
}

// An answer of Kea counts even when the questions' budget ends right after it; a question that
// failed names the budget when it ran out, or the error.
func TestKeaFailure(t *testing.T) {
	spent, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-spent.Done()
	ctx := context.Background()
	for _, c := range []struct {
		budget context.Context
		err    error
		want   string
	}{
		{spent, nil, ""},
		{ctx, nil, ""},
		{spent, errors.New("read: connection reset"), fmt.Sprintf("Kea did not answer within %v", askTimeout)},
		{ctx, os.ErrDeadlineExceeded, fmt.Sprintf("Kea did not answer within %v", askTimeout)},
		{ctx, errors.New("read: connection reset"), "read: connection reset"},
	} {
		if got := keaFailure(ctx, c.budget, c.err); got != c.want {
			t.Errorf("keaFailure(%v, %v) = %q, want %q", c.budget.Err(), c.err, got, c.want)
		}
	}
}
