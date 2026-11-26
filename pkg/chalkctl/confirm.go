package chalkctl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/strs"
)

// declare completes the commands of the tree below root from the Meta each declares: the
// paragraph of its help that names its credentials, and on a command that confirms, --yes and
// the sentence that says when it asks. A group's help gets a paragraph that names the
// credentials of its commands. clientNew's help gets the commands a client file works for.
func declare(root, clientNew *cobra.Command) {
	var clientCommands []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, c := range cmd.Commands() {
			walk(c)
		}
		if cmd == root {
			return
		}
		if cmd.HasSubCommands() {
			cmd.Long = paragraphs(cmd.Long, fill(groupCredentials(cmd)))
			return
		}
		m := layer.MetaOf(cmd)
		if m.Credentials.Role() != "" {
			clientCommands = append(clientCommands, commandName(cmd))
		}
		p := "The command needs " + needs(m.Credentials) + "."
		if strings.Contains(strings.Join(strings.Fields(cmd.Long), " "), p) {
			// A command of a shared package, such as version, may say so itself.
			p = ""
		}
		if m.Confirms != "" {
			cmd.Flags().Bool("yes", false, "go ahead without asking; without a terminal the command refuses unless it is given")
			p += " It " + m.Confirms + "; without a terminal it refuses unless --yes is given."
		}
		if p != "" {
			cmd.Long = paragraphs(cmd.Long, fill(p))
		}
	}
	walk(root)
	paras := strings.Split(clientNew.Long, "\n\n")
	for i, p := range paras {
		if strings.Contains(p, "%s") {
			paras[i] = fill(fmt.Sprintf(p, strs.And(slices.Sorted(slices.Values(clientCommands)))))
		}
	}
	clientNew.Long = strings.Join(paras, "\n\n")
}

// commandName is the command's path below chalkctl, such as node reboot.
func commandName(cmd *cobra.Command) string {
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

// needs completes "The command needs" with the credentials.
func needs(c layer.Credentials) string {
	switch c {
	case layer.NoCredentials:
		return "no secrets file or client file"
	case layer.SecretsFile:
		return "the secrets file"
	case layer.Operator, layer.Admin:
		return "an " + string(c) + " client file or the secrets file"
	case layer.Reader:
		return "a reader client file or the secrets file"
	}
	panic(fmt.Sprintf("chalkctl: a command declares no credentials or unknown ones: %q", c))
}

// credentialsOrder is the order in which a group's help names its commands' credentials.
var credentialsOrder = []layer.Credentials{layer.SecretsFile, layer.Admin, layer.Operator, layer.Reader, layer.NoCredentials}

// groupCredentials is the sentence of a group's help that names the credentials of its commands.
func groupCredentials(group *cobra.Command) string {
	by := map[layer.Credentials][]string{}
	n := 0
	for _, c := range group.Commands() {
		if c.HasSubCommands() || !c.IsAvailableCommand() {
			continue
		}
		cred := layer.MetaOf(c).Credentials
		by[cred] = append(by[cred], commandName(c))
		n++
	}
	if len(by) == 1 {
		for cred := range by {
			if n == 1 {
				return "Its command needs " + needs(cred) + "."
			}
			return "Its commands need " + needs(cred) + "."
		}
	}
	var parts []string
	for _, cred := range credentialsOrder {
		if names := by[cred]; len(names) > 0 {
			verb := " needs "
			if len(names) > 1 {
				verb = " need "
			}
			parts = append(parts, strs.And(names)+verb+needs(cred))
		}
	}
	return strings.Join(parts, "; ") + "."
}

// fill wraps text into lines of at most 100 characters, as the help texts are wrapped.
func fill(text string) string {
	var b strings.Builder
	line := 0
	for _, w := range strings.Fields(text) {
		switch {
		case line == 0:
		case line+1+len(w) > 100:
			b.WriteByte('\n')
			line = 0
		default:
			b.WriteByte(' ')
			line++
		}
		b.WriteString(w)
		line += len(w)
	}
	return b.String()
}

// paragraphs joins text and a paragraph after it.
func paragraphs(text, paragraph string) string {
	if text == "" {
		return paragraph
	}
	return text + "\n\n" + paragraph
}

// errNotConfirmed is the error of a confirmation the user declined.
var errNotConfirmed = errors.New("not confirmed; nothing was changed")

// confirm asks on the terminal before the command does what, a clause such as "storage reset
// wipes the volume data of w1", and fails unless the answer agrees: y or yes, or typed when it is
// not empty, such as the node's name. With --yes it goes ahead without asking; without a terminal
// it refuses and names --yes.
func (a *app) confirm(ctx context.Context, what, typed string) error {
	if a.yes {
		return nil
	}
	prompt := "chalkctl: " + what + ".\nGo ahead? [y/N] "
	if typed != "" {
		prompt = fmt.Sprintf("chalkctl: %s.\nType %s to go ahead: ", what, typed)
	}
	answer, err := a.askLine(ctx, prompt)
	switch {
	case errors.Is(err, errNoTerminal):
		return fmt.Errorf("%s, and there is no terminal to ask whether to go ahead; pass --yes to go ahead", what)
	case err != nil:
		return err
	}
	answer = strings.TrimSpace(answer)
	if typed != "" && answer == typed || typed == "" && (strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")) {
		return nil
	}
	return errNotConfirmed
}

// askLine asks on the terminal for a line, which it returns without its line break: a.ask, else
// standard input when it is a terminal, with the prompt on standard error. It fails with
// errNoTerminal when standard input is no terminal.
func (a *app) askLine(ctx context.Context, prompt string) (string, error) {
	if a.ask != nil {
		return a.ask(ctx, prompt)
	}
	in, ok := a.stdin.(*os.File)
	if !ok || !term.IsTerminal(int(in.Fd())) {
		return "", errNoTerminal
	}
	fmt.Fprint(a.stderr, prompt)
	line, err := readInterruptible(ctx, func() ([]byte, error) {
		line, err := bufio.NewReader(in).ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			err = nil
		}
		return line, err
	}, func() {})
	if errors.Is(err, errInterrupted) {
		fmt.Fprintln(a.stderr)
	}
	return strings.TrimSuffix(string(line), "\n"), err
}
