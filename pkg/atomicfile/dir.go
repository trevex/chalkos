package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// CheckNew refuses an output directory that exists, or whose parent directory does not.
func CheckNew(out string) error {
	if _, err := os.Lstat(out); err == nil {
		return fmt.Errorf("%s exists; the output is a new directory", out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(out)
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is no directory", parent)
	}
	return nil
}

// NewDir writes the new directory out: fill writes a temporary directory next to it, which is
// renamed to out once fill succeeds and removed on any failure, including out appearing meanwhile.
// out must not exist and its parent directory must.
func NewDir(out string, fill func(tmp string) error) (err error) {
	if err := CheckNew(out); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(out), "."+filepath.Base(out)+"-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()
	if err := fill(tmp); err != nil {
		return err
	}
	return renameNew(tmp, out)
}

// renameNew renames the directory tmp to out and fails if out exists by now. It renames with
// RENAME_NOREPLACE, so out cannot appear between the check and the rename; on a file system
// without RENAME_NOREPLACE it checks out just before renaming.
func renameNew(tmp, out string) error {
	err := unix.Renameat2(unix.AT_FDCWD, tmp, unix.AT_FDCWD, out, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		if _, err := os.Lstat(out); err == nil {
			return fmt.Errorf("%s appeared while it was being written", out)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return os.Rename(tmp, out)
	}
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s appeared while it was being written", out)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: tmp, New: out, Err: err}
	}
	return nil
}
