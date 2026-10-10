package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
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

// startSignal asks a supervisor to start the VMs of its lab that do not run.
const startSignal = syscall.SIGUSR1

// Supervise runs the lab of the state directory dir until ctx ends: the switch of the lab
// network, then each node's VM with its TPM. Once they all run it marks the lab ready; when it
// ends, or starting failed, it stops them. A VM that exits, as when its guest powers off, leaves
// the others running: the supervisor logs it to log and the lab is no longer ready until
// StartStopped starts the VM again. It holds the lab's lock meanwhile, so one supervisor runs a
// lab.
func Supervise(ctx context.Context, dir string, log io.Writer) error {
	// Before the PID is written, so a start that reads it may signal this process.
	start := make(chan os.Signal, 1)
	signal.Notify(start, startSignal)
	defer signal.Stop(start)

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
	vms := map[string]*VM{}
	defer func() {
		var wg sync.WaitGroup
		for _, vm := range vms {
			wg.Go(func() { vm.Stop() })
		}
		wg.Wait()
	}()
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	exited := make(chan *VM)
	startVM := func(n LabNode) error {
		vm, err := StartVM(ctx, l.VMConfig(dir, n))
		if err != nil {
			return fmt.Errorf("start the VM of %s: %w", n.Name, err)
		}
		// The supervisor only keeps the VM running: others read its console and, as QEMU serves one
		// QMP client at a time, attach to it; it stops the VM with SIGTERM.
		vm.follower.Close()
		vm.QMP.Close()
		vm.QMP = nil
		vms[n.Name] = vm
		go func() {
			select {
			case <-vm.exited:
				select {
				case exited <- vm:
				case <-watchCtx.Done():
				}
			case <-watchCtx.Done():
			}
		}()
		return nil
	}
	// reap lets go of a VM that exited: its TPM stops, and the lab is not ready without it.
	reap := func(vm *VM) {
		if vms[vm.Config.Name] != vm {
			return
		}
		vm.Stop()
		delete(vms, vm.Config.Name)
		// Logged first, so whoever finds the lab not ready finds the reason in the log.
		fmt.Fprintf(log, "%s the VM of %s exited; chalklab start starts it again\n", time.Now().Format(time.RFC3339), vm.Config.Name)
		os.Remove(ready)
	}
	for _, n := range l.Nodes {
		if err := startVM(n); err != nil {
			return err
		}
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case vm := <-exited:
			reap(vm)
		case <-start:
			// A VM may have exited before this process heard of it.
			for _, vm := range vms {
				select {
				case <-vm.exited:
					reap(vm)
				default:
				}
			}
			for _, n := range l.Nodes {
				if vms[n.Name] != nil {
					continue
				}
				if err := startVM(n); err != nil {
					fmt.Fprintf(log, "%s %v\n", time.Now().Format(time.RFC3339), err)
					continue
				}
				fmt.Fprintf(log, "%s started the VM of %s again\n", time.Now().Format(time.RFC3339), n.Name)
			}
			if len(vms) == len(l.Nodes) {
				if err := os.WriteFile(ready, nil, 0o600); err != nil {
					return err
				}
			}
		}
	}
}

// StartStopped asks the supervisor that runs the lab in dir to start the VMs of the lab that do
// not run, and waits until every VM runs. It fails when no supervisor runs the lab, or it ends.
func StartStopped(ctx context.Context, dir string) error {
	l, err := ReadLab(dir)
	if err != nil {
		return err
	}
	pid, running, err := Supervisor(dir)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("no supervisor runs the lab in %s", dir)
	}
	if err := syscall.Kill(pid, startSignal); err != nil {
		return fmt.Errorf("signal the supervisor %d: %w", pid, err)
	}
	for {
		// The lab may be ready still while its supervisor has not yet heard that a VM exited.
		if Ready(dir) && allRunning(dir, l) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the supervisor %d to start the VMs: %w; see %s", pid, ctx.Err(), filepath.Join(dir, "supervisor.log"))
		case <-time.After(100 * time.Millisecond):
		}
		if _, running, err := Supervisor(dir); err != nil {
			return err
		} else if !running {
			return fmt.Errorf("the supervisor %d ended; see %s", pid, filepath.Join(dir, "supervisor.log"))
		}
	}
}

func allRunning(dir string, l *Lab) bool {
	for _, n := range l.Nodes {
		if !Running(l.VMConfig(dir, n)) {
			return false
		}
	}
	return true
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
	// A supervisor that just took the lock writes its PID right after.
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(f.Name())
		if err != nil {
			return 0, false, err
		}
		if s := strings.TrimSpace(string(data)); s != "" || time.Now().After(deadline) {
			pid, err := strconv.Atoi(s)
			if err != nil {
				return 0, false, fmt.Errorf("%s holds no PID: %w", f.Name(), err)
			}
			return pid, true, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PrepareStart readies a lab that no supervisor runs to start again from its state, as after a
// host's reboot or a supervisor that was killed: it stops what runs of the lab still. It refuses a
// lab a supervisor runs; StartStopped starts the VMs of that one that stopped.
func PrepareStart(ctx context.Context, dir string) error {
	if pid, running, err := Supervisor(dir); err != nil {
		return err
	} else if running {
		return fmt.Errorf("the supervisor %d runs the lab in %s", pid, dir)
	}
	return StopLab(ctx, dir)
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
