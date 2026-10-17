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
// changing it: the whole file system, STATE among it, is read-only, the unit has no network and
// cannot gain privileges. Devices stay accessible, since resolving disks and testing a
// passphrase read them; cryptsetup's lock directory stays writable, where it exists, for the
// passphrase test. RuntimeMaxSec stops the unit itself when chalkd stops waiting.
type Sandbox struct{}

func (Sandbox) Run(ctx context.Context, argv []string, stdin []byte, limit int) ([]byte, error) {
	return run(ctx, sandboxed(argv), stdin, limit)
}

func sandboxed(argv []string) []string {
	return append([]string{
		"systemd-run", "--pipe", "--wait", "--quiet", "--collect",
		"--property=ProtectSystem=strict",
		"--property=ReadOnlyPaths=/state",
		"--property=ReadWritePaths=-/run/cryptsetup",
		"--property=PrivateNetwork=yes",
		"--property=NoNewPrivileges=yes",
		fmt.Sprintf("--property=RuntimeMaxSec=%d", int(Timeout.Seconds())),
		"--",
	}, argv...)
}

// run runs argv with stdin and returns its standard output, refusing more than limit bytes. A
// failure carries what the program wrote to standard error.
func run(ctx context.Context, argv []string, stdin []byte, limit int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	out := &limited{limit: limit}
	stderr := &capped{limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	err := cmd.Run()
	if out.over {
		return nil, fmt.Errorf("it printed more than %d bytes", limit)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.buf.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out.buf.Bytes(), nil
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
