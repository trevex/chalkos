package lab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// supervisorFile holds the PID of the lab's supervisor, which keeps it locked while it runs.
	supervisorFile = "supervisor.pid"
	// readyFile exists while every VM of the lab runs.
	readyFile = "supervisor.ready"
)

// Supervise runs the lab of the state directory dir until ctx ends: the switch of the lab network,
// then each node's VM with its TPM. Once they all run it marks the lab ready; when ctx ends, or
// starting failed, it stops them. It holds the lab's lock meanwhile, so one supervisor runs a lab.
func Supervise(ctx context.Context, dir string) error {
	lock, err := os.OpenFile(filepath.Join(dir, supervisorFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("a supervisor runs the lab in %s already: %w", dir, err)
	}
	if err := lock.Truncate(0); err != nil {
		return err
	}
	if _, err := lock.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		return err
	}
	ready := filepath.Join(dir, readyFile)
	os.Remove(ready)
	defer os.Remove(ready)

	l, err := ReadLab(dir)
	if err != nil {
		return err
	}
	sw, err := StartSwitch(ctx, filepath.Join(dir, "switch"))
	if err != nil {
		return err
	}
	defer sw.Stop()
	var vms []*VM
	defer func() {
		var wg sync.WaitGroup
		for _, vm := range vms {
			wg.Go(func() { vm.Stop() })
		}
		wg.Wait()
	}()
	for _, n := range l.Nodes {
		vm, err := StartVM(ctx, l.VMConfig(dir, n))
		if err != nil {
			return fmt.Errorf("start the VM of %s: %w", n.Name, err)
		}
		// The supervisor only keeps the VM running: others read its console and, as QEMU serves one
		// QMP client at a time, attach to it; it stops the VM with SIGTERM.
		vm.follower.Close()
		vm.QMP.Close()
		vm.QMP = nil
		vms = append(vms, vm)
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// Ready reports whether the lab's supervisor runs every VM of the lab.
func Ready(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, readyFile))
	return err == nil
}

// Supervisor returns the PID of the supervisor that runs the lab in dir, and whether one does.
func Supervisor(dir string) (int, bool, error) {
	f, err := os.Open(filepath.Join(dir, supervisorFile))
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); {
	case err == nil:
		return 0, false, nil
	case !errors.Is(err, syscall.EWOULDBLOCK):
		return 0, false, err
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		return 0, false, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false, fmt.Errorf("%s holds no PID: %w", f.Name(), err)
	}
	return pid, true, nil
}

// StopLab stops the lab in dir: its supervisor stops the VMs and the switch, and what runs of the
// lab without a supervisor, as after it was killed, is stopped too. It fails when anything of the
// lab still runs.
func StopLab(ctx context.Context, dir string) error {
	pid, running, err := Supervisor(dir)
	if err != nil {
		return err
	}
	if running {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("stop the supervisor: %w", err)
		}
		for running {
			select {
			case <-ctx.Done():
				return fmt.Errorf("waiting for the supervisor %d to stop the lab: %w", pid, ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
			if _, running, err = Supervisor(dir); err != nil {
				return err
			}
		}
	}
	l, err := ReadLab(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var left []string
	for _, n := range l.Nodes {
		c := l.VMConfig(dir, n)
		if pid, ok := runningPID(c.pidPath(), c.Dir); ok {
			syscall.Kill(pid, syscall.SIGTERM)
			stopProcess(pid, c.Dir, 10*time.Second)
		}
		stopSWTPMIn(c.tpmDir())
		if Running(c) {
			left = append(left, n.Name)
		}
	}
	stopSwitchIn(filepath.Join(dir, "switch"))
	if len(left) > 0 {
		return fmt.Errorf("the VMs of %s still run", strings.Join(left, ", "))
	}
	return nil
}
