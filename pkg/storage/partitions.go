package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Partition is one partition of a disk, as sysfs and the udev database describe it.
type Partition struct {
	// Name is the kernel name, such as vda3 or nvme0n1p3.
	Name   string
	Device string
	Number int
	Size   uint64
	// Type, Label and UUID are the GPT entry's; Content is what blkid found, such as ext4.
	Type    string
	Label   string
	UUID    string
	Content string
}

// Partitions lists a disk's partitions in partition number order.
func (h Host) Partitions(disk BlockDisk) ([]Partition, error) {
	dir := filepath.Join(h.SysRoot, "block", disk.Name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var parts []Partition
	for _, e := range entries {
		number, err := readTrimmed(filepath.Join(dir, e.Name(), "partition"))
		if err != nil {
			// Only partitions have this file.
			continue
		}
		n, err := strconv.Atoi(number)
		if err != nil {
			return nil, fmt.Errorf("partition %s: %w", e.Name(), err)
		}
		p, err := h.partition(filepath.Join(dir, e.Name()), e.Name())
		if err != nil {
			return nil, err
		}
		p.Number = n
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	return parts, nil
}

func (h Host) partition(dir, name string) (Partition, error) {
	devnum, err := readTrimmed(filepath.Join(dir, "dev"))
	if err != nil {
		return Partition{}, err
	}
	sectors, err := readTrimmed(filepath.Join(dir, "size"))
	if err != nil {
		return Partition{}, err
	}
	n, err := strconv.ParseUint(sectors, 10, 64)
	if err != nil {
		return Partition{}, fmt.Errorf("partition %s size: %w", name, err)
	}
	props, err := readUdevProperties(filepath.Join(h.UdevRoot, "b"+devnum))
	if err != nil {
		return Partition{}, err
	}
	return Partition{
		Name:    name,
		Device:  "/dev/" + name,
		Size:    n * 512,
		Type:    strings.ToLower(props["ID_PART_ENTRY_TYPE"]),
		Label:   props["ID_PART_ENTRY_NAME"],
		UUID:    strings.ToLower(props["ID_PART_ENTRY_UUID"]),
		Content: props["ID_FS_TYPE"],
	}, nil
}

// PartitionOf returns the disk a partition device, or a link to one, belongs to and the
// partition's number.
func (h Host) PartitionOf(dev string) (disk string, number int, err error) {
	rel, ok := strings.CutPrefix(dev, "/dev/")
	if !ok {
		return "", 0, fmt.Errorf("%s is not below /dev", dev)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(h.DevRoot, rel))
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", dev, err)
	}
	name := filepath.Base(target)
	matches, err := filepath.Glob(filepath.Join(h.SysRoot, "block", "*", name, "partition"))
	if err != nil {
		return "", 0, err
	}
	if len(matches) != 1 {
		return "", 0, fmt.Errorf("%s is %s, which is not a partition", dev, "/dev/"+name)
	}
	s, err := readTrimmed(matches[0])
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return "", 0, fmt.Errorf("partition %s: %w", name, err)
	}
	return filepath.Base(filepath.Dir(filepath.Dir(matches[0]))), n, nil
}
