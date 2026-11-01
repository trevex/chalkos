package command

import (
	"context"
	"strings"
	"time"
)

// Manager is the environment with which systemd-run and systemctl reach systemd's manager on its
// private socket: chalkd renders before sysinit.target, where the system bus does not run yet.
const Manager = "DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/systemd/private"

// UnitTimeout bounds each question to the manager about a unit, asked after the caller's context
// may have ended.
const UnitTimeout = 10 * time.Second

// systemctl runs systemctl on the manager with args.
func systemctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), UnitTimeout)
	defer cancel()
	return Run(ctx, append([]string{"systemctl"}, args...), nil, MaxOutput, Manager)
}

// StopUnit stops a transient unit and resets it: one systemd-run stopped waiting for, which may
// run on, or one a chalkd that crashed left behind, which would keep systemd-run from starting a
// unit of its name. A unit that is not loaded is no error; nothing is.
func StopUnit(unit string) {
	systemctl("stop", unit)
	systemctl("reset-failed", unit)
}

// FailedUnit asks the manager why a transient oneshot unit failed, which it keeps until the unit
// is reset, and resets it. result is the unit's Result, such as "exit-code" or "timeout"; status
// is "exit N" for a program that exited with N, "unknown" when the manager knows no status, and
// the result for any other. ok is false when the manager did not answer.
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
