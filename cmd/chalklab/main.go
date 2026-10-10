// Command chalklab runs the nodes of a chalkos cluster as QEMU virtual machines on this machine:
// a lab with Secure Boot and a TPM per node, kept running by a supervisor in the background.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/trevex/chalkos/pkg/lab"
)

const usage = `usage: chalklab <command> [flags]

commands:
  create [--flake .] [--cluster NAME]     build the kvm images of the cluster's nodes, sign them with
                                          the lab's Secure Boot keys, boot them, install them in place
                                          and bootstrap the first control plane
  status [--cluster NAME]                 show the lab's VMs, ports and console logs
  console <node> [--cluster NAME]         follow a node's serial console
  sign <image> [--out DIR] [--cluster NAME]
                                          sign an image with the lab's Secure Boot keys, for upgrades
  destroy [--cluster NAME]                stop the lab and remove its state

The lab of a cluster lives in $XDG_STATE_HOME/chalklab/<cluster>. Run chalklab <command> -h for
the flags of a command.`

// app is chalklab with its environment, so tests can run commands in process.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// output runs a program and returns its standard output; its errors go to stderr.
	output func(ctx context.Context, name string, args ...string) ([]byte, error)
	// chalkctl runs chalkctl with its output on out: its errors too when out is not stdout, as when
	// chalklab reads what it prints. chalkctl asks for secrets on the terminal itself.
	chalkctl func(ctx context.Context, out io.Writer, args ...string) error
}

func main() {
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := a.run(ctx, os.Args[1:])
	stop()
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		os.Exit(0)
	case errors.Is(err, errUsage):
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "chalklab:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

func (a *app) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "create":
		return a.create(ctx, args[1:])
	case "status":
		return a.status(args[1:])
	case "console":
		return a.console(ctx, args[1:])
	case "sign":
		return a.sign(ctx, args[1:])
	case "destroy":
		return a.destroy(ctx, args[1:])
	case "supervise":
		// The supervisor create starts in the background; it ignores the terminal going away.
		if len(args) != 2 {
			return errUsage
		}
		signal.Ignore(syscall.SIGHUP)
		return lab.Supervise(ctx, args[1])
	}
	return errUsage
}

// parse parses flags anywhere among args and returns the positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
