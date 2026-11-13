package console_test

import (
	"errors"
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
