package chalkd

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
)

// ConsoleSocket is the console's socket in chalkd's run directory.
const ConsoleSocket = "console.sock"

// ListenConsole listens on the console's socket in dir, chalkd's run directory, which it creates
// or narrows to root alone; the socket admits root alone too. A socket a chalkd before left
// behind is replaced.
func ListenConsole(dir string) (net.Listener, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	// chalkd runs as root and gives the directory to root; tests, which do not, keep their own.
	if os.Geteuid() == 0 {
		if err := os.Lchown(dir, 0, 0); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(dir, ConsoleSocket)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
