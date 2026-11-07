package atomicfile

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("another process holds the lock")

// Lock opens the file at path, creating it with perm, and takes an exclusive lock on it, held
// until the file is closed or the process ends; a crash leaves no lock behind. With wait unset it
// fails with ErrLocked while another process holds the lock. It does not follow a symbolic link
// at path.
func Lock(path string, perm fs.FileMode, wait bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, perm)
	if err != nil {
		return nil, err
	}
	if err := Flock(f, wait); err != nil {
		f.Close()
		if err != ErrLocked {
			err = &fs.PathError{Op: "flock", Path: path, Err: err}
		}
		return nil, err
	}
	return f, nil
}

// Flock takes an exclusive lock on the open file f as Lock does.
func Flock(f *os.File, wait bool) error {
	how := unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}
	err := unix.Flock(int(f.Fd()), how)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}
