// Package chalklab is the chalklab command, which runs the nodes of a chalkos cluster as QEMU
// virtual machines on this machine: a lab with Secure Boot and a TPM per node, kept running by a
// supervisor in the background. NewCommand returns its command tree.
package chalklab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/lab"
)

// app is chalklab's environment, which every command shares. Its streams are those of the
// command that runs, so tests and programs embedding chalklab capture them.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// output runs a program and returns its standard output; its errors go to stderr.
	output func(ctx context.Context, name string, args ...string) ([]byte, error)
	// chalkctl runs chalkctl with its output on out: its errors too when out is not stdout, as when
	// chalklab reads what it prints. chalkctl asks for secrets on the terminal itself.
	chalkctl func(ctx context.Context, out io.Writer, args ...string) error
	// supervise is the hidden command that runs a lab's supervisor.
	supervise *cobra.Command
}

// superviseName is the name of the hidden command that runs a lab's supervisor.
const superviseName = "supervise"

// NewCommand returns chalklab's root command, which runs programs and chalkctl from PATH.
func NewCommand() *cobra.Command {
	a := &app{}
	a.output = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stderr = a.stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return out, nil
	}
	a.chalkctl = func(ctx context.Context, out io.Writer, args ...string) error {
		cmd := exec.CommandContext(ctx, "chalkctl", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = a.stdin, out, out
		if out == a.stdout {
			cmd.Stderr = a.stderr
		}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("chalkctl %s: %w", args[0], err)
		}
		return nil
	}
	return newCommand(a)
}

// with sets the environment's streams to those of cmd. The functions that run programs read
// them, so the environment is changed in place.
func (a *app) with(cmd *cobra.Command) *app {
	a.stdin, a.stdout, a.stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	return a
}

func newCommand(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:           "chalklab",
		Short:         "Run a chalkos cluster's nodes as QEMU virtual machines on this machine",
		Long:          rootLong,
		Example:       rootExample,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("%w: unknown command %q for %q", errUsage, args[0], cmd.CommandPath())
			}
			return nil
		},
		RunE: func(*cobra.Command, []string) error { return errUsage },
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w; %s --help lists its flags", err, cmd.CommandPath())
	})
	a.supervise = &cobra.Command{
		Use:    superviseName + " <dir>",
		Short:  "Run a lab's VMs in the background",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errUsage
			}
			// The supervisor create starts in the background ignores the terminal going away.
			signal.Ignore(syscall.SIGHUP)
			if err := lab.DenyIOURing(); err != nil {
				return err
			}
			return lab.Supervise(cmd.Context(), args[0], cmd.ErrOrStderr())
		},
	}
	root.AddCommand(
		a.createCommand(),
		a.statusCommand(),
		a.startCommand(),
		a.consoleCommand(),
		a.signCommand(),
		a.destroyCommand(),
		a.supervise,
	)
	return root
}

// command makes cmd run run with the environment and streams of the command that runs.
func (a *app) command(cmd *cobra.Command, run func(a *app, ctx context.Context, args []string) error) *cobra.Command {
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return run(a.with(c), c.Context(), args)
	}
	return cmd
}

// Execute runs chalklab's command tree with ctx, prints its error and returns the exit status:
// 2 and the command's usage after a usage error, 1 after another error.
func Execute(ctx context.Context, root *cobra.Command) int {
	cmd, err := root.ExecuteContextC(ctx)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		if err != errUsage {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", root.Name(), err)
		}
		fmt.Fprint(cmd.ErrOrStderr(), cmd.UsageString())
		return 2
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", root.Name(), err)
	return 1
}

var errUsage = errors.New("usage")

// superviseArgs are the arguments that run the supervisor of the lab in dir: the path of the
// supervise command below the root, so a program running chalklab's tree under a root of its
// own starts the supervisor without code of its own.
func (a *app) superviseArgs(dir string) []string {
	return append(strings.Fields(a.supervise.CommandPath())[1:], dir)
}
