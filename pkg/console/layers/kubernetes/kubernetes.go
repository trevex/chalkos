// Package kubernetes is the console's Kubernetes tab: the layer's section of the node's status,
// with the Node's Ready condition and the pods that do not run, and the warning before a reboot
// or a shutdown of a control plane.
package kubernetes

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	tea "github.com/charmbracelet/bubbletea"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/layers"
	kubernetesview "github.com/trevex/chalkos/pkg/statusview/layers/kubernetes"
)

// StatusClient reads the Kubernetes layer's status, as the generated client of KubernetesService
// does.
type StatusClient interface {
	Status(context.Context, *connect.Request[kubernetesv1.StatusRequest]) (*connect.Response[kubernetesv1.StatusResponse], error)
}

// Tab is the Kubernetes tab.
type Tab struct {
	client StatusClient
	status console.Source[*kubernetesv1.StatusResponse]
	// normal is set while the node is installed and its image has the layer, as the layer
	// answers in normal mode alone.
	normal  bool
	reading bool
	// read is when the last read started.
	read time.Time
	pane console.Pane
}

// The tab reads the status every shownEvery while on screen and every hiddenEvery while not,
// which keeps the warning before a reboot current; while the console is idle it reads none. Each
// read asks the API server for the Node and the node's pods.
const (
	shownEvery  = 5 * time.Second
	hiddenEvery = 30 * time.Second
)

// readTimeout bounds a read; the layer bounds its own reads of the API server.
const readTimeout = 20 * time.Second

var (
	_ console.Tab         = (*Tab)(nil)
	_ console.PowerWarner = (*Tab)(nil)
)

// New returns the tab, which reads the layer's status through client.
func New(client StatusClient) *Tab { return &Tab{client: client} }

func (t *Tab) Title() string { return "Kubernetes" }

func (t *Tab) Applies(info *nodev1.InfoResponse) bool {
	return slices.Contains(info.GetLayers(), layers.Kubernetes)
}

func (t *Tab) Init(console.Client) tea.Cmd { return nil }

// statusMsg is a read of the layer's status.
type statusMsg struct {
	status *kubernetesv1.StatusResponse
	err    error
	at     time.Time
}

func (t *Tab) Update(msg tea.Msg) (console.Tab, tea.Cmd) {
	switch msg := msg.(type) {
	case console.NodeMsg:
		t.normal = msg.Normal() && t.Applies(msg.Info.Value)
		// The first read follows the node's, so that the warning holds before the tab is shown.
		if t.read.IsZero() {
			return t, t.readAt(msg.Info.At)
		}
	case console.TickMsg:
		every := hiddenEvery
		if msg.Shown {
			every = shownEvery
		}
		if !msg.Idle && msg.Now.Sub(t.read) >= every {
			return t, t.readAt(msg.Now)
		}
	case statusMsg:
		t.reading = false
		t.status.Set(msg.status, msg.err, msg.at)
	case tea.KeyMsg:
		t.pane.Key(msg)
	}
	return t, nil
}

// readAt reads the status at now, unless the node is not installed or a read runs.
func (t *Tab) readAt(now time.Time) tea.Cmd {
	if !t.normal || t.reading {
		return nil
	}
	t.reading, t.read = true, now
	client := t.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		resp, err := client.Status(ctx, connect.NewRequest(&kubernetesv1.StatusRequest{}))
		if err != nil {
			return statusMsg{err: err, at: now}
		}
		return statusMsg{status: resp.Msg, at: now}
	}
}

func (t *Tab) View(width, height int) string {
	var b strings.Builder
	// line writes a line of text wrapped to width, each row passed through style.
	line := func(text string, style func(string) string) {
		for row := range strings.SplitSeq(console.Wrap(text, width), "\n") {
			b.WriteString(style(row) + "\n")
		}
	}
	plain := func(s string) string { return s }
	errorStyle := func(s string) string { return console.ErrorStyle.Render(s) }
	switch {
	case !t.normal:
		line("Kubernetes runs once the node is installed.", plain)
	case t.status.Value == nil && t.status.Err == nil:
		line("Reading the Kubernetes status...", plain)
	}
	if p := t.status.Problem("kubernetes status"); p != "" {
		line(p, errorStyle)
	}
	k := t.status.Value
	if k == nil {
		return t.pane.View(b.String(), width, height)
	}
	for _, l := range kubernetesview.StatusLines(k) {
		line(console.OneLine(l), plain)
	}
	var revoked strings.Builder
	if err := kubernetesview.WriteRevoked(&revoked, k); err != nil {
		revoked.WriteString(err.Error() + "\n")
	}
	b.WriteString(console.Clean(revoked.String()))
	switch {
	case k.PodsError != "":
		line(console.OneLine("pods unknown: "+k.PodsError), errorStyle)
	case len(k.PodsNotRunning) > 0:
		b.WriteString("pods not running:\n")
		for _, p := range k.PodsNotRunning {
			line(console.OneLine(strings.TrimRight(fmt.Sprintf("  %s/%s: %s %s", p.Namespace, p.Name, p.Phase, p.Reason), " ")), plain)
		}
		if n := kubernetesview.MorePods(k); n > 0 {
			line(fmt.Sprintf("  and %d more", n), plain)
		}
	}
	return t.pane.View(b.String(), width, height)
}

// PowerWarning warns on a control plane that the console reboots and shuts down without
// chalkctl's drain and etcd quorum check, and so while the layer's status, which tells the
// node's kind, could not be read yet.
func (t *Tab) PowerWarning() string {
	const skips = "chalkctl node reboot drains the node and checks etcd's quorum first; the console does neither."
	switch {
	case !t.normal:
		return ""
	case t.status.Value == nil:
		return "The node may be a control plane: " + skips
	case t.status.Value.GetKind() == "controlplane":
		return skips
	}
	return ""
}
