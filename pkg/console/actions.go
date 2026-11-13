package console

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/statusview"
)

// action is one of the console's actions.
type action struct {
	key, title string
	// unavailable says why the action does not apply now; empty when it does.
	unavailable string
	// confirm is the question that names what the action does.
	confirm string
	// verb names the action in the confirmation's keys and in its failure.
	verb string
	run  func(ctx context.Context, c Client) (string, error)
}

// menu is the open actions menu: the list, or the confirmation of the action whose key is chosen.
type menu struct {
	selected int
	chosen   string
}

// actionDoneMsg is the result or failure of an action, for the status line.
type actionDoneMsg struct{ note string }

// actionTimeout bounds an action's call; a rollback of the config runs the delivery's effects.
const actionTimeout = 5 * time.Minute

// actions are the console's actions for the node as last read.
func (m *Model) actions() []action {
	info, st := m.node.Info.Value, m.node.Status.Value
	name := oneLine(cmp.Or(info.GetNodeName(), info.GetHostname(), "the node"))
	warning := m.powerWarning()
	return []action{
		{
			key: "r", title: "Reboot", verb: "reboot",
			confirm: "Reboot " + name + "? It goes down at once and boots the image and config it runs." + warning,
			run:     func(ctx context.Context, c Client) (string, error) { return "the node reboots", c.Reboot(ctx) },
		},
		{
			key: "s", title: "Shut down", verb: "shut down",
			confirm: "Shut down " + name + "? It stays off until someone powers it on." + warning,
			run:     func(ctx context.Context, c Client) (string, error) { return "the node shuts down", c.Shutdown(ctx) },
		},
		m.rollbackImage(info, st, name),
		m.rollbackConfig(info, st, name),
	}
}

// powerWarning is what the layers' tabs warn of before a reboot or a shutdown, as sentences after
// the question.
func (m *Model) powerWarning() string {
	var warning string
	for _, i := range m.shown {
		if w, ok := m.tabs[i].(PowerWarner); ok {
			if s := w.PowerWarning(); s != "" {
				warning += " " + s
			}
		}
	}
	return warning
}

// unavailableBeforeStatus says why a rollback does not apply without the node's status.
func (m *Model) unavailableBeforeStatus(info *nodev1.InfoResponse) string {
	switch {
	case info.GetInstaller():
		return "the installer runs no image of its own"
	case !m.node.Normal():
		return "the node is not installed"
	case m.node.Status.Value == nil:
		return "the node's status is not read yet"
	}
	return ""
}

func (m *Model) rollbackImage(info *nodev1.InfoResponse, st *nodev1.StatusResponse, name string) action {
	a := action{key: "i", title: "Roll back the image", verb: "roll back the image", unavailable: m.unavailableBeforeStatus(info)}
	prev := st.GetBoot().GetPrevious()
	if a.unavailable == "" && prev == nil {
		a.unavailable = "the node keeps no previous image"
	}
	if a.unavailable != "" {
		return a
	}
	image := statusview.BootImageName(prev)
	a.confirm = "Roll back " + name + " to the image " + image + "? It reboots into it, with the config it last ran."
	a.run = func(ctx context.Context, c Client) (string, error) {
		resp, err := c.RollbackImage(ctx, prev.GetRootHash())
		if resp.GetAlreadyInstalled() {
			return "the node runs " + image + " already", err
		}
		return "the node reboots into " + image, err
	}
	return a
}

func (m *Model) rollbackConfig(info *nodev1.InfoResponse, st *nodev1.StatusResponse, name string) action {
	a := action{key: "c", title: "Roll back the config", verb: "roll back the config", unavailable: m.unavailableBeforeStatus(info)}
	version := st.GetPreviousConfigVersion()
	if tr := st.GetTrial(); tr != nil {
		version = tr.GetPreviousVersion()
	}
	if a.unavailable == "" && version == "" {
		a.unavailable = "the node keeps no previous config"
	}
	if a.unavailable != "" {
		return a
	}
	short := version[:min(12, len(version))]
	a.confirm = "Roll back the config of " + name + " to " + short + "? The node delivers it again, and the units its effects name restart."
	a.run = func(ctx context.Context, c Client) (string, error) {
		resp, err := c.RollbackConfig(ctx, version)
		return fmt.Sprintf("config rolled back to %s; %s restarted", short, count(len(resp.GetRestartedUnits()), "unit")), err
	}
	return a
}

