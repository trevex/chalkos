package diagnostics

import (
	"context"
	"io"
	"time"
)

// Run is a diagnostic's command line to run, with what its process may hold and how long it may
// take.
type Run struct {
	// Name is the diagnostic's, which names the run's unit.
	Name         string
	Argv         []string
	Capabilities []string
	Groups       []string
	Timeout      time.Duration
	// NetworkNamespace is the network namespace the run joins, by its name below NamespaceDir;
	// empty for the host's network.
	NetworkNamespace string
}

// Exit is how a run ended: Code is its exit status while Stopped is empty; Stopped says why the
// run ended without exiting by itself otherwise.
type Exit struct {
	Code    int
	Stopped string
}

// Values of Exit.Stopped that the runner and chalkd set; otherwise it holds systemd's result of
// the unit, such as signal.
const (
	StoppedTimeout     = "timeout"
	StoppedOutputLimit = "output-limit"
)

// Runner runs a diagnostic, writing what it prints to stdout and stderr as it prints it, from two
// goroutines. A run whose ctx ends is stopped, and returns ctx's error. The runner does not limit
// the output: the caller cancels ctx when its writers reach MaxOutput. Tests replace it.
type Runner interface {
	Run(ctx context.Context, r Run, stdout, stderr io.Writer) (Exit, error)
}
