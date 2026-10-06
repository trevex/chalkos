package storage

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// addPartition adds a partition of the disk to the fixture trees, with a udev link to it.
func addPartition(t *testing.T, h Host, disk, name, devnum string, number int, sectors uint64, props map[string]string, link string) {
	t.Helper()
	dir := filepath.Join(h.SysRoot, "block", disk, name)
	files := map[string]string{
		filepath.Join(dir, "partition"): strconv.Itoa(number) + "\n",
		filepath.Join(dir, "dev"):       devnum + "\n",
		filepath.Join(dir, "size"):      strconv.FormatUint(sectors, 10) + "\n",
		filepath.Join(h.DevRoot, name):  "",
	}
	var udev string
	for k, v := range props {
		udev += "E:" + k + "=" + v + "\n"
	}
	files[filepath.Join(h.UdevRoot, "b"+devnum)] = udev
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if link != "" {
		path := filepath.Join(h.DevRoot, link)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(filepath.Dir(path), filepath.Join(h.DevRoot, name))
		if err := os.Symlink(rel, path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPartitions(t *testing.T) {
	h := newHost(t, nvmeSystem)
	addPartition(t, h, "nvme0n1", "nvme0n1p10", "259:10", 10, 2048, map[string]string{"ID_PART_ENTRY_TYPE": "65F335D7-A1F7-F6DF-B954-A97D9A5DB9E6", "ID_PART_ENTRY_NAME": "var", "ID_PART_ENTRY_UUID": "7AD19BDF-77FF-4273-8A5C-D403D2A5F95B", "ID_FS_TYPE": "crypto_LUKS"}, "")
	addPartition(t, h, "nvme0n1", "nvme0n1p1", "259:1", 1, 4096, map[string]string{"ID_PART_ENTRY_TYPE": "c12a7328-f81f-11d2-ba4b-00a0c93ec93b", "ID_PART_ENTRY_NAME": "esp", "ID_FS_TYPE": "vfat"}, "")
	disks, err := h.Disks()
	if err != nil {
		t.Fatal(err)
	}
	parts, err := h.Partitions(disks[0])
	if err != nil {
		t.Fatal(err)
	}
	want := []Partition{
		{Name: "nvme0n1p1", Device: "/dev/nvme0n1p1", Number: 1, Size: 2 << 20, Type: "c12a7328-f81f-11d2-ba4b-00a0c93ec93b", Label: "esp", Content: "vfat"},
		{Name: "nvme0n1p10", Device: "/dev/nvme0n1p10", Number: 10, Size: 1 << 20, Type: "65f335d7-a1f7-f6df-b954-a97d9a5db9e6", Label: "var", UUID: "7ad19bdf-77ff-4273-8a5c-d403d2a5f95b", Content: "crypto_LUKS"},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Errorf("partitions = %+v, want %+v", parts, want)
	}
}

func TestPartitionOf(t *testing.T) {
	h := newHost(t, nvmeSystem, virtioDisk)
	addPartition(t, h, "nvme0n1", "nvme0n1p7", "259:7", 7, 2048, map[string]string{"ID_PART_ENTRY_UUID": "7AD19BDF-77FF-4273-8A5C-D403D2A5F95B"}, "disk/chalk-boot/var")
	addPartition(t, h, "vdb", "vdb1", "253:17", 1, 2048, map[string]string{"ID_PART_ENTRY_UUID": "d506b831-fde9-4335-b2be-9710f18219a6"}, "disk/by-partuuid/d506b831-fde9-4335-b2be-9710f18219a6")
	for dev, want := range map[string]struct {
		disk, device, uuid string
		number             int
	}{
		"/dev/disk/chalk-boot/var":                                   {"nvme0n1", "/dev/nvme0n1p7", "7ad19bdf-77ff-4273-8a5c-d403d2a5f95b", 7},
		"/dev/disk/by-partuuid/d506b831-fde9-4335-b2be-9710f18219a6": {"vdb", "/dev/vdb1", "d506b831-fde9-4335-b2be-9710f18219a6", 1},
	} {
		disk, p, err := h.PartitionOf(dev)
		if err != nil || disk != want.disk || p.Number != want.number || p.Device != want.device || p.UUID != want.uuid {
			t.Errorf("PartitionOf(%s) = %s, %+v, %v; want %s, %+v", dev, disk, p, err, want.disk, want)
		}
	}
	if _, _, err := h.PartitionOf("/dev/vdb"); err == nil {
		t.Error("took a whole disk for a partition")
	}
}
