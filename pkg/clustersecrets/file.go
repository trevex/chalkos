package clustersecrets

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/atomicfile"
)

var (
	// ErrLocked is the error of Open while another command holds the secrets file's lock.
	ErrLocked = errors.New("another command is changing the secrets file")
	// ErrChanged is the error of File.Replace when the file changed since it was read or last
	// replaced.
	ErrChanged = errors.New("the secrets file changed since it was read")
)

// File is a secrets file a command changes in place. It holds the file's lock until Close, and
// the SHA-256 of what the file held when it was read or last replaced.
type File struct {
	path string
	lock *os.File
	sum  [sha256.Size]byte
}

// Open locks the secrets file at path and reads it. The lock is taken before the file is read: a
// second command would read a rotation's state while the first one moves it on.
func Open(path string) (*File, []byte, error) {
	lock, err := lockFile(path)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("read the secrets file: %w", err)
	}
	return &File{path: path, lock: lock, sum: sha256.Sum256(data)}, data, nil
}

// Close releases the file's lock.
func (f *File) Close() error {
	if f.lock == nil {
		return nil
	}
	err := f.lock.Close()
	f.lock = nil
	return err
}

// lockFile takes an exclusive lock on <file>.lock beside the file a link points to, held until
// the returned file is closed or the process ends. The lock file is never removed: a command that
// removed it could leave the next two to lock different files.
func lockFile(path string) (*os.File, error) {
	name := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		name = real
	}
	name += ".lock"
	lock, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock the secrets file: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock the secrets file with %s: %w", name, err)
	}
	return lock, nil
}

// Replace replaces the file with data as the package function Replace does. It refuses with
// ErrChanged a file that changed since it was read or last replaced: the lock keeps other
// commands out, but not an editor or a checkout, whose change would be lost.
func (f *File) Replace(data []byte) error {
	current, err := os.ReadFile(f.path)
	if err != nil {
		return fmt.Errorf("read the secrets file again before replacing it: %w", err)
	}
	if sha256.Sum256(current) != f.sum {
		return ErrChanged
	}
	err = Replace(f.path, data, 0o600)
	var unsynced *UnsyncedError
	if err == nil || errors.As(err, &unsynced) {
		f.sum = sha256.Sum256(data)
	}
	return err
}

// Create writes data with perm to a new file at path and syncs its directory. A directory that
// cannot be synced once the file is written is an *UnsyncedError.
func Create(path string, data []byte, perm fs.FileMode) error {
	if err := atomicfile.WriteNew(path, data, perm); err != nil {
		return err
	}
	if err := SyncDir(filepath.Dir(path)); err != nil {
		return &UnsyncedError{path, err}
	}
	return nil
}

// Replace replaces path with data in one step and keeps what it held as PreviousOf(path).
//   - The data goes to a temporary file beside path and is synced. path.prev becomes a link to
//     the current file, and the temporary file is renamed over path.
//   - path exists throughout, and a crash leaves one version or the other, never part of one.
//   - A file that does not exist yet is created with perm; an existing one keeps its mode.
//   - A symbolic link is followed: the file it points to is replaced and the link kept.
//
// A directory that cannot be synced after the rename is an *UnsyncedError.
func Replace(path string, data []byte, perm fs.FileMode) error {
	switch real, err := filepath.EvalSymlinks(path); {
	case err == nil:
		path = real
	case !errors.Is(err, fs.ErrNotExist):
		return err
	default:
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%s is a link to a file that does not exist", path)
		}
	}
	info, err := os.Stat(path)
	exists := err == nil
	switch {
	case exists:
		perm = info.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	tmp, err := atomicfile.Stage(path, data, perm)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp)
	if exists {
		if err := keepPrevious(path, perm); err != nil {
			return fmt.Errorf("keep the previous version of %s: %w", path, err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := SyncDir(filepath.Dir(path)); err != nil {
		return &UnsyncedError{path, err}
	}
	return nil
}

// PreviousOf is where Replace keeps the previous version of path: beside the file a link points
// to.
func PreviousOf(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return path + ".prev"
}

// keepPrevious makes path.prev what path holds now, replacing an older path.prev in one step: a
// hard link where the file system has them, a synced copy otherwise.
func keepPrevious(path string, perm fs.FileMode) error {
	next := path + ".prev.new"
	os.Remove(next)
	if err := os.Link(path, next); err != nil {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if err := atomicfile.WriteNew(next, data, perm); err != nil {
			return err
		}
	}
	return os.Rename(next, path+".prev")
}

// UnsyncedError is a file written in place whose directory could not be synced, so a crash may
// still bring back what the directory held before.
type UnsyncedError struct {
	Path string
	Err  error
}

func (e *UnsyncedError) Error() string {
	return fmt.Sprintf("%s is written, but its directory was not synced, so a crash may undo that: %v", e.Path, e.Err)
}

func (e *UnsyncedError) Unwrap() error { return e.Err }

// SyncDir syncs a directory after a rename into it. Tests, also those of commands that write
// secrets files, replace it to make the sync fail.
var SyncDir = atomicfile.SyncDir
