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

// Hardened are the properties every transient unit that runs an image's programs shares:
// renderers, checks and validators, diagnostics, command facts and the image checker. Each
// sandbox adds its user, capabilities, devices, network, namespaces, memory and what it hides
// besides.
//   - The system is read-only, programs run from the store alone, and nothing gains privileges.
//   - Kernel tunables, modules, logs, control groups, the clock and the hostname are out of
//     reach, other users' processes are invisible, and realtime scheduling, personality changes
//     and writable executable memory are refused.
//   - System calls are those of @system-service, systemd's set for services: file, socket,
//     process, signal, timer, IPC and key ring calls, unshare and setns among them, which a
//     check's unshare --map-root-user --net takes. Mounts, module loading, the clock, raw I/O,
//     reboot, swap, tracing, BPF and seccomp are not. A filtered call fails with EPERM, so a
//     program that tries one fails with an error rather than a signal.
//   - /run/chalkos is an empty read-only tmpfs: what chalkd and the layers keep below it, the
//     config, credentials and keys, is hidden whatever its path, and a sandbox binds back what it
//     needs, such as a renderer's $OUT. It is a tmpfs because systemd cannot bind a path below
//     an InaccessiblePaths= path. The units' credentials below /run/credentials are hidden too.
//   - At most 256 processes and threads together: a Go program starts a thread for every four
//     CPUs while it collects garbage.
var Hardened = append([]string{
	"--property=NoNewPrivileges=yes",
	"--property=ProtectSystem=strict",
	"--property=ProtectHome=yes",
	"--property=ProtectProc=invisible",
	"--property=ProtectKernelTunables=yes",
	"--property=ProtectKernelModules=yes",
	"--property=ProtectKernelLogs=yes",
	"--property=ProtectControlGroups=yes",
	"--property=ProtectClock=yes",
	"--property=ProtectHostname=yes",
	"--property=RestrictRealtime=yes",
	"--property=LockPersonality=yes",
	"--property=MemoryDenyWriteExecute=yes",
	"--property=SystemCallArchitectures=native",
	"--property=SystemCallFilter=@system-service",
	"--property=SystemCallErrorNumber=EPERM",
	"--property=TemporaryFileSystem=/run/chalkos:ro",
	"--property=InaccessiblePaths=-/run/credentials",
	"--property=TasksMax=256",
}, StoreOnly...)

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
