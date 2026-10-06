package node

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

// Runner runs a tool and returns its standard output. Tests replace it with a recording fake.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// RunWithEnv also sets environment variables. Secrets travel this way, never as arguments,
	// which other processes can read and errors repeat.
	RunWithEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	// RunWithInput writes input to the tool's standard input, so a secret reaches only that
	// short-lived process. Implementations never log the input.
	RunWithInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error)
}

// ToolError is a tool that ran and exited with a non-zero status.
type ToolError struct {
	Command string
	Code    int
	Stderr  string
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("%s exited with status %d: %s", e.Command, e.Code, strings.TrimSpace(e.Stderr))
}

// ExecRunner runs tools from PATH. Their standard error also reaches the console, which shows
// the unit's output during boot.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return run(ctx, nil, nil, name, args...)
}

func (ExecRunner) RunWithEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	return run(ctx, env, nil, name, args...)
}

func (ExecRunner) RunWithInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	return run(ctx, nil, input, name, args...)
}

func run(ctx context.Context, env []string, input []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.Bytes(), &ToolError{Command: name + " " + strings.Join(args, " "), Code: ee.ExitCode(), Stderr: stderr.String()}
	}
	return stdout.Bytes(), err
}
