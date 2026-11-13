package console

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// The Logs tab keeps the last maxLogLines lines of the journal, asks for the last replayLines when
// a stream starts, and takes up to logBatch lines of a stream at a time.
const (
	maxLogLines = 5000
	replayLines = maxLogLines
	logBatch    = 256
)

// While the console reads the journal itself, it asks chalkd for it again after a backoff that
// starts at firstRetry and doubles up to lastRetry.
const (
	firstRetry = 10 * time.Second
	lastRetry  = 2 * time.Minute
)

// errorLine finds the journal lines the Logs tab marks as errors.
var errorLine = regexp.MustCompile(`(?i)\b(error|failed|fail|fatal|panic)\b`)

// logs is the Logs tab: the journal of the boot, following, through chalkd or, while chalkd does
// not answer, read by the console itself; filtered by a unit, searched, and wrapped. It reads the
// journal once it first comes on screen, and stops while the console is idle.
type logs struct {
	o Options
	c Client
	// unit filters the journal; empty for every unit.
	unit  string
	lines []string
	// gen numbers the streams. shown is the stream whose lines are on screen, zero while none
	// runs, and cancel ends it; pending is a stream that replaces it once its first lines come,
	// until which the lines on screen stay.
	gen           int
	shown         int
	cancel        context.CancelFunc
	pending       int
	pendingLocal  bool
	pendingCancel context.CancelFunc
	// started is set once the tab came on screen, and stopped while the console is idle.
	started, stopped bool
	// local is set while the lines on screen are the console's own reading, because chalkd's
	// stream failed with why. retry says whether to ask chalkd again, at retryAt, after backoff.
	local   bool
	why     error
	retry   bool
	retryAt time.Time
	backoff time.Duration
	// done is set once the stream on screen ended, with ended as why.
	done  bool
	ended error
	// search is the text typed after / while searching.
	search    string
	searching bool
	query     string
	// matches index the lines that hold query, and match the one shown.
	matches []int
	match   int
	// jump is a line to bring on screen at the next view; -1 for none.
	jump   int
	picker *unitPicker
	// offset is the first line on screen; follow keeps the last lines on screen. wrap breaks
	// long lines at the width instead of cutting them. width and height are those of the last
	// view, which the keys scroll by.
	offset        int
	follow        bool
	wrap          bool
	width, height int
}

// unitPicker is the open list of units to filter the journal by.
type unitPicker struct {
	units    []string
	err      error
	loaded   bool
	selected int
}

// allUnits is the picker's first entry, which shows the journal of every unit.
const allUnits = "all units"

func newLogs(o Options) *logs { return &logs{o: o, jump: -1, follow: true} }

func (l *logs) Title() string                     { return "Logs" }
func (l *logs) Applies(*nodev1.InfoResponse) bool { return true }
func (l *logs) Init(c Client) tea.Cmd             { l.c = c; return nil }
func (l *logs) Capturing() bool                   { return l.searching || l.picker != nil }

// logStream is one stream of journal lines; err is set before lines closes.
type logStream struct {
	lines chan string
	err   error
}

// logLinesMsg carries lines of the stream gen, which goes on.
type logLinesMsg struct {
	gen    int
	stream *logStream
	lines  []string
}

// logEndMsg says that the stream gen ended, and why.
type logEndMsg struct {
	gen int
	err error
}

// unitsMsg carries the active units for the picker.
type unitsMsg struct {
	units []string
	err   error
}

// open starts a stream of chalkd's Logs or, when local, of the console's own journal.
func (l *logs) open(local bool) (int, context.CancelFunc, tea.Cmd) {
	l.gen++
	ctx, cancel := context.WithCancel(context.Background())
	source := l.c.Logs
	if local {
		source = l.o.Journal
	}
	s := &logStream{lines: make(chan string, 1024)}
	unit := l.unit
	go func() {
		s.err = source(ctx, unit, func(line string) {
			select {
			case s.lines <- line:
			case <-ctx.Done():
			}
		})
		close(s.lines)
	}()
	return l.gen, cancel, next(l.gen, s)
}

// start replaces the streams and the lines on screen with a new stream, of chalkd or, when local,
// of the console's own journal.
func (l *logs) start(local bool) tea.Cmd {
	l.stop()
	var cmd tea.Cmd
	l.shown, l.cancel, cmd = l.open(local)
	l.local = local
	l.lines, l.matches, l.match, l.offset, l.done, l.ended = nil, nil, 0, 0, false, nil
	return cmd
}

