package console_test

import (
	"context"
	"errors"
	"strings"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"testing"

	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/consoletest"
)

// The Network tab shows the links but the loopback, the default routes and the DNS servers.
func TestNetwork(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "2")
	consoletest.WaitFor(t, tm, "DNS servers")
	consoletest.Snapshot(t, tm)
}

// A diagnostic that fails shows its error in place of its part; the others show theirs.
func TestNetworkFailingSource(t *testing.T) {
	c := consoletest.Normal()
	c.DiagnosticErrs = map[string]error{
		consoletest.Key("routes", map[string]string{"family": "inet6", "table": "main"}): errors.New("diagnostic routes: exit 2: Error: ipv6 is disabled"),
	}
	tm := consoletest.Start(t, console.New(c, consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "2")
	consoletest.WaitFor(t, tm, "ipv6 is disabled")
	consoletest.Snapshot(t, tm)
}

// The Network tab shows configured local BGP beside the routes, even when FRR cannot answer.
func TestNetworkBGP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status *nodev1.BGPStatus
		want   string
	}{
		{name: "absent"},
		{name: "empty", status: &nodev1.BGPStatus{}, want: "bgp: no sessions"},
		{name: "peer", status: &nodev1.BGPStatus{Peers: []*nodev1.BGPPeerStatus{
			{Name: "router-enp3s0", Neighbor: "fd00::1", State: "Active", Bfd: "down"},
		}}, want: "router-enp3s0"},
		{name: "unknown", status: &nodev1.BGPStatus{Error: "FRR did not answer"}, want: "bgp: unknown: FRR did not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := consoletest.Options()
			o.NetworkBGP = func(context.Context, *nodev1.InfoResponse) (*nodev1.BGPStatus, error) { return tc.status, nil }
			tm := consoletest.Start(t, console.New(consoletest.Normal(), o))
			consoletest.WaitFor(t, tm, "healthy")
			consoletest.Press(tm, "2")
			consoletest.WaitFor(t, tm, "DNS servers")
			view := consoletest.Screen(t, tm)
			if tc.want != "" && !strings.Contains(view, tc.want) || tc.want == "" && strings.Contains(view, "bgp:") {
				t.Fatalf("BGP section for %s:\n%s", tc.name, view)
			}
		})
	}
}
