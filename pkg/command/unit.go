package command

import (
	"context"
	"strings"
	"time"
)

// Manager is the environment with which systemd-run and systemctl reach systemd's manager on its
// private socket: chalkd renders before sysinit.target, where the system bus does not run yet.
const Manager = "DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/systemd/private"

// StoreOnly are the properties of a transient unit that runs programs from the store alone: in the
// unit's mount namespace no other file system lets a file run as a program.
var StoreOnly = []string{"--property=NoExecPaths=/", "--property=ExecPaths=/nix/store"}

// UnitTimeout bounds each question to the manager about a unit, asked after the caller's context
// may have ended.
const UnitTimeout = 10 * time.Second

// systemctl runs systemctl on the manager with args.
func systemctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), UnitTimeout)
	defer cancel()
	return Run(ctx, append([]string{"systemctl"}, args...), nil, MaxOutput, Manager)
}

// StopUnit stops and resets a transient unit: one systemd-run stopped waiting for, which may run
// on, or one a crashed chalkd left behind, which would keep a new unit of its name from starting.
// It reports no errors.
func StopUnit(unit string) {
	systemctl("stop", unit)
	systemctl("reset-failed", unit)
}

// FailedUnit asks the manager why a transient oneshot unit failed, which it keeps until the unit
// is reset, and resets it. status is "exit N", "unknown" or the Result; ok is false when the
// manager did not answer.
func FailedUnit(unit string) (result, status string, ok bool) {
	show, err := systemctl("show", "--property=Result", "--property=ExecMainStatus", unit)
	systemctl("reset-failed", unit)
	if err != nil {
		return "", "", false
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(show), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	result = props["Result"]
	switch {
	case result != "exit-code" && result != "":
		status = result
	case props["ExecMainStatus"] == "":
		status = "unknown"
	default:
		status = "exit " + props["ExecMainStatus"]
	}
	return result, status, true
}