// switchTo starts a pending stream, of chalkd or, when local, of the console's own journal.
func (l *logs) switchTo(local bool) tea.Cmd {
	if l.pendingCancel != nil {
		l.pendingCancel()
	}
	var cmd tea.Cmd
	l.pending, l.pendingCancel, cmd = l.open(local)
	l.pendingLocal = local
	return cmd
}

// stop ends every stream; the lines stay on screen.
func (l *logs) stop() {
	for _, cancel := range []context.CancelFunc{l.cancel, l.pendingCancel} {
		if cancel != nil {
			cancel()
		}
	}
	l.shown, l.pending, l.cancel, l.pendingCancel = 0, 0, nil, nil
}

// next waits for the stream's next lines and takes those that came with them.
func next(gen int, s *logStream) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-s.lines
		if !ok {
			return logEndMsg{gen: gen, err: s.err}
		}
		lines := []string{line}
		for len(lines) < logBatch {
			select {
			case line, ok := <-s.lines:
				if !ok {
					return logLinesMsg{gen: gen, stream: s, lines: lines}
				}
				lines = append(lines, line)
			default:
				return logLinesMsg{gen: gen, stream: s, lines: lines}
			}
		}
		return logLinesMsg{gen: gen, stream: s, lines: lines}
	}
}

// transient reports whether a stream of chalkd ended as chalkd stops, restarts or hangs, after
// which it answers again.
func transient(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeCanceled, connect.CodeDeadlineExceeded, connect.CodeAborted:
		return true
	}
	return err == nil || errors.Is(err, context.Canceled)
}

// fallBack reads the journal itself, as chalkd's stream failed with err, and asks chalkd again
// after the next backoff when err says chalkd may answer again.
func (l *logs) fallBack(err error) tea.Cmd {
	if l.pending != 0 && l.pendingLocal {
		return nil
	}
	l.why = err
	l.scheduleRetry(err)
	return l.switchTo(true)
}

// scheduleRetry sets when to ask chalkd again, after a stream that ended with err.
func (l *logs) scheduleRetry(err error) {
	l.retry = transient(err)
	if !l.retry {
		return
	}
	l.backoff = min(lastRetry, max(firstRetry, 2*l.backoff))
	l.retryAt = l.o.Now().Add(l.backoff)
}

func (l *logs) Update(msg tea.Msg) (Tab, tea.Cmd) {
	switch msg := msg.(type) {
	case logLinesMsg:
		return l, l.add(msg)
	case logEndMsg:
		return l, l.end(msg)
	case NodeMsg:
		if !l.started || l.stopped || l.pending != 0 {
			return l, nil
		}
		code := connect.CodeOf(msg.Info.Err)
		switch {
		case !l.local && l.shown != 0 && (code == connect.CodeUnavailable || code == connect.CodeDeadlineExceeded):
			// chalkd does not answer, or hangs, so its stream gives nothing either.
			return l, l.fallBack(msg.Info.Err)
		case (l.local || l.shown == 0) && l.retry && msg.Info.Err == nil && !l.o.Now().Before(l.retryAt):
			return l, l.switchTo(false)
		}
	case TickMsg:
		switch {
		case msg.Idle && l.started && !l.stopped:
			l.stop()
			l.stopped = true
		case msg.Idle || !msg.Shown:
		case !l.started:
			l.started = true
			return l, l.start(false)
		case l.stopped:
			l.stopped = false
			return l, l.switchTo(l.local)
		}
	case unitsMsg:
		if l.picker != nil {
			units := make([]string, len(msg.units))
			for i, u := range msg.units {
				units[i] = OneLine(u)
			}
			l.picker.units = append([]string{allUnits}, units...)
			l.picker.err, l.picker.loaded = msg.err, true
		}
	case tea.KeyMsg:
		return l, l.key(msg)
	}
	return l, nil
}

// add takes the lines of a stream: those of the stream on screen, or the first of the pending
// one, which then replaces it.
func (l *logs) add(msg logLinesMsg) tea.Cmd {
	switch msg.gen {
	case l.pending:
		if l.cancel != nil {
			l.cancel()
		}
		l.shown, l.cancel, l.local = l.pending, l.pendingCancel, l.pendingLocal
		l.pending, l.pendingCancel = 0, nil
		l.lines, l.offset, l.done, l.ended = nil, 0, false, nil
		if !l.local {
			l.why, l.retry, l.backoff = nil, false, 0
		}
	case l.shown:
	default:
		return nil
	}
	for _, line := range msg.lines {
		l.lines = append(l.lines, OneLine(line))
	}
	if drop := len(l.lines) - maxLogLines; drop > 0 {
		l.lines = append([]string(nil), l.lines[drop:]...)
	}
	l.findMatches()
	return next(msg.gen, msg.stream)
}

