package console

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Pane shows text a screen at a time, scrolled with the arrows, PgUp, PgDn, Home and End.
type Pane struct {
	offset int
	// lines and height are those of the last view, which the keys scroll by.
	lines, height int
}

// Key scrolls by k and reports whether it was a scrolling key.
func (p *Pane) Key(k tea.KeyMsg) bool {
	switch k.String() {
	case "up":
		p.offset--
	case "down":
		p.offset++
	case "pgup":
		p.offset -= max(1, p.height-1)
	case "pgdown":
		p.offset += max(1, p.height-1)
	case "home":
		p.offset = 0
	case "end":
		p.offset = p.lines
	default:
		return false
	}
	return true
}

// View is the screen of text at the offset, width by height.
func (p *Pane) View(text string, width, height int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	p.lines, p.height = len(lines), height
	p.offset = min(max(p.offset, 0), max(0, len(lines)-height))
	return block(strings.Join(lines[p.offset:], "\n"), width, height)
}
