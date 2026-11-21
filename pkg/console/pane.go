package console

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Pane shows text a screen at a time, scrolled with the arrows, PgUp, PgDn, Home and End.
type Pane struct {
	offset int
	// lines and height are those of the last view, which the keys scroll by.
	lines, height int
}

// Key scrolls by k and reports whether it was a scrolling key.
func (p *Pane) Key(k tea.KeyPressMsg) bool {
	offset, ok := scroll(k.String(), p.offset, p.height, p.lines)
	p.offset = offset
	return ok
}

// scroll is the offset of the first line on a screen of height lines after key, with last the
// offset End goes to, and whether key scrolls.
func scroll(key string, offset, height, last int) (int, bool) {
	switch key {
	case "up":
		return offset - 1, true
	case "down":
		return offset + 1, true
	case "pgup":
		return offset - max(1, height-1), true
	case "pgdown":
		return offset + max(1, height-1), true
	case "home":
		return 0, true
	case "end":
		return last, true
	}
	return offset, false
}

// View is the screen of text at the offset, width by height.
func (p *Pane) View(text string, width, height int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	p.lines, p.height = len(lines), height
	p.offset = min(max(p.offset, 0), max(0, len(lines)-height))
	return block(strings.Join(lines[p.offset:], "\n"), width, height)
}
