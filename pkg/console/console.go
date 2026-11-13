// Package console is the console on a machine's screen: a live view of the node, which chalkd
// serves to it on its console socket, with a header of the node's identity and health, tabs for
// its status, network, storage and logs, the tabs of the image's layers, and actions for a node
// that chalkctl cannot reach.
package console

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// Tab is one tab of the console.
type Tab interface {
	Title() string
	// Applies tells whether the tab shows for the node's Info, such as its layers.
	Applies(info *nodev1.InfoResponse) bool
	Init(c Client) tea.Cmd
	Update(msg tea.Msg) (Tab, tea.Cmd)
	View(width, height int) string
}

// Capturer is a tab that takes every key while it reads text or a choice, such as a search.
type Capturer interface {
	Capturing() bool
}

// PowerWarner is a tab whose layer warns before the console reboots or shuts the node down, or
// rolls back its image, which reboots it.
type PowerWarner interface {
	// PowerWarning says what a reboot, a shutdown or a rollback of the image from the console leaves
	// out that chalkctl does; empty when nothing.
	PowerWarning() string
}

// Source is what one source of data gave last: its value, when it came, and why the reads since
// failed. A panel keeps showing Value while Err is set.
type Source[T any] struct {
	Value T
	At    time.Time
	Err   error
}

// Set records the result of a read at now; a failed read keeps the value before.
func (s *Source[T]) Set(v T, err error, now time.Time) {
	if err != nil {
		s.Err = err
		return
	}
	s.Value, s.At, s.Err = v, now, nil
}

// Problem says why the source fails and since when its data is old, on one line; empty while it
// does not.
func (s Source[T]) Problem(what string) string {
	switch {
	case s.Err == nil:
		return ""
	case s.At.IsZero():
		return OneLine(fmt.Sprintf("%s: %v", what, s.Err))
	}
	return OneLine(fmt.Sprintf("%s: %v; shown as of %s", what, s.Err, s.At.Local().Format(time.TimeOnly)))
}

// NodeMsg is what the console read of the node, which every tab gets after each read.
type NodeMsg struct {
	Info   Source[*nodev1.InfoResponse]
	Status Source[*nodev1.StatusResponse]
	Disks  Source[*nodev1.DisksResponse]
}

// Normal reports whether the node is installed and runs its own image: neither maintenance mode
// nor the installer.
func (n NodeMsg) Normal() bool {
	i := n.Info.Value
	return i.GetMode() == nodev1.Mode_MODE_NORMAL && !i.GetInstaller()
}

// TickMsg comes to every tab each interval, and to a tab as it comes on screen. A tab that reads
// data of its own, such as diagnostics, reads it on ticks while Shown, and stops reading while
// Idle: no key was pressed for a while, so nobody may be watching.
type TickMsg struct {
	Shown bool
	Idle  bool
	Now   time.Time
}

// Options set up a Model. Zero fields mean the node's own.
type Options struct {
	// Tabs are the layers' tabs, shown after the built-in ones while they apply.
	Tabs []Tab
	// Actions says whether the image allows the console's actions.
	Actions bool
	// Interval is the time between two reads of the node; 2 seconds by default.
	Interval time.Duration
	// Now is the clock and Uptime how long the machine has run. Tests fix them.
	Now    func() time.Time
	Uptime func() (time.Duration, error)
	// Journal follows the journal of the boot, or of unit, itself, while chalkd does not answer.
	Journal func(ctx context.Context, unit string, line func(string)) error
	// Units lists the active units, which the logs' filter offers.
	Units func(ctx context.Context) ([]string, error)
	// DNSServers lists the DNS servers the node asks.
	DNSServers func() ([]string, error)
	// PassphrasePrompt reports whether a passphrase prompt waits for an answer on the console.
	PassphrasePrompt func() bool
}

// withDefaults fills the zero fields with the node's own.
func (o Options) withDefaults() Options {
	o.Interval = cmp.Or(o.Interval, 2*time.Second)
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Uptime == nil {
		o.Uptime = uptime
	}
	if o.Journal == nil {
		o.Journal = journal
	}
	if o.Units == nil {
		o.Units = activeUnits
	}
	if o.DNSServers == nil {
		o.DNSServers = dnsServers
	}
	if o.PassphrasePrompt == nil {
		o.PassphrasePrompt = passphrasePrompt
	}
	return o
}

// readTimeout bounds one read of the node.
const readTimeout = 10 * time.Second

// idleAfter is how long the console waits for a key before it counts as idle; idleInterval is
// then the time between two reads of the node.
const (
	idleAfter    = 5 * time.Minute
	idleInterval = 30 * time.Second
)

// The addresses the header shows in maintenance mode and on the installer are read every
// addressesEvery, as each read runs a diagnostic, a transient unit; once they stayed the same for
// addressesSettle, or while the console is idle, every idleInterval.
const (
	addressesEvery  = 5 * time.Second
	addressesSettle = time.Minute
)

