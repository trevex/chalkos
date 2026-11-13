package console

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/statusview"
)

// overview is the Overview tab: what chalkctl status prints, without the inventory's comparisons,
// which the console has no inventory for, and without the layers' sections, which their own tabs
// show.
type overview struct {
	info   Source[*nodev1.InfoResponse]
	status Source[*nodev1.StatusResponse]
	normal bool
	pane   Pane
}

func newOverview() *overview { return &overview{} }

func (o *overview) Title() string                     { return "Overview" }
func (o *overview) Applies(*nodev1.InfoResponse) bool { return true }
func (o *overview) Init(Client) tea.Cmd               { return nil }

func (o *overview) Update(msg tea.Msg) (Tab, tea.Cmd) {
	switch msg := msg.(type) {
	case NodeMsg:
		o.info, o.status, o.normal = msg.Info, msg.Status, msg.Normal()
	case tea.KeyMsg:
		o.pane.Key(msg)
	}
	return o, nil
}

func (o *overview) View(width, height int) string {
	var b strings.Builder
	info := o.info.Value
	switch {
	case o.status.Value == nil && o.info.Err != nil:
		b.WriteString("Waiting for chalkd, which does not answer: " + OneLine(o.info.Err.Error()) + "\n")
	case info.GetInstaller():
		b.WriteString("The installer writes nodes' images to other disks; it has no status of its own.\n")
		b.WriteString("The Storage tab lists the disks it can install onto.\n")
	case info != nil && !o.normal:
		b.WriteString("The node is not installed, so it has no status yet.\n")
		b.WriteString("The Storage tab lists its disks; chalkctl install installs it.\n")
	case o.status.Value == nil && o.status.Err == nil:
		b.WriteString("Reading the node's status...\n")
	}
	if p := o.status.Problem("status"); p != "" {
		b.WriteString(ErrorStyle.Render(p) + "\n")
	}
	if s := o.status.Value; s != nil {
		var text strings.Builder
		if err := statusview.Write(&text, s, statusview.Options{Node: info.GetNodeName()}); err != nil {
			fmt.Fprintln(&text, err)
		}
		b.WriteString(Clean(text.String()))
	}
	return o.pane.View(b.String(), width, height)
}
