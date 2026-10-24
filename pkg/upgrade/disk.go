package upgrade

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/storage/gpt"
)

// emptyLabel marks a slot that holds no image, as the image's first boot creates slot B.
const emptyLabel = "_empty"

// Slot is a store slot: its verity and data partitions.
type Slot struct {
	Verity, Data gpt.Partition
}

// labelHashDigits is how many hex digits of the root hash a slot's labels carry: GPT labels hold
// 36 characters, of which "store-verity_" takes 13.
const labelHashDigits = 16

// LabelID is what the labels of a slot holding the store of the root hash name it by: the root
// hash's first 16 hex digits, as in store_<id> and store-verity_<id>. Nothing finds a store by
// label; the labels name it for people and tools.
func LabelID(rootHash []byte) string {
	return hex.EncodeToString(rootHash)[:labelHashDigits]
}

// Label is the ID the slot's labels name its store by, or "" when it is retired or empty.
func (s Slot) Label() string {
	id, ok := strings.CutPrefix(s.Data.Name, "store_")
	if !ok || s.Verity.Name != "store-verity_"+id {
		return ""
	}
	return id
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
	return gpt.FormatUUID(rootHash[:16]), gpt.FormatUUID(rootHash[16:32])
}

// slots finds the boot disk's two store slots, pairing verity and data partitions in disk order,
// and tells the one holding the running image from the other.
func slots(parts []gpt.Partition, running []byte) (booted, inactive Slot, err error) {
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
func OpenPartition(p gpt.Partition, write bool) (PartitionFile, error) {
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
func StoreSlots(disk string, parts []gpt.Partition) ([]Slot, error) {
	var verity, data []gpt.Partition
	for _, p := range parts {
		switch {
		case gpt.IsStoreVerity(p.Type):
			verity = append(verity, p)
		case gpt.IsStoreData(p.Type):
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
