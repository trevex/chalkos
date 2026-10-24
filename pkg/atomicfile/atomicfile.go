// Package atomicfile replaces files in one step, so a crash leaves the old content or the new,
// never part of either.
package atomicfile

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile replaces path with data and perm, creating its directory, and syncs the file and the
// directory, so the new content survives a crash once WriteFile returns.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	if err := Replace(path, data, perm); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// Replace replaces path with data and perm, creating its directory, and syncs the file but not
// the directory. It suits callers that write several files and sync their directories once.
func Replace(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := Stage(path, data, perm)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Stage writes data with perm to a synced temporary file in path's directory and returns its
// name, for the caller to rename over path or remove. On an error no temporary file is left.
func Stage(path string, data []byte, perm fs.FileMode) (name string, err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	err = f.Chmod(perm)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return f.Name(), nil
}

// WriteNew writes data with perm to a new file at path, which must not exist yet, and syncs it.
// A file it cannot write completely is removed again, so no partial file is left behind.
func WriteNew(path string, data []byte, perm fs.FileMode) error {
	return createNew(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// createNew creates a file that must not exist yet with what write writes, and removes it again
// when it cannot be written completely.
func createNew(path string, perm fs.FileMode, write func(w io.Writer) error) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// SyncDir syncs a directory, so the names of the files renamed into it last survive a crash.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
