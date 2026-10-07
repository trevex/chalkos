package lab

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Switch is a vde_switch: an Ethernet switch in user space that connects VMs without
// privileges, also inside the Nix build sandbox.
type Switch struct {
	// Dir holds the switch's sockets; VMs attach to it.
	Dir string

	cmd      *exec.Cmd
	exited   chan struct{}
	stopOnce sync.Once
}

// StartSwitch starts a switch with its sockets in dir and waits until VMs can attach.
func StartSwitch(ctx context.Context, dir string) (*Switch, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	cmd := exec.Command("vde_switch", "--sock", dir, "--dirmode", "0700", "--nostdin")
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start vde_switch: %w", err)
	}
	s := &Switch{Dir: dir, cmd: cmd, exited: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(s.exited)
	}()
	ctl := filepath.Join(dir, "ctl")
	for {
		if _, err := os.Stat(ctl); err == nil {
			return s, nil
		}
		select {
		case <-ctx.Done():
			s.Stop()
			return nil, fmt.Errorf("waiting for %s: %w", ctl, ctx.Err())
		case <-s.exited:
			return nil, fmt.Errorf("vde_switch exited before creating %s", ctl)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Stop kills the switch and waits for it to exit.
func (s *Switch) Stop() {
	s.stopOnce.Do(func() {
		s.cmd.Process.Kill()
		<-s.exited
	})
}