// Model is the console.
type Model struct {
	c             Client
	o             Options
	width, height int
	// tabs are the built-in tabs, then the layers'; shown indexes those that apply, and active
	// is the shown tab on screen.
	tabs   []Tab
	shown  []int
	active int
	node   NodeMsg
	// addresses are the node's addresses beyond link-local, which the header shows in
	// maintenance mode and on the installer; read when they were last read, and changed when
	// they last changed.
	addresses        Source[[]string]
	addressesRead    time.Time
	addressesChanged time.Time
	// prompt is set while a passphrase prompt waits on the console.
	prompt  bool
	reading bool
	// lastKey is when the last key came; idle is set once none came for idleAfter. tickGen numbers
	// the chain of ticks, which a key that ends the idle time starts anew.
	lastKey time.Time
	idle    bool
	tickGen int
	menu    *menu
	// running is set while an action runs.
	running bool
	// note is the result or error of the last action, or why the actions are off.
	note string
}

// New returns the console of the node c reaches.
func New(c Client, o Options) *Model {
	o = o.withDefaults()
	tabs := append([]Tab{newOverview(), newNetwork(o), newStorage(), newLogs(o)}, o.Tabs...)
	m := &Model{c: c, o: o, tabs: tabs}
	m.shown = m.applying()
	return m
}

func (m *Model) Init() tea.Cmd {
	m.lastKey = m.o.Now()
	cmds := []tea.Cmd{m.read(), m.tick()}
	for _, t := range m.tabs {
		cmds = append(cmds, t.Init(m.c))
	}
	return tea.Batch(cmds...)
}

// tickMsg starts each read of the node; gen is the chain of ticks it belongs to.
type tickMsg struct{ gen int }

func (m *Model) tick() tea.Cmd {
	interval, gen := m.o.Interval, m.tickGen
	if m.idle {
		interval = idleInterval
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return tickMsg{gen: gen} })
}

// readMsg is what one read of the node gave.
type readMsg struct {
	info          *nodev1.InfoResponse
	infoErr       error
	status        *nodev1.StatusResponse
	statusErr     error
	statusRead    bool
	disks         *nodev1.DisksResponse
	disksErr      error
	addresses     []string
	addressesErr  error
	addressesRead bool
	prompt        bool
	at            time.Time
}

// addressesInterval is the time between two reads of the addresses.
func (m *Model) addressesInterval(now time.Time) time.Duration {
	if m.idle || now.Sub(m.addressesChanged) >= addressesSettle {
		return idleInterval
	}
	return addressesEvery
}

// read reads Info and Disks, Status in normal mode, and the addresses in maintenance mode and on
// the installer, unless a read runs.
func (m *Model) read() tea.Cmd {
	if m.reading {
		return nil
	}
	m.reading = true
	c, now, prompt := m.c, m.o.Now(), m.o.PassphrasePrompt
	wantAddresses := now.Sub(m.addressesRead) >= m.addressesInterval(now)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		r := readMsg{at: now, prompt: prompt()}
		r.info, r.infoErr = c.Info(ctx)
		if r.infoErr != nil {
			return r
		}
		r.disks, r.disksErr = c.Disks(ctx)
		if (NodeMsg{Info: Source[*nodev1.InfoResponse]{Value: r.info}}).Normal() {
			r.status, r.statusErr = c.Status(ctx)
			r.statusRead = true
		} else if wantAddresses {
			var out []byte
			out, r.addressesErr = c.Diagnostic(ctx, "addresses", nil)
			if r.addressesErr == nil {
				r.addresses, r.addressesErr = globalAddresses(out)
			}
			r.addressesRead = true
		}
		return r
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m, m.key(msg)
	case tickMsg:
		if msg.gen != m.tickGen {
			return m, nil
		}
		m.idle = m.o.Now().Sub(m.lastKey) >= idleAfter
		return m, tea.Batch(m.read(), m.tick(), m.ticks())
	case readMsg:
		m.reading = false
		m.prompt = msg.prompt
		m.node.Info.Set(msg.info, msg.infoErr, msg.at)
		if msg.infoErr == nil {
			m.node.Disks.Set(msg.disks, msg.disksErr, msg.at)
		}
		if msg.statusRead {
			m.node.Status.Set(msg.status, msg.statusErr, msg.at)
		}
		if msg.addressesRead {
			if msg.addressesErr == nil && (m.addresses.At.IsZero() || !slices.Equal(msg.addresses, m.addresses.Value)) {
				m.addressesChanged = msg.at
			}
			m.addresses.Set(msg.addresses, msg.addressesErr, msg.at)
			m.addressesRead = msg.at
		}
		m.reshow()
		return m, m.broadcast(m.node)
	case actionDoneMsg:
		m.running = false
		m.note = msg.note
		return m, nil
	}
	return m, m.broadcast(msg)
}

