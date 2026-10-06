package main

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

// runner runs a tool and returns its standard output. Tests replace it with a recording fake.
type runner interface {
	run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// toolError is a tool that ran and exited with a non-zero status.
type toolError struct {
	command string
	code    int
	stderr  string
}

func (e *toolError) Error() string {
	return fmt.Sprintf("%s exited with status %d: %s", e.command, e.code, strings.TrimSpace(e.stderr))
}

// exitCode returns the exit status of a failed tool, or -1 when err is not a tool's failure.
func exitCode(err error) int {
	var te *toolError
	if errors.As(err, &te) {
		return te.code
	}
	return -1
}

// execRunner runs tools from PATH. Their standard error also reaches the console, which shows
// the unit's output during boot.
type execRunner struct{}

func (execRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.Bytes(), &toolError{command: name + " " + strings.Join(args, " "), code: ee.ExitCode(), stderr: stderr.String()}
	}
	return stdout.Bytes(), err
}
