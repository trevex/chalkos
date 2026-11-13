package console

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/trevex/chalkos/pkg/askpassword"
)

// enterGuard is how long a prompt ignores Enter after it comes on screen, so an Enter meant for
// what was on screen before sends no empty password; answeredFor is how long an answered request
// stays off the screen while its file remains, after which the asker did not take the answer and
// the request comes back.
const (
	enterGuard  = 300 * time.Millisecond
	answeredFor = 5 * time.Second
)

// prompt is the password request on screen and what was typed for it, size bytes of UTF-8 at
// most askpassword.MaxPassword; opened is when it came on screen. The console clears input
// whenever it lets go of it, so a password stays in memory only until it is sent.
type prompt struct {
	req    askpassword.Request
	input  []rune
	size   int
	opened time.Time
}

// add types r, unless the password would grow past what the asker takes. A full buffer moves to
// one twice as large, and the old one is cleared.
func (p *prompt) add(r rune) {
	n := utf8.RuneLen(r)
	if n < 0 || p.size+n > askpassword.MaxPassword {
		return
	}
	if len(p.input) == cap(p.input) {
		grown := make([]rune, len(p.input), max(64, 2*cap(p.input)))
		copy(grown, p.input)
		clear(p.input)
		p.input = grown
	}
	p.input = append(p.input, r)
	p.size += n
}

// back erases the last character typed.
func (p *prompt) back() {
	if n := len(p.input); n > 0 {
		p.size -= utf8.RuneLen(p.input[n-1])
		p.input[n-1] = 0
		p.input = p.input[:n-1]
	}
}

// erase clears what was typed.
func (p *prompt) erase() {
	clear(p.input)
	p.input = p.input[:0]
	p.size = 0
}

// asksMsg is the requests as read by the read numbered seq; asksChangedMsg says their directory
// changed; asksWatchEndedMsg says the watch of the directory ended, as the directory went;
// answeredMsg is the result of sending an answer or a cancel to the request at path.
type (
	asksMsg struct {
		seq  int
		reqs []askpassword.Request
		err  error
	}
	asksChangedMsg    struct{}
	asksWatchEndedMsg struct{}
	answeredMsg       struct {
		path string
		err  error
	}
)

// readAsks reads the pending requests. The reads are numbered, so a read that ends after a later
// one does not bring back a request that went.
func (m *Model) readAsks() tea.Cmd {
	m.asksSeq++
	agent, seq := m.o.Asks, m.asksSeq
	return func() tea.Msg {
		reqs, err := agent.Pending()
		return asksMsg{seq: seq, reqs: reqs, err: err}
	}
}

// watchAsks watches the requests' directory while no watch runs and the console runs, and then
// reads the requests; nothing while the directory does not exist, which the ticks try again.
func (m *Model) watchAsks() tea.Cmd {
	if m.asksChanged != nil || m.ctx.Err() != nil {
		return nil
	}
	changes, err := m.o.Asks.Watch(m.ctx)
	if err != nil {
		return nil
	}
	m.asksChanged = changes
	return tea.Batch(m.readAsks(), waitAsks(changes))
}

// waitAsks waits for the next change of the requests' directory, or the end of its watch;
// nothing without a watch.
func waitAsks(changes <-chan struct{}) tea.Cmd {
	if changes == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-changes; !ok {
			return asksWatchEndedMsg{}
		}
		return asksChangedMsg{}
	}
}

// setAsks takes the requests as read: the prompt closes when its request went, and the oldest
// open request comes on screen when none is.
func (m *Model) setAsks(reqs []askpassword.Request) {
	m.asks = reqs
	pending := map[string]bool{}
	for _, r := range reqs {
		pending[r.Path] = true
	}
	now := m.o.Now()
	for path, at := range m.answered {
		if !pending[path] || now.Sub(at) >= answeredFor {
			delete(m.answered, path)
		}
	}
	if m.prompt != nil && !pending[m.prompt.req.Path] {
		m.prompt.erase()
		m.prompt = nil
	}
	if m.prompt == nil {
		if open := m.open(); len(open) > 0 {
			m.prompt = &prompt{req: open[0], opened: now}
		} else {
			m.askNote = ""
		}
	}
}

