package diagnostics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/command"
)

// Sandbox runs a diagnostic as a transient unit of its own, with systemd-run's --pipe, so the
// output reaches chalkd as the program prints it:
//   - under a dynamic user, without new privileges, and with only the capabilities and groups the
//     diagnostic names;
//   - on a read-only system with a private /tmp, without home directories, devices, STATE,
//     /var/lib, or the config, credentials and Kubernetes keys chalkd writes below /run/chalkos;
//   - in the host's network, with Unix, IPv4, IPv6 and netlink sockets alone;
//   - for at most the diagnostic's timeout, after which systemd stops the unit.
//
// A failed unit stays loaded, so its result tells a timeout apart from an exit status; chalkd
// resets it after reading it.
type Sandbox struct{}

// stopMargin is how much longer than a run's timeout chalkd waits for systemd-run, so the unit's
// own limit stops the program first and its result says so; it also bounds each systemctl call.
const stopMargin = 10 * time.Second

// sandbox holds the properties of every run's unit.
var sandbox = []string{
	"--property=DynamicUser=yes",
	"--property=NoNewPrivileges=yes",
	"--property=ProtectSystem=strict",
	"--property=ProtectHome=yes",
	"--property=PrivateTmp=yes",
	"--property=PrivateDevices=yes",
	"--property=ProtectKernelTunables=yes",
	"--property=ProtectKernelModules=yes",
	"--property=ProtectKernelLogs=yes",
	"--property=ProtectControlGroups=yes",
	"--property=ProtectClock=yes",
	"--property=ProtectHostname=yes",
	"--property=RestrictNamespaces=yes",
	"--property=RestrictRealtime=yes",
	"--property=LockPersonality=yes",
	"--property=SystemCallArchitectures=native",
	"--property=RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK",
	"--property=InaccessiblePaths=-/state -/var/lib -/run/chalkos/config -/run/chalkos/credentials -/run/chalkos/kubernetes",
	// A run cannot starve the node it diagnoses.
	"--property=MemoryMax=256M",
	"--property=TasksMax=64",
}

// sandboxArgs are the arguments of systemd-run that run r as unit.
func sandboxArgs(unit string, r Run) []string {
	caps := strings.Join(r.Capabilities, " ")
	args := append([]string{"--pipe", "--wait", "--quiet", "--unit=" + unit}, sandbox...)
	args = append(args,
		"--property=CapabilityBoundingSet="+caps,
		"--property=AmbientCapabilities="+caps,
		fmt.Sprintf("--property=RuntimeMaxSec=%dms", r.Timeout.Milliseconds()),
	)
	if len(r.Groups) > 0 {
		args = append(args, "--property=SupplementaryGroups="+strings.Join(r.Groups, " "))
	}
	return append(append(args, "--"), r.Argv...)
}

// unitName is a name for a run of the diagnostic that no other run has.
func unitName(diagnostic string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return "chalkos-diagnostic-" + diagnostic + "-" + hex.EncodeToString(b) + ".service"
}

// systemctl asks the manager about a unit, for as long as it answers within stopMargin.
func systemctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), stopMargin)
	defer cancel()
	return command.Run(ctx, append([]string{"systemctl"}, args...), nil, command.MaxOutput)
}

func (Sandbox) Run(ctx context.Context, r Run, stdout, stderr io.Writer) (Exit, error) {
	unit := unitName(r.Name)
	waitCtx, cancel := context.WithTimeout(ctx, r.Timeout+stopMargin)
	defer cancel()
	cmd := exec.CommandContext(waitCtx, "systemd-run", sandboxArgs(unit, r)...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// The unit's program holds the pipes, so the unit stops first, then systemd-run.
	cmd.Cancel = func() error {
		systemctl("stop", unit)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = stopMargin
	err := cmd.Run()
	if ctx.Err() != nil || waitCtx.Err() != nil {
		systemctl("reset-failed", unit)
		if ctx.Err() != nil {
			return Exit{}, ctx.Err()
		}
		return Exit{Stopped: StoppedTimeout}, nil
	}
	if err == nil {
		return Exit{}, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return Exit{}, err
	}
	show, serr := systemctl("show", "--property=Result", "--property=ExecMainStatus", unit)
	systemctl("reset-failed", unit)
	if serr != nil {
		return Exit{Code: exit.ExitCode()}, nil
	}
	props := map[string]string{}
	for line := range strings.Lines(string(show)) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	switch props["Result"] {
	case "exit-code":
		code, _ := strconv.Atoi(props["ExecMainStatus"])
		return Exit{Code: code}, nil
	case "success", "":
		// systemd-run failed itself, as when the unit could not be started, and said why on
		// standard error.
		return Exit{Code: exit.ExitCode()}, nil
	default:
		return Exit{Stopped: props["Result"]}, nil
	}
}
