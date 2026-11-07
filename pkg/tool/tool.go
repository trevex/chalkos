// Package tool runs the command-line tools that chalkd, the installer and the storage code
// drive, such as cryptsetup, sfdisk and systemctl, behind an interface that tests replace.
package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Cmd is a tool to run: its name, looked up in PATH, and its arguments.
type Cmd struct {
	Name string
	Args []string
	// Env adds environment variables. Secrets travel this way, never as arguments, which other
	// processes can read and errors repeat.
	Env []string
	// Stdin is written to the tool's standard input, so a secret reaches only that short-lived
	// process. Runners never log it.
	Stdin []byte
	// Quiet keeps the tool's standard error off the console, so its error carries it alone: for a
	// tool that runs often and whose failure the caller reports, such as on each status request.
	Quiet bool
}

// String is the command line: the name and arguments joined by spaces.
func (c Cmd) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

// Runner runs a tool and returns its standard output. Tests replace it with a recording fake.
type Runner interface {
	Exec(ctx context.Context, c Cmd) ([]byte, error)
}

// Run runs the tool name with args on r.
func Run(ctx context.Context, r Runner, name string, args ...string) ([]byte, error) {
	return r.Exec(ctx, Cmd{Name: name, Args: args})
}

// Error is a tool that ran and exited with a non-zero status.
type Error struct {
	Command string
	Code    int
	Stderr  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s exited with status %d: %s", e.Command, e.Code, strings.TrimSpace(e.Stderr))
}

// ExecRunner runs tools from PATH. Their standard error also reaches the console, which shows
// the unit's output during boot, unless the Cmd is Quiet.
type ExecRunner struct{}

func (ExecRunner) Exec(ctx context.Context, c Cmd) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if c.Env != nil {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var console io.Writer = os.Stderr
	if c.Quiet {
		console = io.Discard
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(console, &stderr)
	err := cmd.Run()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return stdout.Bytes(), &Error{Command: c.String(), Code: ee.ExitCode(), Stderr: stderr.String()}
	}
	return stdout.Bytes(), err
}