// end handles the end of a stream: chalkd's on screen falls back to the console's own reading, a
// pending one of chalkd is tried again later, and the console's own reading that ends says why.
func (l *logs) end(msg logEndMsg) tea.Cmd {
	switch {
	case msg.gen == l.pending && l.pendingLocal:
		// The console's own reading fails too.
		l.pending, l.pendingCancel = 0, nil
		l.done, l.ended = true, msg.err
	case msg.gen == l.pending:
		l.pending, l.pendingCancel = 0, nil
		if l.shown == 0 && !l.local {
			return l.fallBack(msg.err)
		}
		l.scheduleRetry(msg.err)
	case msg.gen == l.shown && !l.local:
		l.shown, l.cancel = 0, nil
		return l.fallBack(msg.err)
	case msg.gen == l.shown:
		l.shown, l.cancel = 0, nil
		l.done, l.ended = true, msg.err
	}
	return nil
}

// key handles a key: the picker and the search take every key while open; u opens the picker,
// / the search, n and N go to the next and previous match, w turns wrapping on and off, and the
// rest scroll.
func (l *logs) key(k tea.KeyMsg) tea.Cmd {
	switch {
	case l.picker != nil:
		return l.pickerKey(k)
	case l.searching:
		l.searchKey(k)
		return nil
	}
	switch k.String() {
	case "u":
		l.picker = &unitPicker{}
		units := l.o.Units
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
			defer cancel()
			u, err := units(ctx)
			return unitsMsg{units: u, err: err}
		}
	case "/":
		l.searching = true
		l.search = ""
	case "n":
		l.step(1)
	case "N":
		l.step(-1)
	case "w":
		l.wrap = !l.wrap
	default:
		l.scroll(k)
	}
	return nil
}

// scroll moves the lines on screen by k, and follows the journal again once the last line is on
// screen.
func (l *logs) scroll(k tea.KeyMsg) {
	last := l.lastStart(l.width, l.height)
	if l.follow {
		l.offset = last
	}
	switch k.String() {
	case "up":
		l.offset--
	case "down":
		l.offset++
	case "pgup":
		l.offset -= max(1, l.height-1)
	case "pgdown":
		l.offset += max(1, l.height-1)
	case "home":
		l.offset = 0
	case "end":
		l.offset = last
	default:
		return
	}
	l.offset = min(max(l.offset, 0), last)
	l.follow = l.offset >= last
}

func (l *logs) pickerKey(k tea.KeyMsg) tea.Cmd {
	p := l.picker
	switch k.String() {
	case "up":
		p.selected = max(0, p.selected-1)
	case "down":
		p.selected = min(max(0, len(p.units)-1), p.selected+1)
	case "esc":
		l.picker = nil
	case "enter":
		if !p.loaded {
			return nil
		}
		l.picker = nil
		l.unit = ""
		if p.selected > 0 {
			l.unit = p.units[p.selected]
		}
		l.follow = true
		if !l.started || l.stopped {
			return nil
		}
		return l.start(l.local)
	}
	return nil
}

func (l *logs) searchKey(k tea.KeyMsg) {
	switch k.String() {
	case "enter":
		l.searching = false
		l.query = l.search
		l.findMatches()
		l.match = 0
		l.step(0)
		return
	case "esc":
		l.searching = false
		l.query, l.matches = "", nil
		return
	}
	switch {
	case k.Type == tea.KeyBackspace:
		if r := []rune(l.search); len(r) > 0 {
			l.search = string(r[:len(r)-1])
		}
	case k.Type == tea.KeyRunes, k.Type == tea.KeySpace:
		l.search += OneLine(string(k.Runes))
	}
}

// findMatches finds the lines that hold the query, ignoring case.
func (l *logs) findMatches() {
	l.matches = nil
	if l.query == "" {
		return
	}
	q := strings.ToLower(l.query)
	for i, line := range l.lines {
		if strings.Contains(strings.ToLower(line), q) {
			l.matches = append(l.matches, i)
		}
	}
	l.match = min(l.match, max(0, len(l.matches)-1))
}

