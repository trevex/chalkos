package esp

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// tempPrefix starts the name a UKI is written under before it gets its own. systemd-boot
// ignores files whose names start with a dot.
const tempPrefix = ".chalkos-"

// Stage returns the path in the ESP mounted at dir that a UKI booting the store with the root
// hash is written to before it gets its name, and creates its directory.
func Stage(dir string, rootHash []byte) (string, error) {
	linux := filepath.Join(dir, LinuxDir)
	if err := os.MkdirAll(linux, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(linux, tempPrefix+hex.EncodeToString(rootHash)+".efi"), nil
}

// Leftovers lists the UKIs in the ESP mounted at dir that were staged and never named, such as
// by an install or upgrade that stopped, except the one at the path keep.
func Leftovers(dir, keep string) ([]string, error) {
	files, err := os.ReadDir(filepath.Join(dir, LinuxDir))
	if err != nil {
		return nil, fmt.Errorf("list the UKIs on the ESP: %w", err)
	}
	var left []string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), tempPrefix) && f.Name() != filepath.Base(keep) {
			left = append(left, f.Name())
		}
	}
	return left, nil
}

// Remove removes the named UKIs from the ESP mounted at dir, calling change before each when it
// is set, and syncs the ESP once it removed any.
func Remove(dir string, files []string, change func(what string) error) error {
	for _, name := range files {
		if err := call(change, "remove "+name); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dir, LinuxDir, name)); err != nil {
			return fmt.Errorf("remove %s from the ESP: %w", name, err)
		}
	}
	if len(files) == 0 {
		return nil
	}
	return Sync(dir)
}

// Name gives the UKI staged at the path staged its name in the ESP mounted at dir, calling change
// first when it is set, and syncs the ESP.
func Name(dir, staged, name string, change func(what string) error) error {
	if err := call(change, "name the UKI "+name); err != nil {
		return err
	}
	if err := os.Rename(staged, filepath.Join(dir, LinuxDir, name)); err != nil {
		return fmt.Errorf("name the UKI %s: %w", name, err)
	}
	return Sync(dir)
}

// Sync flushes the ESP mounted at dir, the directory of its UKIs first: the boot loader reads
// the ESP without the kernel's caches.
func Sync(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	linux, err := os.Open(filepath.Join(dir, LinuxDir))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		err := linux.Sync()
		linux.Close()
		if err != nil {
			return fmt.Errorf("sync the ESP: %w", err)
		}
	}
	if err := unix.Syncfs(int(d.Fd())); err != nil {
		return fmt.Errorf("sync the ESP: %w", err)
	}
	return nil
}

func call(change func(string) error, what string) error {
	if change == nil {
		return nil
	}
	return change(what)
}
