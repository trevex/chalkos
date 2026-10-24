package main

import (
	"regexp"
	"slices"
	"strings"
)

// commands returns the simple commands of a line, including those in command substitutions,
// $(…) or backticks, within double quotes.
func commands(line string) [][]string {
	cmds := simpleCommands(words(line))
	for _, inner := range quotedSubstitutions(line) {
		cmds = append(cmds, commands(inner)...)
	}
	return cmds
}

// operators end a simple command; redirections end its arguments. A placeholder, a word such as
// <node>, is an argument, though a shell would read it as a redirection.
var (
	operators    = []string{"|", "||", "&&", ";", "&", "(", ")"}
	redirections = regexp.MustCompile(`^[0-9]*(>|>>|<|<<|>&|<&|&>)`)
	placeholder  = regexp.MustCompile(`^<[A-Za-z0-9._-]+>$`)
)

// substitution stands in a command for a command substitution, which is a command of its own.
const substitution = "$(…)"

// simpleCommands splits words at shell operators and command substitutions, and drops
// redirections and the comment that words ends with.
func simpleCommands(ws []string) [][]string {
	var cmds [][]string
	var cur []string
	// outer holds the commands that open substitutions interrupted, and opened how they opened.
	var outer [][]string
	var opened []string
	target := false
	for _, w := range ws {
		closes := len(opened) > 0 && (w == ")" && opened[len(opened)-1] == "$(" || w == "`" && opened[len(opened)-1] == "`")
		switch {
		case w == "#":
			return append(cmds, cur)
		case closes:
			cmds = append(cmds, cur)
			cur = append(outer[len(outer)-1], substitution)
			outer, opened = outer[:len(outer)-1], opened[:len(opened)-1]
		case w == "$(" || w == "`":
			outer, opened = append(outer, cur), append(opened, w)
			cur = nil
		case slices.Contains(operators, w):
			cmds, cur = append(cmds, cur), nil
		case redirections.MatchString(w) && !placeholder.MatchString(w):
			// The redirection's target, when it is a word of its own, is no argument either.
			target = redirections.ReplaceAllString(w, "") == ""
		case target:
			target = false
		default:
			cur = append(cur, w)
		}
	}
	cmds = append(cmds, cur)
	return append(cmds, outer...)
}

// words splits a command line as a shell does, roughly: quotes group, a backslash escapes, and
// operators are words of their own. Expansions are left as they are written; a command
// substitution within double quotes stays part of its word.
func words(line string) []string {
	var ws []string
	var cur strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			ws = append(ws, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			flush()
		case c == '#' && !inWord:
			flush()
			return append(ws, "#")
		case c == '\'':
			inWord = true
			end := strings.IndexByte(line[i+1:], c)
			if end < 0 {
				cur.WriteString(line[i+1:])
				i = len(line)
				continue
			}
			cur.WriteString(line[i+1 : i+1+end])
			i += end + 1
		case c == '"':
			inWord = true
			end := closingQuote(line, i)
			for j := i + 1; j < end; j++ {
				if line[j] == '\\' && j+1 < end {
					j++
				}
				cur.WriteByte(line[j])
			}
			i = end
		case c == '\\' && i+1 < len(line):
			inWord = true
			i++
			cur.WriteByte(line[i])
		case c == '$' && i+1 < len(line) && line[i+1] == '(':
			flush()
			ws = append(ws, "$(")
			i++
		case c == '&' && (inWord && strings.HasSuffix(cur.String(), ">") || inWord && strings.HasSuffix(cur.String(), "<") || i+1 < len(line) && line[i+1] == '>'):
			// Part of a redirection: 2>&1, <&3, &>file.
			inWord = true
			cur.WriteByte(c)
		case c == '|' || c == '&' || c == ';' || c == '(' || c == ')' || c == '`':
			flush()
			op := string(c)
			if i+1 < len(line) && (c == '|' || c == '&') && line[i+1] == c {
				op += string(c)
				i++
			}
			ws = append(ws, op)
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	flush()
	return ws
}

// closingQuote returns the index of the double quote that closes the one at open, skipping
// escaped characters and command substitutions, or len(s) when none does.
func closingQuote(s string, open int) int {
	for j := open + 1; j < len(s); j++ {
		switch {
		case s[j] == '\\':
			j++
		case s[j] == '$' && j+1 < len(s) && s[j+1] == '(':
			j = closingParen(s, j+1)
		case s[j] == '"':
			return j
		}
	}
	return len(s)
}

// closingParen returns the index of the parenthesis that closes the one at open, skipping quoted
// text, or len(s) when none does.
func closingParen(s string, open int) int {
	depth := 0
	for j := open; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '\'':
			k := strings.IndexByte(s[j+1:], '\'')
			if k < 0 {
				return len(s)
			}
			j += k + 1
		case '"':
			j = closingQuote(s, j)
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return j
			}
		}
	}
	return len(s)
}

// quotedSubstitutions returns the commands of the command substitutions, $(…) or backticks,
// within double quotes of a line, outside its comment; words keeps them inside their words.
func quotedSubstitutions(line string) []string {
	var subs []string
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '#':
			if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
				return subs
			}
		case '\'':
			k := strings.IndexByte(line[i+1:], '\'')
			if k < 0 {
				return subs
			}
			i += k + 1
		case '"':
			end := closingQuote(line, i)
			for j := i + 1; j < end; j++ {
				switch {
				case line[j] == '\\':
					j++
				case line[j] == '$' && j+1 < end && line[j+1] == '(':
					closing := closingParen(line, j+1)
					subs = append(subs, line[j+2:min(closing, len(line))])
					j = closing
				case line[j] == '`':
					closing := end
					if k := strings.IndexByte(line[j+1:end], '`'); k >= 0 {
						closing = j + 1 + k
					}
					subs = append(subs, line[j+1:closing])
					j = closing
				}
			}
			i = end
		}
	}
	return subs
}
