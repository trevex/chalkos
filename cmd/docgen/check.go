package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// shellLanguages are the code blocks whose commands are checked; a block without a language,
// also one with attributes alone, counts as shell.
var shellLanguages = []string{"", "sh", "bash", "shell", "zsh", "console", "shell-session"}

// check finds the chalkctl, chalklab, chalkbuild and chalkcfg commands in the shell code blocks of
// the Markdown files under dir, but not under dir/superpowers, and returns an error naming each
// one that runs a command or passes a flag the programs do not have. newRoots returns fresh
// command trees, as parsing changes a tree's flags.
func check(newRoots func() []*cobra.Command, dir string) error {
	files, err := markdownFiles(dir)
	if err != nil {
		return err
	}
	var problems []string
	for _, path := range files {
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
	}
	if len(problems) > 0 {
		return errors.New("commands in the documentation that chalkctl, chalklab, chalkbuild and chalkcfg do not have:\n" + strings.Join(problems, "\n"))
	}
	return nil
}

// block is a shell code block: its lines and the line number of the first one.
type block struct {
	first   int
	console bool
	lines   []string
}

// shellBlocks returns the shell code blocks of a Markdown file.
func shellBlocks(path string) ([]block, error) {
	cbs, err := codeBlocks(path, shellLanguages...)
	if err != nil {
		return nil, err
	}
	blocks := make([]block, len(cbs))
	for i, b := range cbs {
		blocks[i] = block{first: b.first, console: b.lang == "console" || b.lang == "shell-session", lines: b.lines}
	}
	return blocks, nil
}

// invocation is a command line that runs chalkctl, chalklab, chalkbuild or chalkcfg: the program
// and its arguments.
type invocation struct {
	line int
	args []string
}

// programNames are the programs whose commands are checked.
var programNames = []string{"chalkctl", "chalklab", "chalkbuild", "chalkcfg"}

// invocations returns the chalkctl, chalklab, chalkbuild and chalkcfg command lines of a block.
// Lines continued with a backslash are joined, and a $ prompt is dropped; in a console block, only
// lines with a $ prompt are commands, and the lines they continue on may start with a > prompt.
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

// program returns the name of one of programNames and its arguments when cmd runs one of them:
// directly, after variables set for it, through a wrapper, with nix run from a flake or in nix
// develop.
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