// open lists the requests not answered lately, oldest first.
func (m *Model) open() []askpassword.Request {
	var open []askpassword.Request
	for _, r := range m.asks {
		if _, ok := m.answered[r.Path]; !ok {
			open = append(open, r)
		}
	}
	return open
}

// promptKey handles a key while a request is on screen: Enter sends what was typed, Esc
// cancels, Backspace erases a character, Ctrl+U the line, and every other printable key types.
func (m *Model) promptKey(k tea.KeyMsg) tea.Cmd {
	p := m.prompt
	switch k.Type {
	case tea.KeyEnter:
		if m.o.Now().Sub(p.opened) < enterGuard {
			return nil
		}
		password := make([]byte, 0, p.size)
		for _, r := range p.input {
			password = utf8.AppendRune(password, r)
		}
		return m.reply(func(r askpassword.Request) error {
			defer clear(password)
			return r.Answer(password)
		})
	case tea.KeyEsc:
		return m.reply(askpassword.Request.Cancel)
	case tea.KeyBackspace, tea.KeyCtrlH:
		p.back()
	case tea.KeyCtrlU:
		p.erase()
	case tea.KeySpace:
		p.add(' ')
	case tea.KeyRunes:
		for _, r := range k.Runes {
			if !isControl(r) {
				p.add(r)
			}
		}
	}
	return nil
}

// reply sends the request on screen its answer or its cancel, and puts the next request on
// screen; the answered one stays off it until its file goes, or for answeredFor.
func (m *Model) reply(send func(askpassword.Request) error) tea.Cmd {
	req := m.prompt.req
	m.prompt.erase()
	m.prompt = nil
	m.answered[req.Path] = m.o.Now()
	m.askNote = ""
	m.setAsks(m.asks)
	return func() tea.Msg { return answeredMsg{path: req.Path, err: send(req)} }
}

// promptScreen is the screen while a request is on it: the header, the request in place of the
// tabs, and the prompt's keys.
func (m *Model) promptScreen() string {
	top := m.headerLines()
	bottom := []string{fit("Enter: send   Esc: cancel   Backspace: erase   Ctrl+U: erase all", m.width)}
	if m.askNote != "" {
		bottom = append([]string{fit(ErrorStyle.Render(m.askNote), m.width)}, bottom...)
	}
	height := max(1, m.height-len(top)-len(bottom))
	return strings.Join(slices.Concat(top, []string{m.promptView(m.width, height)}, bottom), "\n")
}

// promptView is the request on screen: its question, where a volume's key comes from, what was
// typed, as stars unless the request asks for echo or for silence, and how many more requests
// wait. A question too long for the screen is cut, so the input stays on it.
func (m *Model) promptView(width, height int) string {
	p := m.prompt
	var hint []string
	if strings.HasPrefix(p.req.ID, "cryptsetup:") {
		name := OneLine(cmp.Or(m.node.Info.Value.GetNodeName(), "<node>"))
		hint = []string{GreyStyle.Render("chalkctl recovery-key " + name + " prints the node's recovery key")}
	}
	waiting := len(m.open()) - 1
	// Besides the question: the title and a blank row, the hint, a blank row and the input, and
	// a blank row and the count of the requests that wait.
	fixed := 4 + len(hint)
	if waiting > 0 {
		fixed += 2
	}
	question := strings.Split(Wrap(OneLine(p.req.Message), width), "\n")
	if room := max(1, height-fixed); len(question) > room {
		question = question[:room]
		question[room-1] = ansi.Truncate(question[room-1], max(0, width-1), "") + "…"
	}
	rows := slices.Concat([]string{TitleStyle.Render("systemd asks for a password"), ""}, question, hint)
	var field string
	switch {
	case p.req.Echo:
		field = OneLine(string(p.input))
	case !p.req.Silent:
		field = strings.Repeat("*", len(p.input))
	}
	// A long answer shows its end, where typing goes on.
	if room := max(1, width-3); ansi.StringWidth(field) > room {
		field = ansi.TruncateLeft(field, ansi.StringWidth(field)-room, "")
	}
	rows = append(rows, "", "> "+field+"_")
	if waiting > 0 {
		verb := " waits"
		if waiting > 1 {
			verb = " wait"
		}
		rows = append(rows, "", GreyStyle.Render(count(waiting, "more request")+verb))
	}
	return block(strings.Join(rows, "\n"), width, height)
}
