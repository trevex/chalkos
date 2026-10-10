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

// shellLanguages are the code blocks whose commands are checked; a block without a language,
// also one with attributes alone, counts as shell, as cobra's pages write them.
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

// fence opens or closes a code block, also indented in a list, an admonition or a tab, and
// holds its info string. The info string of a fence of backticks has no backticks.
var fence = regexp.MustCompile("^\\s*(```+|~~~+)(.*)$")

// language returns the language of a fence's info string: its first word, or the first class of
// an attribute list ({ .sh title="x" }). A fence with attributes alone (title="x") has none.
func language(info string) string {
	info = strings.TrimSpace(info)
	if attrs, ok := strings.CutPrefix(info, "{"); ok {
		for _, a := range strings.Fields(strings.TrimSuffix(attrs, "}")) {
			if class, ok := strings.CutPrefix(a, "."); ok {
				return strings.ToLower(class)
			}
		}
		return ""
	}
	lang, _, _ := strings.Cut(info, " ")
	if strings.Contains(lang, "=") {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(lang, "."))
}

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
		if m != nil && m[1][0] == '`' && strings.Contains(m[2], "`") {
			m = nil
		}
		switch {
		case open == "" && m != nil:
			open = m[1]
			lang := language(m[2])
			if slices.Contains(shellLanguages, lang) {
				blocks = append(blocks, block{first: n + 1, console: lang == "console" || lang == "shell-session"})
				cur = &blocks[len(blocks)-1]
			}
		case open != "" && m != nil && strings.TrimSpace(m[2]) == "" && strings.HasPrefix(m[1], open):
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
// backslash are joined, and a $ prompt is dropped; in a console block, only lines with a $
// prompt are commands, and the lines they continue on may start with a > prompt.
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
		} else {
			line = strings.TrimPrefix(line, "$ ")
		}
		for _, cmd := range commands(line) {
			if args := program(cmd); args != nil {
				invs = append(invs, invocation{line: start, args: args})
			}
		}
	}
	return invs
}

// commands returns the simple commands of a line, including those in command substitutions,
// $(…) or backticks, within double quotes.
func commands(line string) [][]string {
	cmds := simpleCommands(words(line))
	for _, inner := range quotedSubstitutions(line) {
		cmds = append(cmds, commands(inner)...)
	}
	return cmds
}

// wrapper is a program that runs the command after its options: valued are the options that take
// the next word as their value, operands the words between the options and the command.
type wrapper struct {
	valued   []string
	operands int
}

// wrappers are the programs that run the command after them.
var wrappers = map[string]wrapper{
	"sudo": {valued: []string{
		"-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-T", "-U", "-R",
		"--user", "--group", "--close-from", "--chdir", "--host", "--prompt", "--role", "--type",
		"--command-timeout", "--other-user", "--chroot",
	}},
	"env":     {valued: []string{"-u", "-C", "-S", "--unset", "--chdir", "--split-string"}},
	"time":    {valued: []string{"-f", "-o", "--format", "--output"}},
	"timeout": {valued: []string{"-s", "-k", "--signal", "--kill-after"}, operands: 1},
	"watch":   {valued: []string{"-n", "-q", "--interval", "--equexit"}},
	"exec":    {valued: []string{"-a"}},
}

// program returns chalkctl's or chalklab's name and arguments when cmd runs one of them: directly,
// after variables set for it, through a wrapper, with nix run from a flake or in nix develop.
func program(cmd []string) []string {
	for len(cmd) > 0 {
		w := cmd[0]
		wr, isWrapper := wrappers[w]
		switch {
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-"):
			cmd = cmd[1:]
		case isWrapper:
			cmd = cmd[1:]
			for len(cmd) > 0 && strings.HasPrefix(cmd[0], "-") {
				opt := cmd[0]
				cmd = cmd[1:]
				if opt == "--" {
					break
				}
				if slices.Contains(wr.valued, opt) && len(cmd) > 0 {
					cmd = cmd[1:]
				}
			}
			cmd = cmd[min(wr.operands, len(cmd)):]
			// watch runs a single argument with sh -c.
			if w == "watch" && len(cmd) == 1 {
				if cmds := simpleCommands(words(cmd[0])); len(cmds) > 0 {
					cmd = cmds[0]
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
		case w == "nix" && len(cmd) > 1 && cmd[1] == "develop":
			// nix develop [options] [installable] (-c | --command) <command>
			i := slices.IndexFunc(cmd, func(a string) bool { return a == "-c" || a == "--command" })
			if i < 0 {
				return nil
			}
			cmd = cmd[i+1:]
		case slices.Contains(programNames, filepath.Base(w)):
			return append([]string{filepath.Base(w)}, cmd[1:]...)
		default:
			return nil
		}
	}
	return nil
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
