package console_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/charmbracelet/x/ansi"

	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/consoletest"
)

// The Logs tab follows the journal of the boot and marks the errors.
func TestLogs(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "4")
	consoletest.WaitFor(t, tm, "delivered")
	consoletest.Snapshot(t, tm)
}

// u picks a unit from the active ones, whose journal the tab then shows alone.
func TestLogsFilter(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "4")
	consoletest.WaitFor(t, tm, "delivered")
	consoletest.Press(tm, "u")
	consoletest.WaitFor(t, tm, "chronyd.service")
	consoletest.Press(tm, "down")
	consoletest.Press(tm, "down")
	consoletest.Press(tm, "enter")
	consoletest.WaitFor(t, tm, "unit chronyd.service", "System clock wrong")
	consoletest.Snapshot(t, tm)
}

// / searches the lines and shows the first match.
func TestLogsSearch(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "4")
	consoletest.WaitFor(t, tm, "delivered")
	consoletest.Press(tm, "/")
	consoletest.Press(tm, "apply")
	consoletest.Press(tm, "enter")
	consoletest.WaitFor(t, tm, "1 match")
	consoletest.Snapshot(t, tm)
}

// w wraps the lines longer than the screen.
func TestLogsWrap(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "4")
	consoletest.WaitFor(t, tm, "delivered")
	consoletest.Press(tm, "w")
	consoletest.WaitFor(t, tm, "wrap on")
	consoletest.Snapshot(t, tm)
}

// While chalkd does not answer, the console reads the journal itself.
func TestLogsReadsTheJournalItself(t *testing.T) {
	c := consoletest.Normal()
	c.LogsErr = connect.NewError(connect.CodeUnavailable, errors.New("dial unix /run/chalkd/console.sock: connect: connection refused"))
	o := consoletest.Options()
	o.Journal = func(ctx context.Context, _ string, line func(string)) error {
		line("2026-11-13T11:52:01+00:00 node1 systemd[1]: Started chalkd.service.")
		line("2026-11-13T11:52:03+00:00 node1 chalkd[612]: read the node's state")
		<-ctx.Done()
		return nil
	}
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "4")
	consoletest.WaitFor(t, tm, "read by the console", "read the node's state")
	consoletest.Snapshot(t, tm)
}

// The Logs tab reads the journal once it first comes on screen.
func TestLogsStartWhenShown(t *testing.T) {
	c := consoletest.Normal()
	tm := consoletest.Start(t, console.New(c, consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	if n := c.Streams(); n != 0 {
		t.Errorf("%d streams before the Logs tab came on screen", n)
	}
	consoletest.Press(tm, "4")
	consoletest.WaitFor(t, tm, "delivered")
	consoletest.Eventually(t, "one stream", func() bool { return c.Streams() == 1 })
}

// Tabs, escapes and other control characters of a line show as text within the screen's width,
// cut or wrapped.
func TestLogsUnprintable(t *testing.T) {
	c := consoletest.Normal()
	c.Lines = []string{
		"2026-11-13T11:53:15+00:00 node1 chalkd[612]: panic: boom",
		"2026-11-13T11:53:15+00:00 node1 chalkd[612]: \t/build/source/pkg/chalkd/server.go:412 +0x1d4 github.com/trevex/chalkos/pkg/chalkd.(*Server).Logs",
		"2026-11-13T11:53:16+00:00 node1 demo[700]: \x1b[31mred\x1b[0m\x1b[2J and \x07 a bell",
	}
	tm := consoletest.Start(t, console.New(c, consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "4")
	consoletest.WaitFor(t, tm, "demo[700]")
	consoletest.Press(tm, "w")
	consoletest.WaitFor(t, tm, "wrap on")
	screen := consoletest.Snapshot(t, tm)
	for i, row := range strings.Split(screen, "\n") {
		if strings.ContainsAny(row, "\t\x1b\x07") || ansi.StringWidth(row) > 80 {
			t.Errorf("row %d %q holds a control character or passes 80 columns", i+1, row)
		}
	}
}

// Once chalkd answers again, the tab follows its stream again; the console's own lines stay on
// screen until chalkd's come.
func TestLogsBackToChalkd(t *testing.T) {
	c := consoletest.Normal()
	c.LogsErr = connect.NewError(connect.CodeUnavailable, errors.New("dial unix /run/chalkd/console.sock: connect: connection refused"))
	clock := consoletest.NewClock()
	o := consoletest.Options()
	o.Interval = 50 * time.Millisecond
	o.Now = clock.Now
	o.Journal = func(ctx context.Context, _ string, line func(string)) error {
		line("2026-11-13T11:52:03+00:00 node1 chalkd[612]: read the node's state")
		<-ctx.Done()
		return nil
	}
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "4")
	consoletest.WaitFor(t, tm, "read by the console", "read the node's state")
	c.Set(func(c *consoletest.Client) { c.LogsErr = nil })
	clock.Advance(11 * time.Second)
	consoletest.WaitFor(t, tm, "delivered")
	screen := consoletest.Snapshot(t, tm)
	if strings.Contains(screen, "read by the console") {
		t.Error("the tab still says the console reads the journal")
	}
}

// After five minutes without a key, the console reads less and follows no journal; a key resumes.
func TestIdle(t *testing.T) {
	c := consoletest.Normal()
	clock := consoletest.NewClock()
	o := consoletest.Options()
	o.Interval = 50 * time.Millisecond
	o.Now = clock.Now
	tm := consoletest.Start(t, console.New(c, o))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "4")
	consoletest.WaitFor(t, tm, "delivered")
	consoletest.Eventually(t, "a stream", func() bool { return c.Streams() == 1 })
	clock.Advance(6 * time.Minute)
	consoletest.WaitFor(t, tm, "idle:", "paused while idle")
	consoletest.Eventually(t, "the stream's end", func() bool { return c.Streams() == 0 })
	consoletest.Press(tm, "end")
	consoletest.Eventually(t, "a stream again", func() bool { return c.Streams() == 1 })
	screen := consoletest.Snapshot(t, tm)
	if strings.Contains(screen, "idle") {
		t.Error("the screen still says the console is idle")
	}
}
