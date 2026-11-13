package console

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The console's styles use the 16 colours of the Linux console alone. The layers' tabs use the
// exported ones.
var (
	// TitleStyle marks the hostname and headings.
	TitleStyle = lipgloss.NewStyle().Bold(true)
	// ErrorStyle marks errors.
	ErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	// OKStyle marks good health.
	OKStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	// WarnStyle marks warnings.
	WarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	// GreyStyle marks what does not apply.
	GreyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	activeTabStyle = lipgloss.NewStyle().Reverse(true)
	selectedStyle  = lipgloss.NewStyle().Reverse(true)
)

// fit cuts a line to width columns.
func fit(s string, width int) string { return ansi.Truncate(s, width, "") }

// block is s as exactly height lines, each cut to width: the lines past height are dropped and
// empty ones fill the rest.
func block(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = fit(lines[i], width)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// wrapWords is label and the words of text on rows of width, broken at spaces; the rows after the
// first are indented below the text.
func wrapWords(label, text string, width int) []string {
	indent := strings.Repeat(" ", len(label)+1)
	row := label
	var rows []string
	for i, w := range strings.Fields(text) {
		if i > 0 && ansi.StringWidth(row)+1+ansi.StringWidth(w) > width {
			rows = append(rows, row)
			row = indent + w
			continue
		}
		row += " " + w
	}
	return append(rows, row)
}

// count is n of a thing, such as 1 unit or 2 units.
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// clean makes text safe to print on the screen: a tab expands to the next multiple of 8 columns
// of its line, and every other control character, the escape that starts a terminal's sequences
// among them, shows as \xNN. Newlines stay.
func clean(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r != '\n' && isControl(r) }) {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
			col = 0
		case r == '\t':
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case isControl(r):
			fmt.Fprintf(&b, "\\x%02x", r)
			col += 4
		default:
			b.WriteRune(r)
			col += ansi.StringWidth(string(r))
		}
	}
	return b.String()
}

// isControl reports whether r is a control character of C0 or C1, or DEL.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }

// lineBreaks are what oneLine replaces with a space.
var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// oneLine is s for a single line of the screen: its line breaks become spaces, and it is clean.
func oneLine(s string) string { return clean(lineBreaks.Replace(s)) }

// wrapChars is label and text on rows of width, text broken anywhere; the rows after the first
// are indented below the text.
func wrapChars(label, text string, width int) []string {
	indent := strings.Repeat(" ", len(label)+1)
	room := max(1, width-len(indent))
	row := label + " "
	var rows []string
	for len(text) > room {
		rows = append(rows, row+text[:room])
		row, text = indent, text[room:]
	}
	return append(rows, row+text)
}
