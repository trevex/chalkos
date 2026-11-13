package console_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/consoletest"
)

// The actions open with a; one that does not apply is greyed out with the reason.
func TestActionsMenu(t *testing.T) {
	c := consoletest.Normal()
	c.StatusResp.Boot.Previous = nil
	o := consoletest.Options()
	o.Actions = true
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "a")
	consoletest.WaitFor(t, tm, "the node keeps no previous image")
	consoletest.Snapshot(t, tm)
}

// An action asks first and names what it does; y alone confirms it.
func TestActionConfirmation(t *testing.T) {
	o := consoletest.Options()
	o.Actions = true
	tm := consoletest.Start(t, console.New(consoletest.Normal(), o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "f10")
	consoletest.Press(tm, "c")
	consoletest.WaitFor(t, tm, "y: roll back the config")
	consoletest.Snapshot(t, tm)
}

// A confirmed action runs and its result shows in the status line.
func TestActionResult(t *testing.T) {
	c := consoletest.Normal()
	o := consoletest.Options()
	o.Actions = true
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "c")
	consoletest.Press(tm, "y")
	consoletest.WaitFor(t, tm, "config rolled back")
	if calls := c.Calls(); !slices.Equal(calls, []string{"RollbackConfig " + c.StatusResp.PreviousConfigVersion}) {
		t.Errorf("calls %q", calls)
	}
	consoletest.Snapshot(t, tm)
}

// With the actions off in the image, a says so and nothing opens.
func TestActionsOff(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "a")
	consoletest.WaitFor(t, tm, "the image turns the console's actions off")
	consoletest.Snapshot(t, tm)
}

// An action that fails says why.
func TestActionError(t *testing.T) {
	c := consoletest.Normal()
	c.ActionErr = connect.NewError(connect.CodePermissionDenied, errors.New("the image turns the console's actions off"))
	o := consoletest.Options()
	o.Actions = true
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "r")
	consoletest.Press(tm, "y")
	consoletest.WaitFor(t, tm, "reboot failed")
	consoletest.Snapshot(t, tm)
}

// Enter steps back from the confirmation: y alone confirms.
func TestActionEnterDoesNotConfirm(t *testing.T) {
	c := consoletest.Normal()
	o := consoletest.Options()
	o.Actions = true
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "r")
	consoletest.WaitFor(t, tm, "y: reboot")
	consoletest.Press(tm, "enter")
	consoletest.WaitFor(t, tm, "Enter or a letter: choose")
	consoletest.Snapshot(t, tm)
	if calls := c.Calls(); len(calls) > 0 {
		t.Errorf("calls %q", calls)
	}
}

// While an action runs, the menu stays closed, so a second action waits for the first.
func TestActionRunsAlone(t *testing.T) {
	c := consoletest.Normal()
	c.ActionBlock = make(chan struct{})
	o := consoletest.Options()
	o.Actions = true
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "r")
	consoletest.Press(tm, "y")
	consoletest.WaitFor(t, tm, "Reboot...")
	consoletest.Press(tm, "a")
	consoletest.WaitFor(t, tm, "an action runs")
	consoletest.Press(tm, "s")
	close(c.ActionBlock)
	consoletest.WaitFor(t, tm, "the node reboots")
	if calls := c.Calls(); !slices.Equal(calls, []string{"Reboot"}) {
		t.Errorf("calls %q", calls)
	}
}

// A confirmed rollback acts on the node as last read, which may have changed since the action was
// picked.
func TestActionReadsTheTargetWhenConfirmed(t *testing.T) {
	c := consoletest.Normal()
	o := consoletest.Options()
	o.Actions = true
	o.Interval = 50 * time.Millisecond
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "c")
	consoletest.WaitFor(t, tm, "to 1b2c3d4e5f60?")
	c.Set(func(c *consoletest.Client) {
		// The console holds the status it read, so the node reports a new one.
		s := proto.Clone(c.StatusResp).(*nodev1.StatusResponse)
		s.PreviousConfigVersion = "99aa99aa99aa"
		c.StatusResp = s
	})
	consoletest.WaitFor(t, tm, "to 99aa99aa99aa?")
	consoletest.Press(tm, "y")
	consoletest.WaitFor(t, tm, "config rolled back to 99aa99aa99aa")
	if calls := c.Calls(); !slices.Equal(calls, []string{"RollbackConfig 99aa99aa99aa"}) {
		t.Errorf("calls %q", calls)
	}
}

// An error of many lines stays on its one line, below the header.
func TestActionErrorOfManyLines(t *testing.T) {
	c := consoletest.Normal()
	c.ActionErr = errors.New("first line\nsecond line\nthird line")
	o := consoletest.Options()
	o.Actions = true
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "r")
	consoletest.Press(tm, "y")
	consoletest.WaitFor(t, tm, "third line")
	screen := consoletest.Snapshot(t, tm)
	if rows := strings.Split(screen, "\n"); len(rows) != 25 || !strings.HasPrefix(rows[0], "node1") {
		t.Errorf("the screen has %d rows, the first %q", len(rows), rows[0])
	}
}
