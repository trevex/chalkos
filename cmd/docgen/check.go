package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// shellLanguages are the code blocks whose commands are checked; a block without a language
// counts as shell, as cobra's pages write them.
var shellLanguages = []string{"", "sh", "bash", "shell", "zsh", "console", "shell-session"}

// check finds the chalkctl and chalklab commands in the shell code blocks of the Markdown files
// under dir, but not under dir/superpowers, and returns an error naming each one that runs a
// command or passes a flag the programs do not have. newRoots returns fresh command trees, as
// parsing changes a tree's flags.
func check(newRoots func() []*cobra.Command, dir string) error {
	var problems []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path == filepath.Join(dir, "superpowers") {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		blocks, err := shellBlocks(path)
		if err != nil {
			return err
		}
		for _, b := range blocks {
			for _, inv := range invocations(b) {
				if err := checkInvocation(newRoots(), inv.args); err != nil {
					problems = append(problems, fmt.Sprintf("%s:%d: %s: %v", path, inv.line, strings.Join(inv.args, " "), err))
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return errors.New("commands in the documentation that chalkctl and chalklab do not have:\n" + strings.Join(problems, "\n"))
	}
	return nil
}

// block is a shell code block: its lines and the line number of the first one.
type block struct {
	first   int
	console bool
	lines   []string
}

// fence opens or closes a code block, also indented in a list, an admonition or a tab.
var fence = regexp.MustCompile("^\\s*(```+|~~~+)\\s*([^\\s`]*)")

// shellBlocks returns the shell code blocks of a Markdown file.
func shellBlocks(path string) ([]block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var blocks []block
	var open string // the fence of the open block
	var cur *block
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		m := fence.FindStringSubmatch(line)
		switch {
		case open == "" && m != nil:
			open = m[1]
			lang := strings.ToLower(strings.Trim(m[2], "{}."))
			if slices.Contains(shellLanguages, lang) {
				blocks = append(blocks, block{first: n + 1, console: lang == "console" || lang == "shell-session"})
				cur = &blocks[len(blocks)-1]
			}
		case open != "" && m != nil && m[2] == "" && strings.HasPrefix(m[1], open):
			open, cur = "", nil
		case cur != nil:
			cur.lines = append(cur.lines, line)
		}
	}
	return blocks, scanner.Err()
}

// invocation is a command line that runs chalkctl or chalklab: the program and its arguments.
type invocation struct {
	line int
	args []string
}

// programNames are the programs whose commands are checked.
var programNames = []string{"chalkctl", "chalklab"}

// invocations returns the chalkctl and chalklab command lines of a block. Lines continued with a
// backslash are joined; in a console block, only lines with a $ prompt are commands, and the
// lines they continue on may start with a > prompt.
func invocations(b block) []invocation {
	var invs []invocation
	for i := 0; i < len(b.lines); i++ {
		start := b.first + i
		line := strings.TrimSpace(b.lines[i])
		for strings.HasSuffix(line, "\\") && i+1 < len(b.lines) {
			i++
			next := strings.TrimSpace(b.lines[i])
			if b.console {
				next = strings.TrimPrefix(next, "> ")
			}
			line = strings.TrimSuffix(line, "\\") + " " + next
		}
		if b.console {
			rest, ok := strings.CutPrefix(line, "$ ")
			if !ok {
				continue
			}
			line = rest
		}
		for _, cmd := range commands(line) {
			if args := program(cmd); args != nil {
				invs = append(invs, invocation{line: start, args: args})
			}
		}
	}
	return invs
}

// commands returns the simple commands of a line, including those in command substitutions
// within double quotes.
func commands(line string) [][]string {
	cmds := simpleCommands(words(line))
	for _, inner := range quotedSubstitutions(line) {
		cmds = append(cmds, commands(inner)...)
	}
	return cmds
}

// wrapperOptions are the options with a value of the programs that run the command after them.
var wrapperOptions = map[string][]string{
	"sudo": {"-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-T", "-U", "-R"},
	"env":  {"-u", "-C", "-S"},
	"time": {"-f", "-o"},
}

// program returns chalkctl's or chalklab's name and arguments when cmd runs one of them: directly,
// after variables set for it, through sudo, env or time, or with nix run from a flake.
func program(cmd []string) []string {
	for len(cmd) > 0 {
		w := cmd[0]
		switch {
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-"):
			cmd = cmd[1:]
		case wrapperOptions[w] != nil:
			cmd = cmd[1:]
			for len(cmd) > 0 && strings.HasPrefix(cmd[0], "-") {
				opt := cmd[0]
				cmd = cmd[1:]
				if opt == "--" {
					break
				}
				if slices.Contains(wrapperOptions[w], opt) && len(cmd) > 0 {
					cmd = cmd[1:]
				}
			}
		case w == "nix" && len(cmd) > 1 && cmd[1] == "run":
			// nix run [options] <flake>#<program> [-- arguments]
			for i, a := range cmd[2:] {
				if _, name, ok := strings.Cut(a, "#"); ok && slices.Contains(programNames, name) {
					args := cmd[3+i:]
					if len(args) > 0 && args[0] == "--" {
						args = args[1:]
					}
					return append([]string{name}, args...)
				}
			}
			return nil
		case slices.Contains(programNames, filepath.Base(w)):
			return append([]string{filepath.Base(w)}, cmd[1:]...)
		default:
			return nil
		}
	}
	return nil
}

// operators end a simple command; redirections end its arguments.
var (
	operators    = []string{"|", "||", "&&", ";", "&", "(", ")"}
	redirections = regexp.MustCompile(`^[0-9]*(>|>>|<|<<|>&|<&|&>)`)
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
		case redirections.MatchString(w):
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

// quotedSubstitutions returns the commands of the command substitutions within double quotes of
// a line, outside its comment; words keeps them inside their words.
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
				}
			}
			i = end
		}
	}
	return subs
}

