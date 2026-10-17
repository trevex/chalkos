package facts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Sandbox runs command facts as transient units of systemd, so a fact reads the node without
// changing it: the whole file system, STATE among it where the node has one, is read-only, home
// directories are hidden and /tmp is the unit's own; the kernel's tunables, modules and control
// groups cannot be changed; the unit has no network, opens no socket but Unix and kernel crypto
// ones, and cannot gain privileges. Devices stay accessible and the unit keeps root's
// capabilities, since resolving disks and testing a passphrase read block devices and cryptsetup
// may set up a temporary device-mapper mapping for a cipher the kernel crypto API lacks;
// cryptsetup's lock directory stays writable, where it exists, for the passphrase test.
// RuntimeMaxSec stops the unit itself when chalkd stops waiting.
type Sandbox struct{}

func (Sandbox) Run(ctx context.Context, argv []string, stdin []byte, limit int) ([]byte, error) {
	return run(ctx, sandboxed(argv), stdin, limit)
}

func sandboxed(argv []string) []string {
	return append([]string{
		"systemd-run", "--pipe", "--wait", "--quiet", "--collect",
		"--property=ProtectSystem=strict",
		"--property=ReadOnlyPaths=-/state",
		"--property=ReadWritePaths=-/run/cryptsetup",
		"--property=ProtectHome=yes",
		"--property=PrivateTmp=yes",
		"--property=ProtectKernelTunables=yes",
		"--property=ProtectKernelModules=yes",
		"--property=ProtectControlGroups=yes",
		"--property=PrivateNetwork=yes",
		"--property=RestrictAddressFamilies=AF_UNIX AF_ALG",
		"--property=NoNewPrivileges=yes",
		fmt.Sprintf("--property=RuntimeMaxSec=%d", int(Timeout.Seconds())),
		"--",
	}, argv...)
}

// run runs argv with stdin and returns its standard output, refusing more than limit bytes. A
// program that fails is a CommandError that holds the start of what it wrote to standard error.
func run(ctx context.Context, argv []string, stdin []byte, limit int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
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
		return nil, &CommandError{Status: status, Stderr: strings.TrimSpace(stderr.buf.String())}
	}
	if err != nil {
		return nil, err
	}
	return out.buf.Bytes(), nil
}

// maxStderr bounds what a CommandError keeps of a program's standard error.
const maxStderr = 4096

// CommandError is a command fact's program that failed. Its error names the exit status alone:
// the program can echo what it reads on standard input, the config, so chalkd logs what it wrote
// to standard error to the journal instead of returning it.
type CommandError struct {
	// Status is "exit N", or the signal that ended the program.
	Status string
	Stderr string
}

func (e *CommandError) Error() string {
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
