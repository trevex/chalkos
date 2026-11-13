package console_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/consoletest"
)

// The Overview shows what chalkctl status prints, without the inventory's comparisons.
func TestOverview(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "VOLUME")
	consoletest.Snapshot(t, tm)
}

// PgDn scrolls the Overview to the end of the status below the screen.
func TestOverviewScrolls(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "VOLUME")
	consoletest.Press(tm, "pgdown")
	consoletest.WaitFor(t, tm, "time: synchronised")
	consoletest.Snapshot(t, tm)
}

// A status that fails after a good read keeps showing the last good one, with the error and its
// time; the other panels go on. The clock stays at one time, so every read carries it and the
// error's time is the same however many reads ran before the failure.
func TestOverviewFailingSource(t *testing.T) {
	c := consoletest.Normal()
	o := consoletest.Options()
	o.Interval = 50 * time.Millisecond
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "VOLUME")
	c.Set(func(c *consoletest.Client) {
		c.StatusErr = connect.NewError(connect.CodeDeadlineExceeded, errors.New("chronyc timed out"))
	})
	consoletest.WaitFor(t, tm, "chronyc timed out")
	consoletest.Snapshot(t, tm)
}

// In maintenance mode the node has no status; the tab says where to look instead.
func TestOverviewMaintenance(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Maintenance(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "not installed")
	consoletest.Snapshot(t, tm)
}
