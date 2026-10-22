package facts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Sandbox runs command facts as transient units of systemd, so a fact reads the node without
// changing it: the file system, STATE among it where the node has one, is read-only but for /dev
// and the unit's own, writable /tmp and /var/tmp; home directories are hidden; the kernel's
// tunables and control groups cannot be changed, and the unit cannot load a kernel module itself,
// although the kernel still loads one on request, as for a socket family; the unit has no
// network, opens no socket but Unix and kernel crypto ones, and cannot gain privileges. Devices
// stay accessible and the unit keeps root's capabilities but CAP_SYS_MODULE, since resolving
// disks and testing a passphrase read block devices and cryptsetup may set up a temporary
// device-mapper mapping for a cipher the kernel crypto API lacks; cryptsetup's lock directory
// stays writable, where it exists, for the passphrase test.
type Sandbox struct{}

func (Sandbox) Run(ctx context.Context, argv []string, stdin []byte, limit int) ([]byte, error) {
	return RunCommand(ctx, Sandboxed(argv, "--property=ReadWritePaths=-/run/cryptsetup"), stdin, limit)
}

// Sandboxed is the command line that runs argv in the sandbox Sandbox describes, with the extra
// options of systemd-run given.
func Sandboxed(argv []string, extra ...string) []string {
	args := append([]string{"systemd-run", "--pipe", "--wait", "--quiet", "--collect"}, SandboxProperties()...)
	// RuntimeMaxSec stops the unit itself when chalkd stops waiting.
	args = append(args, fmt.Sprintf("--property=RuntimeMaxSec=%d", int(Timeout.Seconds())))
	args = append(args, extra...)
	return append(append(args, "--"), argv...)
}

// SandboxProperties are the properties of the sandbox Sandbox describes, without cryptsetup's
// lock directory and the time limit, as options of systemd-run.
func SandboxProperties() []string {
	return []string{
		"--property=ProtectSystem=strict",
		"--property=ReadOnlyPaths=-/state",
		"--property=ProtectHome=yes",
		"--property=PrivateTmp=yes",
		"--property=ProtectKernelTunables=yes",
		"--property=ProtectKernelModules=yes",
		"--property=ProtectControlGroups=yes",
		"--property=PrivateNetwork=yes",
		"--property=RestrictAddressFamilies=AF_UNIX AF_ALG",
		"--property=NoNewPrivileges=yes",
	}
}

// RunCommand runs argv with stdin and env added to chalkd's environment, and returns its standard
// output, refusing more than limit bytes. A program that fails is a CommandError that holds the
// start of what it wrote to standard error.
func RunCommand(ctx context.Context, argv []string, stdin []byte, limit int, env ...string) ([]byte, error) {
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
