// Package command runs a program of the image's store with bounded output, for command facts and
// the renderers and checks of outputs. A program can echo the config it reads, so a failure's
// error names the exit status alone and keeps what it wrote to standard error apart.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// MaxOutput is the most a program may print on standard output.
const MaxOutput = 1 << 20

// maxStderr bounds what an Error keeps of a program's standard error.
const maxStderr = 4096

// Run runs argv with stdin and env added to the caller's environment, and returns its standard
// output, refusing more than limit bytes. A program that fails is an Error that holds the start of
// what it wrote to standard error.
func Run(ctx context.Context, argv []string, stdin []byte, limit int, env ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdin = bytes.NewReader(stdin)
	out := &limited{limit: limit}
	stderr := &capped{limit: maxStderr}
	cmd.Stdout = out
	cmd.Stderr = stderr
	err := cmd.Run()
	if out.over {
		return nil, fmt.Errorf("it printed more than %d bytes", limit)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		status := fmt.Sprintf("exit %d", exit.ExitCode())
		if exit.ExitCode() < 0 {
			status = exit.String()
		}
		return nil, &Error{Status: status, Stderr: strings.TrimSpace(stderr.buf.String())}
	}
	if err != nil {
		return nil, err
	}
	return out.buf.Bytes(), nil
}

// Error is a program that failed. Its error names the exit status alone: the program can echo
// what it reads on standard input, the config, so chalkd logs what it wrote to standard error to
// the journal instead of returning it.
type Error struct {
	// Status is "exit N", or the signal that ended the program.
	Status string
	Stderr string
}

func (e *Error) Error() string {
	return "the command failed (" + e.Status + "); see the node's journal"
}

// capped keeps the start of what a program writes to standard error, up to limit bytes.
type capped struct {
	buf   bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// limited keeps what a program prints up to limit bytes and notes when it prints more.
type limited struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (l *limited) Write(p []byte) (int, error) {
	if l.buf.Len()+len(p) > l.limit {
		l.over = true
		return 0, errors.New("output limit reached")
	}
	return l.buf.Write(p)
}
