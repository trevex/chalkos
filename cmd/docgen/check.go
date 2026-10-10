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
			lang := strings.Trim(m[2], "{}.")
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
// backslash are joined; in a console block, only lines with a $ prompt are commands.
func invocations(b block) []invocation {
	var invs []invocation
	for i := 0; i < len(b.lines); i++ {
		start := b.first + i
		line := strings.TrimSpace(b.lines[i])
		for strings.HasSuffix(line, "\\") && i+1 < len(b.lines) {
			i++
			line = strings.TrimSuffix(line, "\\") + " " + strings.TrimSpace(b.lines[i])
		}
		if b.console {
			rest, ok := strings.CutPrefix(line, "$ ")
			if !ok {
				continue
			}
			line = rest
		}
		for _, cmd := range simpleCommands(words(line)) {
			// Variables set for the command and sudo come before the program.
			for len(cmd) > 0 && (cmd[0] == "sudo" || strings.Contains(cmd[0], "=") && !strings.HasPrefix(cmd[0], "-")) {
				cmd = cmd[1:]
			}
			if len(cmd) > 0 && slices.Contains(programNames, filepath.Base(cmd[0])) {
				invs = append(invs, invocation{line: start, args: append([]string{filepath.Base(cmd[0])}, cmd[1:]...)})
			}
		}
	}
	return invs
}

// operators end a simple command; redirections end its arguments.
var (
	operators    = []string{"|", "||", "&&", ";", "&", "$(", "(", ")", "`"}
	redirections = regexp.MustCompile(`^[0-9]*(>|>>|<|<<|>&|&>)`)
)

// simpleCommands splits words at shell operators and drops redirections and the comment that
// words ends with.
func simpleCommands(ws []string) [][]string {
	var cmds [][]string
	var cur []string
	target := false
	for _, w := range ws {
		switch {
		case w == "#":
			return append(cmds, cur)
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
	return append(cmds, cur)
}

// words splits a command line as a shell does, roughly: quotes group, a backslash escapes, and
// operators are words of their own. Expansions are left as they are written.
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
		case c == '\'' || c == '"':
			inWord = true
			end := strings.IndexByte(line[i+1:], c)
			if end < 0 {
				cur.WriteString(line[i+1:])
				i = len(line)
				continue
			}
			cur.WriteString(line[i+1 : i+1+end])
			i += end + 1
		case c == '\\' && i+1 < len(line):
			inWord = true
			i++
			cur.WriteByte(line[i])
		case c == '$' && i+1 < len(line) && line[i+1] == '(':
			flush()
			ws = append(ws, "$(")
			i++
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
			short, _, hasValue := strings.Cut(arg[1:], "=")
			if len(short) != 1 {
				return fmt.Errorf("%s has no flag %s; long flags start with --", cmd.CommandPath(), arg)
			}
			f := fs.ShorthandLookup(short)
			if f == nil {
				return fmt.Errorf("%s has no flag -%s", cmd.CommandPath(), short)
			}
			if !hasValue && f.NoOptDefVal == "" {
				i++
			}
		case cmd.HasAvailableSubCommands():
			return fmt.Errorf("%s has no command %q", cmd.CommandPath(), arg)
		}
	}
	return nil
}