// checkInvocation checks that a program has the command args name and the flags they pass.
// Positional arguments and flag values are not checked: documentation writes placeholders.
func checkInvocation(roots []*cobra.Command, args []string) error {
	var root *cobra.Command
	for _, r := range roots {
		if r.Name() == args[0] {
			root = r
		}
	}
	if root == nil {
		return fmt.Errorf("no program %s", args[0])
	}
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	cmd, rest, err := root.Find(args[1:])
	if err != nil {
		return err
	}
	cmd.InitDefaultHelpFlag()
	fs := cmd.Flags()
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		switch {
		case arg == "--":
			return nil
		case strings.HasPrefix(arg, "--"):
			name, _, hasValue := strings.Cut(arg[2:], "=")
			f := fs.Lookup(name)
			if f == nil {
				return fmt.Errorf("%s has no flag --%s", cmd.CommandPath(), name)
			}
			if !hasValue && f.NoOptDefVal == "" {
				if i+1 == len(rest) {
					return fmt.Errorf("--%s of %s needs a value", name, cmd.CommandPath())
				}
				i++
			}
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			// pflag reads -abc as -a -b -c; a flag that takes a value takes the rest of the word,
			// or the next word. A long flag's name after one dash is a mistake, though.
			if name, _, _ := strings.Cut(arg[1:], "="); len(name) > 1 && fs.Lookup(name) != nil {
				return fmt.Errorf("%s has no flag %s; long flags start with --", cmd.CommandPath(), arg)
			}
			shorts := arg[1:]
			for j := 0; j < len(shorts); j++ {
				f := fs.ShorthandLookup(shorts[j : j+1])
				if f == nil {
					return fmt.Errorf("%s has no flag -%c", cmd.CommandPath(), shorts[j])
				}
				if j+1 < len(shorts) && shorts[j+1] == '=' {
					break
				}
				if f.NoOptDefVal == "" {
					if j+1 == len(shorts) {
						if i+1 == len(rest) {
							return fmt.Errorf("-%c of %s needs a value", shorts[j], cmd.CommandPath())
						}
						i++
					}
					break
				}
			}
		case cmd.HasAvailableSubCommands():
			return fmt.Errorf("%s has no command %q", cmd.CommandPath(), arg)
		}
	}
	return nil
}
