package diagnostics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/command"
)

// Sandbox runs a diagnostic as a transient unit under systemd-run --pipe: a dynamic user with only
// the capabilities and groups it names, in command.Hardened without devices, STATE or /var/lib,
// Unix, IP and netlink sockets alone in the host's network or the namespace it names, and at most
// its timeout. A failed unit stays loaded, so its result tells a timeout apart from an exit status;
// chalkd resets it after reading it.
type Sandbox struct {
	// NamespaceDir holds the named network namespaces a run may join; empty for NamespaceDir.
	NamespaceDir string
}

// ErrNoNamespace is what a run in a network namespace that does not exist fails with, before its
// unit starts: systemd would fail the unit with an exit status that reads as the program's.
var ErrNoNamespace = errors.New("the network namespace does not exist")

// noNamespace is ErrNoNamespace for the namespace of that name.
type noNamespace string

func (n noNamespace) Error() string { return "the network namespace " + string(n) + " does not exist" }

func (noNamespace) Is(err error) bool { return err == ErrNoNamespace }

// namespaces is the directory of the named network namespaces.
func (s Sandbox) namespaces() string {
	if s.NamespaceDir != "" {
		return s.NamespaceDir
	}
	return NamespaceDir
}

// stopMargin is how much longer than a run's timeout chalkd waits for systemd-run, so the unit's
// own limit stops the program first and its result says so.
const stopMargin = 10 * time.Second

// sandbox holds the properties of every run's unit, command.Hardened and its own.
var sandbox = append(slices.Clone(command.Hardened),
	"--property=DynamicUser=yes",
	"--property=PrivateTmp=yes",
	"--property=PrivateDevices=yes",
	"--property=RestrictNamespaces=yes",
	"--property=RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK",
	"--property=InaccessiblePaths=-/state -/var/lib",
	// A run cannot starve the node it diagnoses.
	"--property=MemoryMax=256M",
)

// args are the arguments of systemd-run that run r as unit.
func (s Sandbox) args(unit string, r Run) []string {
	caps := strings.Join(r.Capabilities, " ")
	// Without --expand-environment=no, systemd-run would expand $NAME and ${NAME} in the
	// command's arguments, which hold the call's values.
	args := append([]string{"--pipe", "--wait", "--quiet", "--expand-environment=no", "--unit=" + unit}, sandbox...)
	args = append(args,
		"--property=CapabilityBoundingSet="+caps,
		"--property=AmbientCapabilities="+caps,
		fmt.Sprintf("--property=RuntimeMaxSec=%dms", r.Timeout.Milliseconds()),
	)
	if len(r.Groups) > 0 {
		args = append(args, "--property=SupplementaryGroups="+strings.Join(r.Groups, " "))
	}
	// systemd joins the namespace before it drops the run's privileges, which RestrictNamespaces=
	// would refuse the program itself.
	if r.NetworkNamespace != "" {
		args = append(args, "--property=NetworkNamespacePath="+filepath.Join(s.namespaces(), r.NetworkNamespace))
	}
	return append(append(args, "--"), r.Argv...)
}

// unitName is a name for a run of the diagnostic that no other run has.
func unitName(diagnostic string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return "chalkos-diagnostic-" + diagnostic + "-" + hex.EncodeToString(b) + ".service"
}

func (s Sandbox) Run(ctx context.Context, r Run, stdout, stderr io.Writer) (Exit, error) {
	if r.Timeout <= 0 {
		return Exit{}, fmt.Errorf("diagnostic %s: its timeout %v is not positive", r.Name, r.Timeout)
	}
	if r.NetworkNamespace != "" {
		if err := checkNamespace(r.NetworkNamespace); err != nil {
			return Exit{}, fmt.Errorf("diagnostic %s: %w", r.Name, err)
		}
		if _, err := os.Stat(filepath.Join(s.namespaces(), r.NetworkNamespace)); errors.Is(err, fs.ErrNotExist) {
			return Exit{}, noNamespace(r.NetworkNamespace)
		} else if err != nil {
			return Exit{}, fmt.Errorf("diagnostic %s: %w", r.Name, err)
		}
	}
	unit := unitName(r.Name)
	waitCtx, cancel := context.WithTimeout(ctx, r.Timeout+stopMargin)
	defer cancel()
	cmd := exec.CommandContext(waitCtx, "systemd-run", s.args(unit, r)...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// The unit's program holds the pipes, so the unit stops first, then systemd-run.
	stopped := false
	cmd.Cancel = func() error {
		stopped = true
		command.StopUnit(unit)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = stopMargin
	err := cmd.Run()
	if ctx.Err() != nil || waitCtx.Err() != nil {
		if !stopped {
			command.StopUnit(unit)
		}
		if ctx.Err() != nil {
			return Exit{}, ctx.Err()
		}
		return Exit{Stopped: StoppedTimeout}, nil
	}
	if err == nil {
		return Exit{}, nil
	}
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return Exit{}, err
	}
	result, status, ok := command.FailedUnit(unit)
	code, codeErr := strconv.Atoi(strings.TrimPrefix(status, "exit "))
	switch {
	case ok && result == "exit-code" && codeErr == nil:
		return Exit{Code: code}, nil
	case !ok || result == "exit-code" || result == "success" || result == "":
		// The manager did not answer, or systemd-run failed itself, as when the unit could not be
		// started, and said why on standard error.
		return Exit{Code: exit.ExitCode()}, nil
	default:
		return Exit{Stopped: result}, nil
	}
}
