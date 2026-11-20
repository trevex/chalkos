// Package fatfs formats, reads and writes a FAT file system that starts at an offset in a file,
// such as the ESP of a raw disk image, without mounting it or running a tool.
package fatfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/soypat/fat"
)

const sectorSize = 512

// region is the block device of the sectors of a file from offset on.
type region struct {
	f      *os.File
	offset int64
}

func (r region) ReadBlocks(dst []byte, block int64) (int, error) {
	return r.f.ReadAt(dst, r.offset+block*sectorSize)
}

func (r region) WriteBlocks(data []byte, block int64) (int, error) {
	return r.f.WriteAt(data, r.offset+block*sectorSize)
}

// EraseBlocks zeroes the blocks: the file system clears a new directory's cluster with it, and a
// cluster left as it was would hold the bytes of whatever the region held before.
func (r region) EraseBlocks(block, count int64) error {
	_, err := r.f.WriteAt(make([]byte, count*sectorSize), r.offset+block*sectorSize)
	return err
}

// Format formats the size bytes of the file name from offset on as FAT32 with the label and the
// volume ID, or as FAT16 or FAT12 when FAT32 cannot describe so few sectors, as for a test's small
// ESP. The same arguments always write the same bytes.
func Format(name string, offset, size int64, label string, volumeID uint32) error {
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	r := region{f, offset}
	var formatter fat.Formatter
	sectors := int(size / sectorSize)
	if err := formatter.Format(r, sectorSize, sectors, fat.FormatParams{Format: fat.FormatFAT32}); err != nil {
		if err := formatter.Format(r, sectorSize, sectors, fat.FormatParams{Format: fat.FormatFAT16}); err != nil {
			return fmt.Errorf("format the FAT file system at %d in %s: %w", offset, name, err)
		}
	}
	if err := setVolume(r, label, volumeID); err != nil {
		return fmt.Errorf("label the FAT file system at %d in %s: %w", offset, name, err)
	}
	return f.Close()
}

// setVolume writes the label and the volume ID into the boot sector, and on FAT32 into its backup
// too, which the formatter leaves at "NO NAME" and an ID of its own, and the label as the first
// entry of the empty root directory, where fsck.vfat expects it as well.
func setVolume(r region, label string, volumeID uint32) error {
	boot := make([]byte, sectorSize)
	if _, err := r.ReadBlocks(boot, 0); err != nil {
		return err
	}
	name := fmt.Sprintf("%-11.11s", strings.ToUpper(label))
	reserved, fats := int64(binary.LittleEndian.Uint16(boot[0x0e:])), int64(boot[0x10])
	// The extended boot record follows the FAT32 BPB at 0x40, the FAT12 and FAT16 BPB at 0x24. A
	// FAT32 boot sector gives no 16-bit FAT size, and its root directory is the data area's first
	// cluster, where the FAT12 and FAT16 root directory comes before the data area.
	ebr, sectors := 0x24, []int64{0}
	root := reserved + fats*int64(binary.LittleEndian.Uint16(boot[0x16:]))
	if binary.LittleEndian.Uint16(boot[0x16:]) == 0 {
		ebr, sectors = 0x40, []int64{0, int64(binary.LittleEndian.Uint16(boot[0x32:]))}
		root = reserved + fats*int64(binary.LittleEndian.Uint32(boot[0x24:]))
	}
	binary.LittleEndian.PutUint32(boot[ebr+3:], volumeID)
	copy(boot[ebr+7:ebr+18], name)
	for _, s := range sectors {
		if _, err := r.WriteBlocks(boot, s); err != nil {
			return err
		}
	}
	entry := make([]byte, sectorSize)
	copy(entry, name)
	entry[11] = 0x08 // The volume label attribute.
	_, err := r.WriteBlocks(entry, root)
	return err
}

// FS is a FAT file system Open opened. Its paths are absolute and their names case-insensitive,
// as FAT's are: /EFI/Linux/chalkos_1.efi.
type FS struct {
	f   *os.File
	fat fat.FS
}

// Open opens the FAT file system at offset in the file name, for writing when writable is set.
// The caller closes it.
func Open(name string, offset int64, writable bool) (*FS, error) {
	flag, mode := os.O_RDONLY, fat.ModeRead
	if writable {
		flag, mode = os.O_RDWR, fat.ModeRW
	}
	f, err := os.OpenFile(name, flag, 0)
	if err != nil {
		return nil, err
	}
	fsys := &FS{f: f}
	if err := fsys.fat.Mount(region{f, offset}, sectorSize, mode); err != nil {
		f.Close()
		return nil, fmt.Errorf("the FAT file system at %d in %s: %w", offset, name, err)
	}
	return fsys, nil
}

// Close closes the file system and its file.
func (fsys *FS) Close() error {
	return errors.Join(fsys.fat.Unmount(), fsys.f.Close())
}

// MkdirAll makes the directory dir and the directories above it that do not exist.
func (fsys *FS) MkdirAll(dir string) error {
	if dir == "/" {
		return nil
	}
	if err := fsys.MkdirAll(path.Dir(dir)); err != nil {
		return err
	}
	var info fat.FileInfo
	if fsys.fat.Stat(dir, &info) == nil {
		if !info.IsDir() {
			return fmt.Errorf("make the directory %s: a file has its name", dir)
		}
		return nil
	}
	if err := fsys.fat.Mkdir(dir); err != nil {
		return fmt.Errorf("make the directory %s: %w", dir, err)
	}
	return nil
}

// ReadFile returns the content of the file name.
func (fsys *FS) ReadFile(name string) ([]byte, error) {
	var f fat.File
	if err := fsys.fat.OpenFile(&f, name, fat.ModeRead); err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	data, err := io.ReadAll(&f)
	if err := errors.Join(err, f.Close()); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return data, nil
}

// WriteFile writes data to the file name, replacing its content if it exists. Its directory must
// exist.
func (fsys *FS) WriteFile(name string, data []byte) error {
	var f fat.File
	if err := fsys.fat.OpenFile(&f, name, fat.ModeCreateAlways|fat.ModeWrite); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	_, err := f.Write(data)
	if err := errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// List returns the paths of the files below the directory dir, in its subdirectories too: those
// of a directory's files before those of its subdirectories, each in the order the directory
// lists them.
func (fsys *FS) List(dir string) ([]string, error) {
	var d fat.Dir
	if err := fsys.fat.OpenDir(&d, dir); err != nil {
		return nil, fmt.Errorf("open the directory %s: %w", dir, err)
	}
	var files, dirs []string
	err := d.ForEachFile(func(info *fat.FileInfo) error {
		if p := path.Join(dir, info.Name()); info.IsDir() {
			dirs = append(dirs, p)
		} else {
			files = append(files, p)
		}
		return nil
	})
	if err := errors.Join(err, d.Close()); err != nil {
		return nil, fmt.Errorf("list the directory %s: %w", dir, err)
	}
	for _, sub := range dirs {
		below, err := fsys.List(sub)
		if err != nil {
			return nil, err
		}
		files = append(files, below...)
	}
	return files, nil
}
