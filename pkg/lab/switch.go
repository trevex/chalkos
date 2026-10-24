package lab

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// Switch is a vde_switch: an Ethernet switch in user space that connects VMs without
// privileges, also inside the Nix build sandbox.
type Switch struct {
	*daemon
	// Dir holds the switch's sockets; VMs attach to it.
	Dir string
}

// StartSwitch starts a switch with its sockets in dir, which no other switch may use, and waits
// until VMs can attach.
func StartSwitch(ctx context.Context, dir string) (*Switch, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	// The sockets a switch that is gone left, as after a host's reboot, would be taken for this
	// one's.
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	cmd := exec.Command("vde_switch", "--sock", dir, "--dirmode", "0700", "--nostdin", "--pidfile", dir+".pid")
	d, err := startDaemon(ctx, cmd, filepath.Join(dir, "ctl"), "")
	if err != nil {
		return nil, err
	}
	return &Switch{daemon: d, Dir: dir}, nil
}

// stopSwitchIn kills the switch whose sockets are in dir, as another process started it.
func stopSwitchIn(dir string) {
	stopDaemonIn(dir+".pid", dir)
}
