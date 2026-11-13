package console_test

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/consoletest"
)

func TestHeaderNormal(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Snapshot(t, tm)
}

// In maintenance mode the header keeps the fingerprint and the addresses beyond link-local on
// screen, which install needs.
func TestHeaderMaintenance(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Maintenance(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "2001:db8::50")
	consoletest.Snapshot(t, tm)
}

func TestHeaderInstaller(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Installer(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "2001:db8::50")
	consoletest.Snapshot(t, tm)
}

// While chalkd does not answer, the header says so and the console keeps trying.
func TestChalkdDoesNotAnswer(t *testing.T) {
	c := consoletest.Normal()
	c.InfoErr = connect.NewError(connect.CodeUnavailable, errors.New("dial unix /run/chalkd/console.sock: connect: connection refused"))
	tm := consoletest.Start(t, console.New(c, consoletest.Options()))
	consoletest.WaitFor(t, tm, "chalkd does not answer")
	consoletest.Snapshot(t, tm)
}

// Tab, Shift-Tab, Left, Right and the number keys switch tabs.
func TestSwitchTabs(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "tab")
	consoletest.WaitFor(t, tm, "DNS servers")
	consoletest.Press(tm, "3")
	consoletest.WaitFor(t, tm, "ENCRYPTION")
	consoletest.Press(tm, "shift+tab")
	consoletest.WaitFor(t, tm, "DNS servers")
	consoletest.Press(tm, "right")
	consoletest.WaitFor(t, tm, "ENCRYPTION")
	consoletest.Press(tm, "left")
	consoletest.WaitFor(t, tm, "DNS servers")
	consoletest.Snapshot(t, tm)
}

// On a narrow screen the fingerprint breaks over rows instead of being cut.
func TestFingerprintWraps(t *testing.T) {
	m := console.New(consoletest.Maintenance(), consoletest.Options())
	tm := consoletest.Start(t, m)
	consoletest.WaitFor(t, tm, "2001:db8::50")
	tm.Send(tea.WindowSizeMsg{Width: 40, Height: 25})
	screen := consoletest.Snapshot(t, tm)
	if !strings.Contains(strings.ReplaceAll(screen, "\n            ", ""), consoletest.Maintenance().InfoResp.Fingerprint) {
		t.Errorf("the fingerprint is not on screen whole:\n%s", screen)
	}
}