// step moves the match shown by delta and brings it on screen.
func (l *logs) step(delta int) {
	if len(l.matches) == 0 {
		return
	}
	l.match = (l.match + delta + len(l.matches)) % len(l.matches)
	l.jump = l.matches[l.match]
}

// rows are the screen rows of line i: the line in its style, cut to width, or broken at width
// while wrapping.
func (l *logs) rows(i, width int) []string {
	line := l.lines[i]
	switch {
	case len(l.matches) > 0 && i == l.matches[l.match]:
		line = selectedStyle.Render(line)
	case errorLine.MatchString(line):
		line = ErrorStyle.Render(line)
	}
	if !l.wrap {
		return []string{fit(line, width)}
	}
	return strings.Split(ansi.Hardwrap(line, width, true), "\n")
}

// lastStart is the first line on screen when the last line is at the bottom.
func (l *logs) lastStart(width, height int) int {
	if !l.wrap {
		return max(0, len(l.lines)-height)
	}
	rows := 0
	for i := len(l.lines) - 1; i >= 0; i-- {
		if rows += len(l.rows(i, width)); rows > height {
			return i + 1
		}
	}
	return 0
}

// View renders the status row and only the lines on screen, which keeps a long journal cheap.
func (l *logs) View(width, height int) string {
	if l.picker != nil {
		return l.pickerView(width, height)
	}
	top := l.statusRow(width)
	if l.searching {
		top = "/" + l.search
	}
	h := max(1, height-1)
	l.width, l.height = width, h
	last := l.lastStart(width, h)
	switch {
	case l.jump >= 0:
		l.offset, l.follow = min(max(0, l.jump-h/2), last), false
		l.jump = -1
	case l.follow:
		l.offset = last
	}
	l.offset = min(max(l.offset, 0), last)
	var rows []string
	for i := l.offset; i < len(l.lines) && len(rows) < h; i++ {
		rows = append(rows, l.rows(i, width)...)
	}
	return top + "\n" + block(strings.Join(rows, "\n"), width, h)
}

// statusRow says what the tab shows: the unit, whether it follows and wraps, the search, and
// whether the console reads the journal itself; then the tab's keys, where they fit.
func (l *logs) statusRow(width int) string {
	row := "this boot, " + allUnits
	if l.unit != "" {
		row = "unit " + l.unit
	}
	if l.follow {
		row += ", following"
	}
	if l.wrap {
		row += ", wrap on"
	}
	if l.stopped {
		row += ", paused while idle"
	}
	if l.query != "" {
		found := "no match"
		switch n := len(l.matches); {
		case n == 1:
			found = "1 match"
		case n > 1:
			found = fmt.Sprintf("%d matches", n)
		}
		row += fmt.Sprintf(", %q: %s", l.query, found)
	}
	if l.local {
		why := "chalkd's stream ended"
		switch {
		case connect.CodeOf(l.why) == connect.CodeUnavailable:
			why = "chalkd does not answer"
		case l.why != nil:
			why = OneLine(l.why.Error())
		}
		row += WarnStyle.Render("; read by the console: " + why)
	} else if l.why != nil && l.shown == 0 {
		row += WarnStyle.Render(OneLine(fmt.Sprintf("; chalkd's stream ended: %v", l.why)))
	}
	if l.done {
		row += ErrorStyle.Render(OneLine(fmt.Sprintf("; the journal ended: %v", l.ended)))
	}
	if keys := "   u unit  / search  n N match  w wrap"; ansi.StringWidth(row)+len(keys) <= width {
		row += GreyStyle.Render(keys)
	}
	return row
}

func (l *logs) pickerView(width, height int) string {
	p := l.picker
	rows := []string{TitleStyle.Render("Show the journal of") + "   Enter: pick  Esc: back"}
	switch {
	case !p.loaded:
		rows = append(rows, "reading the units...")
	case p.err != nil:
		rows = append(rows, ErrorStyle.Render(OneLine("units: "+p.err.Error())))
	}
	h := max(1, height-len(rows))
	first := max(0, min(p.selected-h/2, len(p.units)-h))
	for i := first; i < len(p.units) && i < first+h; i++ {
		row := "  " + p.units[i]
		if i == p.selected {
			row = selectedStyle.Render("> " + p.units[i])
		}
		rows = append(rows, row)
	}
	return block(strings.Join(rows, "\n"), width, height)
}
