// Package router is the console's Router tab: the router layer's section of the node's status,
// with its BGP sessions and their neighbours and the session counts of its NAT64 instances, and
// the DHCP leases and WireGuard peers that the image's diagnostics show.
package router

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	tea "github.com/charmbracelet/bubbletea"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/statusview"
	routerview "github.com/trevex/chalkos/pkg/statusview/layers/router"
)

// StatusClient reads the router layer's status, as the generated client of RouterService does.
type StatusClient interface {
	Status(context.Context, *connect.Request[routerv1.StatusRequest]) (*connect.Response[routerv1.StatusResponse], error)
}

// Tab is the Router tab.
type Tab struct {
	client StatusClient
	c      console.Client
	status console.Source[*routerv1.StatusResponse]
	// parts are the diagnostics' outputs of the last read, in the order the tab shows them; a
	// diagnostic the image lacks has none.
	parts []part
	// normal is set while the node is installed and its image has the layer, as the layer
	// answers in normal mode alone.
	normal  bool
	reading bool
	// node names the node in the command the tab suggests.
	node string
	// read is when the last read started.
	read time.Time
	pane console.Pane
}

// part is the output of one diagnostic, under its heading.
type part struct {
	heading string
	// table is set for output of tab-separated columns.
	table  bool
	output console.Source[string]
}

// every is the least time between two reads, each of which runs up to a few diagnostics, each a
// transient unit. The tab reads only while on screen and the console is not idle.
const every = 10 * time.Second

// statusTimeout bounds the read of the status, and diagnosticTimeout each diagnostic's run.
const (
	statusTimeout     = 20 * time.Second
	diagnosticTimeout = 15 * time.Second
)

var _ console.Tab = (*Tab)(nil)

// New returns the tab, which reads the layer's status through client.
func New(client StatusClient) *Tab { return &Tab{client: client} }

func (t *Tab) Title() string { return "Router" }

func (t *Tab) Applies(info *nodev1.InfoResponse) bool {
	return slices.Contains(info.GetLayers(), layers.Router)
}

func (t *Tab) Init(c console.Client) tea.Cmd { t.c = c; return nil }

// readMsg is what one read gave.
type readMsg struct {
	status    *routerv1.StatusResponse
	statusErr error
	parts     []partRead
	at        time.Time
}

// partRead is one diagnostic's run; absent when the image lacks the diagnostic.
type partRead struct {
	heading string
	table   bool
	output  string
	err     error
	absent  bool
}

func (t *Tab) Update(msg tea.Msg) (console.Tab, tea.Cmd) {
	switch msg := msg.(type) {
	case console.NodeMsg:
		t.normal = msg.Normal() && t.Applies(msg.Info.Value)
		info := msg.Info.Value
		t.node = console.OneLine(cmp.Or(info.GetNodeName(), info.GetHostname(), "<node>"))
	case console.TickMsg:
		if msg.Shown && !msg.Idle && t.normal && !t.reading && (t.read.IsZero() || msg.Now.Sub(t.read) >= every) {
			t.reading, t.read = true, msg.Now
			return t, t.readAll(msg.Now)
		}
	case readMsg:
		t.reading = false
		t.status.Set(msg.status, msg.statusErr, msg.at)
		t.parts = t.merge(msg.parts, msg.at)
	case tea.KeyMsg:
		t.pane.Key(msg)
	}
	return t, nil
}

// merge is the parts of a read, each keeping the output a part showed before when its run
// failed.
func (t *Tab) merge(reads []partRead, at time.Time) []part {
	var parts []part
	for _, r := range reads {
		if r.absent {
			continue
		}
		p := part{heading: r.heading, table: r.table}
		if i := slices.IndexFunc(t.parts, func(old part) bool { return old.heading == r.heading }); i >= 0 {
			p.output = t.parts[i].output
		}
		p.output.Set(r.output, r.err, at)
		parts = append(parts, p)
	}
	return parts
}

// readAll reads the status, then runs the diagnostics one after the other, as chalkd runs few at
// a time: the DHCP leases and the WireGuard interfaces. The status counts each NAT64 instance's
// sessions; the tab lists none, as a busy instance's list exceeds what a diagnostic may print.
func (t *Tab) readAll(now time.Time) tea.Cmd {
	client, c := t.client, t.c
	return func() tea.Msg {
		r := readMsg{at: now}
		ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
		resp, err := client.Status(ctx, connect.NewRequest(&routerv1.StatusRequest{}))
		cancel()
		if err != nil {
			r.statusErr = err
		} else {
			r.status = resp.Msg
		}
		run := func(heading string, table bool, name string) {
			ctx, cancel := context.WithTimeout(context.Background(), diagnosticTimeout)
			defer cancel()
			out, err := c.Diagnostic(ctx, name, nil)
			r.parts = append(r.parts, partRead{heading: heading, table: table, output: string(out), err: err, absent: connect.CodeOf(err) == connect.CodeNotFound})
		}
		run("dhcp leases", true, "dhcp-leases")
		run("wireguard", false, "wireguard")
		return r
	}
}

func (t *Tab) View(width, height int) string {
	var b strings.Builder
	switch {
	case !t.normal:
		b.WriteString("The router runs once the node is installed.\n")
	case t.read.IsZero() || t.status.Value == nil && t.status.Err == nil:
		b.WriteString("Reading the router's status...\n")
	}
	if p := t.status.Problem("router status"); p != "" {
		b.WriteString(console.ErrorStyle.Render(p) + "\n")
	}
	if s := t.status.Value; s != nil {
		var section strings.Builder
		if err := routerview.Write(&section, s); err != nil {
			section.WriteString(err.Error() + "\n")
		}
		b.WriteString(console.Clean(section.String()))
		if len(s.Nat64) > 0 {
			b.WriteString(console.GreyStyle.Render("nat64 sessions: chalkctl node diag "+t.node+" nat64-sessions instance=<name> lists them") + "\n")
		}
	}
	for _, p := range t.parts {
		if problem := p.output.Problem(p.heading); problem != "" {
			b.WriteString(console.ErrorStyle.Render(problem) + "\n")
			if p.output.At.IsZero() {
				continue
			}
		} else {
			b.WriteString(p.heading + ":\n")
		}
		if p.table {
			writeTable(&b, p.output.Value)
		} else {
			writeIndented(&b, p.output.Value)
		}
	}
	return t.pane.View(b.String(), width, height)
}

// writeTable writes output of tab-separated columns aligned and indented by two spaces, or none
// when it has no line past its header.
func writeTable(b *strings.Builder, output string) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) < 2 {
		b.WriteString("  none\n")
		return
	}
	tw := statusview.Table(b)
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		for i, f := range fields {
			fields[i] = console.OneLine(f)
		}
		fmt.Fprintln(tw, "  "+strings.Join(fields, "\t"))
	}
	tw.Flush()
}

// writeIndented writes output indented by two spaces, without its empty lines, or none when it
// has no others.
func writeIndented(b *strings.Builder, output string) {
	none := true
	for _, line := range strings.Split(console.Clean(output), "\n") {
		if strings.TrimSpace(line) != "" {
			b.WriteString("  " + line + "\n")
			none = false
		}
	}
	if none {
		b.WriteString("  none\n")
	}
}