// broadcast passes msg to every tab.
func (m *Model) broadcast(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, len(m.tabs))
	for i, t := range m.tabs {
		m.tabs[i], cmds[i] = t.Update(msg)
	}
	return tea.Batch(cmds...)
}

// ticks passes a TickMsg to every tab, Shown to the one on screen.
func (m *Model) ticks() tea.Cmd {
	now := m.o.Now()
	cmds := make([]tea.Cmd, len(m.tabs))
	for i, t := range m.tabs {
		m.tabs[i], cmds[i] = t.Update(TickMsg{Shown: i == m.current(), Idle: m.idle, Now: now})
	}
	return tea.Batch(cmds...)
}

// current is the index in tabs of the tab on screen.
func (m *Model) current() int { return m.shown[m.active] }

// applying lists the tabs that apply to the node's Info; until Info comes, the built-in ones.
func (m *Model) applying() []int {
	var shown []int
	for i, t := range m.tabs {
		if t.Applies(m.node.Info.Value) {
			shown = append(shown, i)
		}
	}
	return shown
}

// reshow updates the shown tabs and keeps the one on screen where it still applies.
func (m *Model) reshow() {
	current := m.current()
	m.shown = m.applying()
	m.active = max(0, slices.Index(m.shown, current))
}

// show puts the n-th shown tab on screen and tells it so.
func (m *Model) show(n int) tea.Cmd {
	if n < 0 || n >= len(m.shown) || n == m.active {
		return nil
	}
	m.active = n
	i := m.current()
	var cmd tea.Cmd
	m.tabs[i], cmd = m.tabs[i].Update(TickMsg{Shown: true, Idle: m.idle, Now: m.o.Now()})
	return cmd
}

// key handles a key: it ends the idle time, then the actions menu takes it while open, a
// capturing tab while it captures, then the console's own keys, and the tab on screen the rest.
func (m *Model) key(k tea.KeyMsg) tea.Cmd {
	m.lastKey = m.o.Now()
	var resume tea.Cmd
	if m.idle {
		m.idle = false
		m.tickGen++
		resume = tea.Batch(m.read(), m.tick(), m.ticks())
	}
	return tea.Batch(resume, m.handle(k))
}

func (m *Model) handle(k tea.KeyMsg) tea.Cmd {
	if m.menu != nil {
		return m.menuKey(k)
	}
	i := m.current()
	if c, ok := m.tabs[i].(Capturer); ok && c.Capturing() {
		var cmd tea.Cmd
		m.tabs[i], cmd = m.tabs[i].Update(k)
		return cmd
	}
	switch s := k.String(); {
	case s == "tab" || s == "right":
		return m.show((m.active + 1) % len(m.shown))
	case s == "shift+tab" || s == "left":
		return m.show((m.active + len(m.shown) - 1) % len(m.shown))
	case len(s) == 1 && s >= "1" && s <= "9":
		return m.show(int(s[0] - '1'))
	case s == "a" || s == "f10":
		m.openMenu()
		return nil
	}
	var cmd tea.Cmd
	m.tabs[i], cmd = m.tabs[i].Update(k)
	return cmd
}

func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	top := append(m.headerLines(), m.tabsLine())
	bottom := []string{m.keysLine()}
	switch {
	case m.note != "":
		bottom = append([]string{fit(m.note, m.width)}, bottom...)
	case m.idle:
		bottom = append([]string{fit(GreyStyle.Render("idle: the console reads the node every 30 s and follows no journal; a key resumes"), m.width)}, bottom...)
	}
	height := max(1, m.height-len(top)-len(bottom))
	var body string
	if m.menu != nil {
		body = m.menuView(m.width, height)
	} else {
		body = m.tabs[m.current()].View(m.width, height)
	}
	return strings.Join(slices.Concat(top, []string{block(body, m.width, height)}, bottom), "\n")
}

// tabsLine names the shown tabs by their numbers, the one on screen reversed.
func (m *Model) tabsLine() string {
	titles := make([]string, len(m.shown))
	for n, i := range m.shown {
		title := fmt.Sprintf(" %d %s ", n+1, m.tabs[i].Title())
		if n == m.active {
			title = activeTabStyle.Render(title)
		}
		titles[n] = title
	}
	return fit(strings.Join(titles, " "), m.width)
}

// keysLine names the console's keys.
func (m *Model) keysLine() string {
	actions := "a actions"
	if !m.o.Actions {
		actions += " (off in this image)"
	}
	return fit(fmt.Sprintf("Tab/Left/Right 1-%d tabs  Up/Down/PgUp/PgDn scroll  %s", len(m.shown), actions), m.width)
}
