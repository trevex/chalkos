// Package storagetest builds the sysfs, udev database and /dev trees a storage.Host reads, for
// tests.
package storagetest

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/trevex/chalkos/pkg/storage"
)

// Disk is a whole disk of a test host. It reports itself as rotational.
type Disk struct {
	Name, Devnum string
	// Size is the disk's size in bytes; 0 means 16 GiB.
	Size uint64
	// Props are the disk's udev properties, such as ID_PATH and ID_SERIAL.
	Props map[string]string
}

// NewHost builds the trees of the disks below a temporary directory and links
// /dev/disk/chalk-boot-disk to the disk named boot.
func NewHost(t testing.TB, boot string, disks ...Disk) storage.Host {
	t.Helper()
	root := t.TempDir()
	h := storage.Host{
		SysRoot:  filepath.Join(root, "sys"),
		UdevRoot: filepath.Join(root, "udev"),
		DevRoot:  filepath.Join(root, "dev"),
	}
	for _, d := range disks {
		size := d.Size
		if size == 0 {
			size = 16 << 30
		}
		dir := filepath.Join(h.SysRoot, "block", d.Name)
		write(t, filepath.Join(dir, "dev"), d.Devnum+"\n")
		write(t, filepath.Join(dir, "size"), strconv.FormatUint(size/512, 10)+"\n")
		write(t, filepath.Join(dir, "queue", "rotational"), "1\n")
		var props string
		for k, v := range d.Props {
			props += "E:" + k + "=" + v + "\n"
		}
		write(t, filepath.Join(h.UdevRoot, "b"+d.Devnum), props)
		write(t, filepath.Join(h.DevRoot, d.Name), "")
	}
	if err := os.MkdirAll(filepath.Join(h.DevRoot, "disk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../"+boot, filepath.Join(h.DevRoot, "disk", "chalk-boot-disk")); err != nil {
		t.Fatal(err)
	}
	return h
}

// write writes a file, making its directory.
func write(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
