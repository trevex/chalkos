package lab

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// swtpm is a software TPM 2.0 emulator whose state lives in a directory, so it survives restarts.
type swtpm struct {
	*daemon
	// socket is the control socket QEMU's tpm-emulator backend connects to.
	socket string
}

// startSWTPM starts swtpm with its state in stateDir and waits for the control socket.
func startSWTPM(ctx context.Context, stateDir string) (*swtpm, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	sock := filepath.Join(stateDir, "swtpm.sock")
	logPath := filepath.Join(stateDir, "swtpm.log")
	os.Remove(sock)

	cmd := exec.Command("swtpm", "socket", "--tpm2",
		"--tpmstate", "dir="+stateDir,
		"--ctrl", "type=unixio,path="+sock,
		"--pid", "file="+filepath.Join(stateDir, "swtpm.pid"),
		"--log", "file="+logPath+",level=5")
	d, err := startDaemon(ctx, cmd, sock, "; see "+logPath)
	if err != nil {
		return nil, err
	}
	return &swtpm{daemon: d, socket: sock}, nil
}

// stopSWTPMIn kills the swtpm whose state is in stateDir, as another process started it. The
// state directory is kept.
func stopSWTPMIn(stateDir string) {
	stopDaemonIn(filepath.Join(stateDir, "swtpm.pid"), stateDir)
}
