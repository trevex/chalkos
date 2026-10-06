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

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.RunWithEnv(ctx, nil, name, args...)
}

func (ExecRunner) RunWithEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
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
