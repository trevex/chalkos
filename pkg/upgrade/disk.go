package upgrade

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// GPT partition types of the store's data and verity partitions.
var (
	dataTypes   = map[string]bool{"8484680c-9521-48c6-9c11-b0720656f69e": true, "b0e01050-ee5f-4390-949a-9101b17104e9": true}
	verityTypes = map[string]bool{"77ff5f63-e7b6-4633-acf4-1565b864c0e6": true, "6e11a4e7-fbca-4ded-b9e9-e1a512bb664e": true}
)

// emptyLabel marks a slot that holds no image, as the image's first boot creates slot B.
const emptyLabel = "_empty"

// Partition is a partition of a disk as sfdisk --json lists it.
type Partition struct {
	Node string `json:"node"`
	// Start and Size count the disk's sectors.
	Start  int64  `json:"start"`
	Size   int64  `json:"size"`
	Type   string `json:"type"`
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Number int    `json:"-"`
	// SectorSize is the disk's sector size in bytes.
	SectorSize int64 `json:"-"`
}

// Bytes is the partition's size in bytes.
func (p Partition) Bytes() int64 { return p.Size * p.SectorSize }

// ParseTable reads a disk's GPT from what sfdisk --json printed for it. Partition numbers come
// from the device names sfdisk derives, /dev/vda3 or /dev/nvme0n1p3, and sizes count the sectors
// of the table's sector size: 4096 bytes on a 4Kn disk, 512 when sfdisk names none.
func ParseTable(disk string, sfdiskJSON []byte) ([]Partition, error) {
	var dump struct {
		Table struct {
			Label      string      `json:"label"`
			SectorSize int64       `json:"sectorsize"`
			Partitions []Partition `json:"partitions"`
		} `json:"partitiontable"`
	}
	if err := json.Unmarshal(sfdiskJSON, &dump); err != nil {
		return nil, fmt.Errorf("read the partition table of %s: %w", disk, err)
	}
	t := dump.Table
	if t.Label != "gpt" {
		return nil, fmt.Errorf("%s has a %q partition table, not a GPT", disk, t.Label)
	}
	if t.SectorSize == 0 {
		t.SectorSize = 512
	}
	parts := t.Partitions
	for i, p := range parts {
		number, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(p.Node, disk), "p"))
		if err != nil {
			return nil, fmt.Errorf("partition %s of %s: no partition number", p.Node, disk)
		}
		parts[i].Number = number
		parts[i].SectorSize = t.SectorSize
		parts[i].Type = strings.ToLower(p.Type)
		parts[i].UUID = strings.ToLower(p.UUID)
	}
	return parts, nil
}

// Slot is a store slot: its verity and data partitions.
type Slot struct {
	Verity, Data Partition
}

// Version is the version a slot's labels name, or "" when it is retired or empty.
func (s Slot) Version() string {
	v, ok := strings.CutPrefix(s.Data.Name, "store_")
	if !ok || s.Verity.Name != "store-verity_"+v {
		return ""
	}
	return v
}

// Holds reports whether the slot's partition UUIDs are those systemd derives from the root
// hash, so a UKI with that hash boots from it.
func (s Slot) Holds(rootHash []byte) bool {
	data, verity := PartitionUUIDs(rootHash)
	return s.Data.UUID == data && s.Verity.UUID == verity
}

// PartitionUUIDs are the partition UUIDs systemd looks for with usrhash=: the data partition's
// is the root hash's first 128 bits, the verity partition's its last.
func PartitionUUIDs(rootHash []byte) (data, verity string) {
	return formatUUID(rootHash[:16]), formatUUID(rootHash[16:32])
}

func formatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return formatUUID(b)
}

// slots finds the boot disk's two store slots, pairing verity and data partitions in disk order,
// and tells the one holding the running image from the other.
func slots(parts []Partition, running []byte) (booted, inactive Slot, err error) {
	pairs, err := StoreSlots("the boot disk", parts)
	if err != nil {
		return Slot{}, Slot{}, err
	}
	// The node found its store by these UUIDs; on two slots, which one it runs is unknown.
	if pairs[0].Holds(running) && pairs[1].Holds(running) {
		return Slot{}, Slot{}, errors.New("both slots carry the running store's partition UUIDs; which one the node runs is unknown")
	}
	dataUUID, verityUUID := PartitionUUIDs(running)
	for i, s := range pairs {
		if !s.Holds(running) {
			continue
		}
		other := pairs[1-i]
		for _, uuid := range []string{other.Data.UUID, other.Verity.UUID} {
			if uuid == dataUUID || uuid == verityUUID {
				return Slot{}, Slot{}, fmt.Errorf("the inactive slot carries the running store's partition UUID %s; which partition the node runs is unknown", uuid)
			}
		}
		return s, other, nil
	}
	return Slot{}, Slot{}, errors.New("no slot of the boot disk holds the running image's store")
}

// PartitionFile is a partition opened for reading or writing.
type PartitionFile interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
}

// OpenPartition opens a partition's device. Writing takes it exclusively, which the kernel refuses
// while it is mounted or held by a device-mapper device, as the running store's partitions are;
// reading drops what the page cache holds of it first, so a check reads the disk.
func OpenPartition(p Partition, write bool) (PartitionFile, error) {
	if write {
		return os.OpenFile(p.Node, os.O_RDWR|os.O_EXCL, 0)
	}
	f, err := os.Open(p.Node)
	if err != nil {
		return nil, err
	}
	if err := unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED); err != nil {
		f.Close()
		return nil, fmt.Errorf("drop the cached data of %s: %w", p.Node, err)
	}
	return f, nil
}

// StoreSlots finds the two store slots of a disk, which errors name, pairing verity and data
// partitions in disk order: slot A first, then slot B.
func StoreSlots(disk string, parts []Partition) ([]Slot, error) {
	var verity, data []Partition
	for _, p := range parts {
		switch {
		case verityTypes[p.Type]:
			verity = append(verity, p)
		case dataTypes[p.Type]:
			data = append(data, p)
		}
	}
	if len(verity) != 2 || len(data) != 2 {
		return nil, fmt.Errorf("%s has %d store and %d verity partitions; it needs two slots of each", disk, len(data), len(verity))
	}
	return []Slot{{verity[0], data[0]}, {verity[1], data[1]}}, nil
}

// Fits refuses an image whose store or hash tree is larger than the slot's partitions.
func (s Slot) Fits(h Header) error {
	if h.StoreSize > s.Data.Bytes() || h.VeritySize > s.Verity.Bytes() {
		return fmt.Errorf("the image's store of %d bytes and hash tree of %d bytes do not fit the slot's partitions of %d and %d bytes", h.StoreSize, h.VeritySize, s.Data.Bytes(), s.Verity.Bytes())
	}
	return nil
}
