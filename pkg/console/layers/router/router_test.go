package router_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/charmbracelet/x/exp/teatest"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/consoletest"
	"github.com/trevex/chalkos/pkg/console/layers/router"
	"github.com/trevex/chalkos/pkg/layers"
)

// statusOf is a router layer that reports st.
type statusOf struct{ st *routerv1.StatusResponse }

func (s statusOf) Status(context.Context, *connect.Request[routerv1.StatusRequest]) (*connect.Response[routerv1.StatusResponse], error) {
	return connect.NewResponse(s.st), nil
}

// status is a router with two BGP sessions, NAT64 instance edge and a DHCPv4 server with one
// subnet; without NAT64 when nat64 is false.
func status(nat64 bool) *routerv1.StatusResponse {
	st := &routerv1.StatusResponse{
		Peers: []*routerv1.BGPPeerStatus{
			{Name: "fabric", Neighbor: "eth1", State: "Established", UptimeSeconds: 3723, PrefixesReceived: 1, PrefixesSent: 2, Bfd: "up"},
			{Name: "upstream", Neighbor: "203.0.113.254", State: "Active"},
		},
		Dhcp: []*routerv1.DHCPServerStatus{
			{Vrf: "default", Family: "ipv4", Running: true, Subnets: []*routerv1.DHCPSubnetStatus{{Name: "office", Subnet: "192.0.2.0/24", Leased: 2, Pool: 100}}},
		},
	}
	if nat64 {
		st.Nat64 = []*routerv1.NAT64Status{{Name: "edge", Sessions: 2, Bindings: 2, Pools: []*routerv1.NAT64PoolStatus{{Protocol: "tcp", Size: 4535}, {Protocol: "udp", Size: 4535}}}}
	}
	return st
}

// The diagnostics' outputs: Kea's leases and wg show.
const (
	leases = "VRF\tSUBNET\tADDRESS\tMAC\tHOSTNAME\tENDS\n" +
		"default\toffice\t192.0.2.10\t52:54:00:aa:bb:01\tlaptop\t2026-11-13T19:04:05Z\n" +
		"default\toffice\t192.0.2.11\t52:54:00:aa:bb:02\t-\t2026-11-13T18:30:00Z\n"
	wireguard = "interface: wg0\n" +
		"  public key: uLRrWL5pg3FX2ydGsAsUrQ/VvWautg0OJpoJEGP12XM=\n" +
		"  private key: (hidden)\n" +
		"  listening port: 51820\n" +
		"\n" +
		"peer: TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU=\n" +
		"  endpoint: 198.51.100.7:41641\n" +
		"  allowed ips: 2001:db8:f0::2/128\n" +
		"  latest handshake: 1 minute, 3 seconds ago\n" +
		"  transfer: 9.11 KiB received, 12.40 KiB sent\n"
)

// routerNode is an installed router with the diagnostics of DHCP and WireGuard.
func routerNode() *consoletest.Client {
	c := consoletest.Normal()
	c.InfoResp.Layers = []string{layers.Router}
	c.Diagnostics[consoletest.Key("dhcp-leases", nil)] = leases
	c.Diagnostics[consoletest.Key("wireguard", nil)] = wireguard
	return c
}

// start runs the console of c with the Router tab of st and shows the tab.
func start(t *testing.T, c *consoletest.Client, st *routerv1.StatusResponse) *teatest.TestModel {
	t.Helper()
	o := consoletest.Options()
	o.Tabs = []console.Tab{router.New(statusOf{st})}
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "5 Router")
	consoletest.Press(tm, "5")
	return tm
}

// The tab shows the layer's status section, with the NAT64 sessions' counts, then the DHCP leases
// and the WireGuard peers.
func TestRouterTab(t *testing.T) {
	tm := start(t, routerNode(), status(true))
	consoletest.WaitFor(t, tm, "nat64-sessions instance=")
	consoletest.Press(tm, "end")
	consoletest.WaitFor(t, tm, "latest handshake: 1 minute, 3 seconds ago")
	consoletest.Snapshot(t, tm)
}

