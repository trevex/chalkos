package outputs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/command"
)

// A role is what an output's program does: render its files or check them.
type role int

const (
	roleRenderer role = iota
	roleCheck
)

func (r role) String() string {
	if r == roleCheck {
		return "check"
	}
	return "renderer"
}

// A Program is a renderer or a check to run: its argv, the input it reads on standard input, and
// $OUT, the directory of the output's files, which a renderer may write and a check may only read.
// Timeout bounds it.
type Program struct {
	Argv    []string
	Stdin   []byte
	Out     string
	Write   bool
	Timeout time.Duration
}

// Runner runs renderers and checks. A program that fails is a ProgramError, and one that does not
// finish within its timeout an error that wraps context.DeadlineExceeded.
type Runner interface {
	Run(ctx context.Context, p Program) error
}

// A ProgramError is a renderer or a check that failed. Status is "exit N", or why systemd
// stopped it; Stderr is the start of what it wrote to standard error, or, in the sandbox, empty,
// and Unit names the transient unit whose journal holds it.
type ProgramError struct {
	Status, Stderr, Unit string
}

func (e *ProgramError) Error() string { return "the program failed (" + e.Status + ")" }

// RenderUser is the unprivileged system user renderers and checks run as on a node, which owns
// $OUT while the renderer writes it.
const RenderUser = "chalkos-render"

// Sandbox runs programs as oneshot transient units that write standard error to the journal, in a
// sandbox of their own: as RenderUser without capabilities, devices or network, on a read-only
// system without STATE, /var, or the config, credentials and Kubernetes keys chalkd writes below
// /run/chalkos, so a program reads no Secret's value; a renderer may write $OUT alone, and a check
// may open netlink sockets besides Unix ones. Through netlink a check reaches only its own
// network namespace, but nf_tables there makes the kernel load the modules of the expressions a
// ruleset uses, ProtectKernelModules notwithstanding.
// chalkos-config renders before sysinit.target, where the system bus does not run yet, so
// systemd-run reaches the manager on its private socket, on which it cannot follow a unit as
// --wait and --pipe do: the start job of a oneshot unit finishes when the program exits, the
// program reads standard input from a file, and its exit status is the unit's. The units have no
// default dependencies, which would wait for sysinit.target.
type Sandbox struct{}

// manager reaches systemd's manager without the system bus.
const manager = "DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/systemd/private"

// stopMargin is how much longer than a program's timeout chalkd waits for systemd-run, so the
// unit's own timeout stops the program first and its result says so.
const stopMargin = 10 * time.Second

// defaultTimeout bounds a program when the Renderer sets no timeout. A delivery renders its
// outputs under the config lock.
const defaultTimeout = 30 * time.Second

// sandbox holds the properties of the units Sandbox runs.
var sandbox = []string{
	"--property=User=" + RenderUser,
	"--property=CapabilityBoundingSet=",
	"--property=AmbientCapabilities=",
	"--property=NoNewPrivileges=yes",
	"--property=ProtectSystem=strict",
	"--property=ProtectHome=yes",
	"--property=ProtectProc=invisible",
	// A private /tmp, small enough that a renderer cannot fill memory through it.
	"--property=TemporaryFileSystem=/tmp:size=16M,mode=1777",
	"--property=PrivateDevices=yes",
	"--property=PrivateNetwork=yes",
	"--property=ProtectKernelTunables=yes",
	"--property=ProtectKernelModules=yes",
	"--property=ProtectKernelLogs=yes",
	"--property=ProtectControlGroups=yes",
	"--property=InaccessiblePaths=-/state -/var -/run/chalkos/config -/run/chalkos/credentials -/run/chalkos/kubernetes",
	"--property=DefaultDependencies=no",
}

// sandboxArgs is the systemd-run command line that runs p as unit, reading input.
func sandboxArgs(unit string, p Program, input string) []string {
	args := append([]string{"systemd-run", "--quiet", "--service-type=oneshot", "--unit=" + unit}, sandbox...)
	args = append(args,
		// The start of a oneshot unit lasts until its program exits.
		fmt.Sprintf("--property=TimeoutStartSec=%dms", p.Timeout.Milliseconds()),
		"--setenv=OUT="+p.Out,
		"--property=StandardInput=file:"+input,
		"--property=StandardOutput=null",
		"--property=StandardError=journal",
	)
	if p.Write {
		args = append(args, "--property=ReadWritePaths="+p.Out, "--property=RestrictAddressFamilies=AF_UNIX")
	} else {
		// A check may also talk netlink, which reaches the network namespace PrivateNetwork gives
		// it or one of its own, so nft --check can take a ruleset to the kernel.
		args = append(args, "--property=RestrictAddressFamilies=AF_UNIX AF_NETLINK")
	}
	return append(append(args, "--"), p.Argv...)
}

func (Sandbox) Run(ctx context.Context, p Program) error {
	input := p.Out + ".input"
	if err := os.WriteFile(input, p.Stdin, 0o600); err != nil {
		return err
	}
	defer os.Remove(input)
	if p.Write {
		u, err := user.Lookup(RenderUser)
		if err != nil {
			return fmt.Errorf("the renderers' user: %w", err)
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		if err := os.Chown(p.Out, uid, gid); err != nil {
			return err
		}
	}
	unit := "chalkos-render-" + filepath.Ext(p.Out)[1:] + ".service"
	waitCtx, cancel := context.WithTimeout(ctx, p.Timeout+stopMargin)
	defer cancel()
	_, err := command.Run(waitCtx, sandboxArgs(unit, p, input), nil, command.MaxOutput, manager)
	// What follows asks the manager about the unit, as long as it answers.
	systemctl := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), stopMargin)
		defer cancel()
		return command.Run(ctx, append([]string{"systemctl"}, args...), nil, command.MaxOutput, manager)
	}
	if waitCtx.Err() != nil {
		// systemd-run is gone, and the unit may run on.
		systemctl("stop", unit)
		systemctl("reset-failed", unit)
		return fmt.Errorf("%s: %w", unit, waitCtx.Err())
	}
	var exit *command.Error
	if !errors.As(err, &exit) {
		return err
	}
	// A unit that failed stays loaded, with its result, until it is reset.
	show, serr := systemctl("show", "--property=Result", "--property=ExecMainStatus", unit)
	systemctl("reset-failed", unit)
	if serr != nil {
		return &ProgramError{Status: exit.Status, Unit: unit}
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(show), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	if props["Result"] == "timeout" {
		return fmt.Errorf("%s: %w", unit, context.DeadlineExceeded)
	}
	status := "exit " + props["ExecMainStatus"]
	if r := props["Result"]; r != "exit-code" && r != "" {
		status = r
	}
	return &ProgramError{Status: status, Unit: unit}
}

// Direct runs programs as they are, as the image's build and tests do; a check may write $OUT.
type Direct struct{}

func (Direct) Run(ctx context.Context, p Program) error {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	_, err := command.Run(ctx, p.Argv, p.Stdin, command.MaxOutput, "OUT="+p.Out)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failed *command.Error
	if errors.As(err, &failed) {
		return &ProgramError{Status: failed.Status, Stderr: failed.Stderr}
	}
	return err
}
