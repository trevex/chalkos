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

// Temporary returns the path in the ESP mounted at dir that a UKI booting the store with the root
// hash is written to before it gets its name, and creates its directory.
func Temporary(dir string, rootHash []byte) (string, error) {
	linux := filepath.Join(dir, LinuxDir)
	if err := os.MkdirAll(linux, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(linux, tempPrefix+hex.EncodeToString(rootHash)+".efi"), nil
}

// stagedPrefix starts the name of the UKI of an image staged in the inactive slot, which waits
// for its activation. systemd-boot ignores it as it does a temporary UKI, and Leftovers leaves it:
// a staged image stays staged across a restart of chalkd and a reboot.
const stagedPrefix = ".staged-"

// StagedName is the file name in /EFI/Linux of the UKI of the image staged in the inactive slot:
// .staged-<id>_<generation>.efi. The generation is the one its activation names the entry at
// least, chosen when the image is staged, before the retirement removes the UKIs of the image
// that named higher ones: a new entry never takes the ID of an entry the node booted before.
func StagedName(imageID string, generation uint64) string {
	return stagedPrefix + EntryName(imageID, generation)
}

// Staged reads the UKI of the image staged in the ESP mounted at dir, as StagedName names it, into
// an entry without an ID or a tries counter, with the generation its name gives. false without
// one; a file of that prefix whose name gives no generation of the image, or that is no UKI of the
// image, is none, which the next upgrade's retirement removes. Of two, the higher generation
// counts, as a retirement leaves none.
func Staged(dir, imageID string) (Entry, bool, error) {
	names, err := StagedFiles(dir)
	if err != nil {
		return Entry{}, false, err
	}
	var staged Entry
	for _, name := range names {
		g := EntryGeneration(strings.ToLower(strings.TrimPrefix(name, stagedPrefix)), imageID)
		if g == 0 || g < staged.Generation {
			continue
		}
		img, readable, err := readUKI(filepath.Join(dir, LinuxDir, name), os.Open)
		if err != nil {
			return Entry{}, false, fmt.Errorf("read %s on the ESP: %w", name, err)
		}
		root, herr := img.UsrHash()
		if !readable || img.ID() != imageID || herr != nil {
			continue
		}
		staged = Entry{File: name, TriesLeft: -1, TriesDone: -1, Readable: true, ImageID: imageID, RootHash: root, Generation: g}
	}
	return staged, staged.File != "", nil
}

// StagedFiles lists the files in the ESP mounted at dir whose names a staged UKI has, of any image
// and whether they are UKIs or not.
func StagedFiles(dir string) ([]string, error) {
	files, err := os.ReadDir(filepath.Join(dir, LinuxDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list the UKIs on the ESP: %w", err)
	}
	var staged []string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), stagedPrefix) && !f.IsDir() {
			staged = append(staged, f.Name())
		}
	}
	return staged, nil
}

// Leftovers lists the UKIs in the ESP mounted at dir that were written under a Temporary name and
// never named, such as by an install or upgrade that stopped, except the one at the path keep. The
// UKI of an image staged in the inactive slot, under its StagedName, is none of them.
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

// Name gives the UKI at the path from, under a Temporary or a StagedName, its name in the ESP
// mounted at dir, calling change first when it is set, and syncs the ESP.
func Name(dir, from, name string, change func(what string) error) error {
	if err := call(change, "name the UKI "+name); err != nil {
		return err
	}
	if err := os.Rename(from, filepath.Join(dir, LinuxDir, name)); err != nil {
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
