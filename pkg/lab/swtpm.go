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

// SWTPM is a software TPM 2.0 emulator whose state lives in a directory, so it survives restarts.
type SWTPM struct {
	// Socket is the control socket QEMU's tpm-emulator backend connects to.
	Socket string

	cmd      *exec.Cmd
	exited   chan struct{}
	stopOnce sync.Once
}

// StartSWTPM starts swtpm with its state in stateDir and waits for the control socket.
func StartSWTPM(ctx context.Context, stateDir string) (*SWTPM, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	sock := filepath.Join(stateDir, "swtpm.sock")
	logPath := filepath.Join(stateDir, "swtpm.log")
	os.Remove(sock)

	cmd := exec.Command("swtpm", "socket", "--tpm2",
		"--tpmstate", "dir="+stateDir,
		"--ctrl", "type=unixio,path="+sock,
		"--log", "file="+logPath+",level=5")
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start swtpm: %w", err)
	}
	s := &SWTPM{Socket: sock, cmd: cmd, exited: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(s.exited)
	}()

	for {
		if _, err := os.Stat(sock); err == nil {
			return s, nil
		}
		select {
		case <-ctx.Done():
			s.Stop()
			return nil, fmt.Errorf("waiting for swtpm socket %s: %w", sock, ctx.Err())
		case <-s.exited:
			return nil, fmt.Errorf("swtpm exited before creating %s; see %s", sock, logPath)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Stop kills swtpm and waits for it to exit. The state directory is kept.
func (s *SWTPM) Stop() {
	s.stopOnce.Do(func() {
		s.cmd.Process.Kill()
		<-s.exited
	})
}