// openMenu opens the actions, or says why it does not: the image turns them off, or one runs.
func (m *Model) openMenu() {
	switch {
	case !m.o.Actions:
		m.note = WarnStyle.Render("the image turns the console's actions off (chalkos.console.actions.enable)")
	case m.running:
		m.note = WarnStyle.Render("an action runs; its result comes here")
	default:
		m.menu = &menu{}
	}
}

// find is the action of key in the actions for the node as last read.
func (m *Model) find(key string) action {
	for _, a := range m.actions() {
		if a.key == key {
			return a
		}
	}
	return action{}
}

// menuKey handles a key while the menu is open: the arrows choose, Enter or an action's letter
// picks it, y confirms it, and Enter, Esc or n step back. A confirmed action acts on the node as
// last read, which may have changed since it was picked.
func (m *Model) menuKey(k tea.KeyMsg) tea.Cmd {
	if m.menu.chosen != "" {
		switch k.String() {
		case "y":
			a := m.find(m.menu.chosen)
			if a.unavailable != "" {
				m.menu.chosen = ""
				m.note = WarnStyle.Render(a.title + ": " + a.unavailable)
				return nil
			}
			m.menu, m.running = nil, true
			m.note = a.title + "..."
			c, run, verb := m.c, a.run, a.verb
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
				defer cancel()
				result, err := run(ctx, c)
				if err != nil {
					return actionDoneMsg{ErrorStyle.Render(oneLine(verb + " failed: " + err.Error()))}
				}
				return actionDoneMsg{OKStyle.Render(oneLine(result))}
			}
		case "enter", "esc", "n":
			m.menu.chosen = ""
		}
		return nil
	}
	list := m.actions()
	switch s := k.String(); s {
	case "up":
		m.menu.selected = (m.menu.selected + len(list) - 1) % len(list)
	case "down":
		m.menu.selected = (m.menu.selected + 1) % len(list)
	case "esc", "a", "f10":
		m.menu = nil
	case "enter":
		m.choose(list[m.menu.selected])
	default:
		for _, a := range list {
			if a.key == s {
				m.choose(a)
			}
		}
	}
	return nil
}

// choose asks for the confirmation of a, unless it does not apply.
func (m *Model) choose(a action) {
	if a.unavailable == "" {
		m.menu.chosen = a.key
	}
}

// menuView is the open menu: the actions, those that do not apply greyed out with the reason, or
// the confirmation of the one chosen.
func (m *Model) menuView(width, height int) string {
	if m.menu.chosen != "" {
		a := m.find(m.menu.chosen)
		question := a.confirm
		if a.unavailable != "" {
			question = GreyStyle.Render(a.title + ": " + a.unavailable)
		}
		return block(ansi.Wordwrap(question, width, "")+"\n\ny: "+a.verb+"   Enter or Esc: back", width, height)
	}
	list := m.actions()
	rows := []string{TitleStyle.Render("Actions")}
	titleWidth := 0
	for _, a := range list {
		titleWidth = max(titleWidth, len(a.title))
	}
	for i, a := range list {
		mark := "  "
		if i == m.menu.selected {
			mark = "> "
		}
		row := mark + a.key + "  " + a.title
		if a.unavailable != "" {
			row = GreyStyle.Render(fmt.Sprintf("%s%s  %-*s    %s", mark, a.key, titleWidth, a.title, a.unavailable))
		}
		if i == m.menu.selected {
			row = selectedStyle.Render(row)
		}
		rows = append(rows, row)
	}
	rows = append(rows, "", "Enter or a letter: choose   Esc: close")
	return block(strings.Join(rows, "\n"), width, height)
}
