package lab

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// daemon is a helper process this process started, such as swtpm or the switch.
type daemon struct {
	cmd      *exec.Cmd
	exited   chan struct{}
	stopOnce sync.Once
}

// startDaemon starts cmd and waits until it creates the file ready. hint ends the error when the
// process exits first, such as where its log is.
func startDaemon(ctx context.Context, cmd *exec.Cmd, ready, hint string) (*daemon, error) {
	name := cmd.Args[0]
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	d := &daemon{cmd: cmd, exited: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(d.exited)
	}()
	for {
		if _, err := os.Stat(ready); err == nil {
			return d, nil
		}
		select {
		case <-ctx.Done():
			d.Stop()
			return nil, fmt.Errorf("waiting for %s to create %s: %w", name, ready, ctx.Err())
		case <-d.exited:
			return nil, fmt.Errorf("%s exited before creating %s%s", name, ready, hint)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Stop kills the process and waits for it to exit.
func (d *daemon) Stop() {
	d.stopOnce.Do(func() {
		d.cmd.Process.Kill()
		<-d.exited
	})
}

// stopDaemonIn kills the process whose PID is in pidFile and whose command line names dir, as
// another process started it.
func stopDaemonIn(pidFile, dir string) {
	if pid, ok := runningPID(pidFile, dir); ok {
		syscall.Kill(pid, syscall.SIGKILL)
		stopProcess(pid, dir, 10*time.Second)
	}
}