// The tab's first screen holds the status section, with the command that lists the NAT64
// sessions, and the leases.
func TestRouterTabTop(t *testing.T) {
	tm := start(t, routerNode(), status(true))
	consoletest.WaitFor(t, tm, "nat64-sessions instance=")
	consoletest.Snapshot(t, tm)
}

// An image without NAT64, DHCP or WireGuard shows none of their parts.
func TestRouterTabWithoutFeatures(t *testing.T) {
	c := consoletest.Normal()
	c.InfoResp.Layers = []string{layers.Router}
	tm := start(t, c, status(false))
	consoletest.WaitFor(t, tm, "bgp:")
	screen := consoletest.Snapshot(t, tm)
	for _, heading := range []string{"nat64", "dhcp leases", "wireguard"} {
		if strings.Contains(screen, heading) {
			t.Errorf("the screen shows %q:\n%s", heading, screen)
		}
	}
}

// A diagnostic that fails shows its error in place of its output, and the other parts as before.
func TestRouterTabFailingDiagnostic(t *testing.T) {
	c := routerNode()
	c.DiagnosticErrs = map[string]error{
		consoletest.Key("dhcp-leases", nil): connect.NewError(connect.CodePermissionDenied, errors.New("the console may not run dhcp-leases\x1b[2J")),
	}
	tm := start(t, c, status(true))
	consoletest.WaitFor(t, tm, "nat64-sessions instance=")
	consoletest.Press(tm, "end")
	consoletest.WaitFor(t, tm, "dhcp leases: permission_denied")
	screen := consoletest.Snapshot(t, tm)
	if !strings.Contains(screen, `dhcp-leases\x1b[2J`) {
		t.Errorf("the error is not escaped:\n%s", screen)
	}
}

// The tab shows only on images with the router layer.
func TestRouterTabNeedsTheLayer(t *testing.T) {
	if router.New(statusOf{status(true)}).Applies(consoletest.Normal().InfoResp) {
		t.Error("the tab applies to an image without the layer")
	}
}

// The tab reads while on screen, at most every 10 seconds, and none while hidden, before the node
// is installed or while the console is idle.
func TestRouterTabReads(t *testing.T) {
	c := routerNode()
	tab := router.New(statusOf{status(true)})
	tab.Init(c)
	at := func(d time.Duration) time.Time { return consoletest.Now.Add(d) }
	if _, cmd := tab.Update(console.TickMsg{Shown: true, Now: at(0)}); cmd != nil {
		t.Error("a read before the node's Info")
	}
	tab.Update(console.NodeMsg{Info: console.Source[*nodev1.InfoResponse]{Value: c.InfoResp, At: at(0)}})
	_, cmd := tab.Update(console.TickMsg{Shown: true, Now: at(0)})
	if cmd == nil {
		t.Fatal("no read on screen")
	}
	tab.Update(cmd())
	for _, tc := range []struct {
		tick console.TickMsg
		read bool
	}{
		{console.TickMsg{Shown: true, Now: at(9 * time.Second)}, false},
		{console.TickMsg{Shown: false, Now: at(time.Hour)}, false},
		{console.TickMsg{Shown: true, Idle: true, Now: at(time.Hour)}, false},
		{console.TickMsg{Shown: true, Now: at(10 * time.Second)}, true},
	} {
		if _, cmd := tab.Update(tc.tick); (cmd != nil) != tc.read {
			t.Errorf("%+v: read %v, want %v", tc.tick, cmd != nil, tc.read)
		}
	}
}

// A DHCP server without leases shows none.
func TestRouterTabNoLeases(t *testing.T) {
	c := routerNode()
	c.Diagnostics[consoletest.Key("dhcp-leases", nil)] = "VRF\tSUBNET\tADDRESS\tMAC\tHOSTNAME\tENDS\n"
	c.Diagnostics[consoletest.Key("wireguard", nil)] = ""
	tm := start(t, c, status(false))
	consoletest.WaitFor(t, tm, "dhcp leases:")
	if screen := consoletest.Snapshot(t, tm); !strings.Contains(screen, "dhcp leases:\n  none\nwireguard:\n  none\n") {
		t.Errorf("no leases and no interfaces show as:\n%s", screen)
	}
}
