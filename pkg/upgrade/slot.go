package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/verity"
)

// SlotWriter writes an image's store into a slot of a disk: the boot disk's inactive slot in an
// upgrade, slot A of the target disk in an install. A slot is retired before it is written and
// activated once its store verifies from the disk, so a slot written in part never carries the
// partition UUIDs a UKI's usrhash= finds.
type SlotWriter struct {
	Run node.Runner
	// Disk is the disk's device.
	Disk string
	// OpenPartition opens a partition of the disk.
	OpenPartition func(p Partition, write bool) (PartitionFile, error)
	// TableLock, when set, is held while the disk's partition table is read or changed.
	TableLock sync.Locker
	// LockWait is how long a read or change of the partition table waits for the disk's lock
	// while another program holds it; 30 seconds when zero.
	LockWait time.Duration
	// Change, when set, is called before each change to the disk; tests make it fail to stop a
	// write at each of them.
	Change func(what string) error
}

func (w *SlotWriter) change(what string) error {
	if w.Change == nil {
		return nil
	}
	return w.Change(what)
}

// lockTable takes the partition table's lock, when there is one, and returns its release.
func (w *SlotWriter) lockTable() func() {
	if w.TableLock == nil {
		return func() {}
	}
	w.TableLock.Lock()
	return w.TableLock.Unlock
}

// defaultLockWait is how long sfdisk waits for the disk's lock by default.
const defaultLockWait = 30 * time.Second

// sfdisk runs sfdisk on the disk with its BSD lock, as systemd-repart takes it, and which udev
// waits for before it probes the disk. sfdisk takes the lock before it reads or writes anything;
// while another program holds it, sfdisk is run again for a bounded time, so the partition
// table's lock, which chalkd's other requests wait for, is not held as long as another program
// holds the disk's.
func (w *SlotWriter) sfdisk(ctx context.Context, args ...string) ([]byte, error) {
	wait := w.LockWait
	if wait == 0 {
		wait = defaultLockWait
	}
	deadline := time.Now().Add(wait)
	for {
		out, err := w.Run.Run(ctx, "sfdisk", append([]string{"--lock=nonblock"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "already locked") {
			return out, err
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("another program kept %s locked for %v: %w", w.Disk, wait, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(250*time.Millisecond, time.Until(deadline))):
		}
	}
}

// ReadTable reads the disk's GPT with sfdisk, under the partition table's lock.
func (w *SlotWriter) ReadTable(ctx context.Context) ([]Partition, error) {
	unlock := w.lockTable()
	defer unlock()
	return w.readTable(ctx)
}

func (w *SlotWriter) readTable(ctx context.Context) ([]Partition, error) {
	out, err := w.sfdisk(ctx, "--json", w.Disk)
	if err != nil {
		return nil, fmt.Errorf("read the partition table of %s: %w", w.Disk, err)
	}
	var dump struct {
		Table struct {
			Label      string      `json:"label"`
			Partitions []Partition `json:"partitions"`
		} `json:"partitiontable"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		return nil, fmt.Errorf("read the partition table of %s: %w", w.Disk, err)
	}
	if dump.Table.Label != "gpt" {
		return nil, fmt.Errorf("%s has no GPT", w.Disk)
	}
	parts := dump.Table.Partitions
	for i, p := range parts {
		number, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(p.Node, w.Disk), "p"))
		if err != nil {
			return nil, fmt.Errorf("partition %s of %s: no partition number", p.Node, w.Disk)
		}
		parts[i].Number = number
		parts[i].Type = strings.ToLower(p.Type)
		parts[i].UUID = strings.ToLower(p.UUID)
	}
	return parts, nil
}

// Retire gives the slot's partitions random UUIDs and the label _empty, so nothing boots it.
func (w *SlotWriter) Retire(ctx context.Context, slot Slot) error {
	if err := w.setSlot(ctx, slot, randomUUID(), emptyLabel, randomUUID(), emptyLabel); err != nil {
		return err
	}
	log.Printf("retired the slot of partitions %d and %d", slot.Verity.Number, slot.Data.Number)
	return nil
}

// Write writes the store and its hash tree that the stream holds into the slot and checks them
// against the root hash, as read back from the disk. A slot that holds them already is left as
// it is, and the stream's copies are skipped. It reports whether it wrote the slot.
func (w *SlotWriter) Write(h Header, slot Slot, stream io.Reader) (bool, error) {
	if w.Holds(slot, h) {
		log.Printf("the slot of partitions %d and %d holds the store of %s already", slot.Verity.Number, slot.Data.Number, h.Version)
		if _, err := io.CopyN(io.Discard, stream, h.StoreSize+h.VeritySize); err != nil {
			return false, fmt.Errorf("receive the image: %w", err)
		}
		return false, nil
	}
	if h.StoreSize > slot.Data.Bytes() || h.VeritySize > slot.Verity.Bytes() {
		return false, fmt.Errorf("the image's store of %d bytes and hash tree of %d bytes do not fit the slot's partitions of %d and %d bytes", h.StoreSize, h.VeritySize, slot.Data.Bytes(), slot.Verity.Bytes())
	}
	for _, part := range []struct {
		what string
		p    Partition
		size int64
		sum  []byte
	}{
		{"store", slot.Data, h.StoreSize, h.StoreSHA256},
		{"hash tree", slot.Verity, h.VeritySize, h.VeritySHA256},
	} {
		if err := w.change("write the " + part.what); err != nil {
			return false, err
		}
		if err := w.copyInto(part.p, stream, part.size, part.sum, part.what); err != nil {
			return false, err
		}
	}
	sb, err := w.verify(slot, h.RootHash)
	if err != nil {
		return false, fmt.Errorf("the written store: %w", err)
	}
	if sb.DataSize() != h.StoreSize || sb.HashSize() != h.VeritySize {
		return false, fmt.Errorf("the hash tree covers %d bytes of store in %d bytes, but the image has %d and %d", sb.DataSize(), sb.HashSize(), h.StoreSize, h.VeritySize)
	}
	return true, nil
}

// Holds reports whether the slot's partitions hold the image's store and hash tree, whatever
// their UUIDs and labels. Only a hash tree with the image's root hash is read in full.
func (w *SlotWriter) Holds(slot Slot, h Header) bool {
	hash, err := w.OpenPartition(slot.Verity, false)
	if err != nil {
		return false
	}
	sb, root, err := verity.Root(hash)
	hash.Close()
	if err != nil || !bytes.Equal(root, h.RootHash) || sb.DataSize() != h.StoreSize || sb.HashSize() != h.VeritySize {
		return false
	}
	_, err = w.verify(slot, h.RootHash)
	return err == nil
}

func (w *SlotWriter) verify(slot Slot, root []byte) (verity.Superblock, error) {
	data, err := w.OpenPartition(slot.Data, false)
	if err != nil {
		return verity.Superblock{}, err
	}
	defer data.Close()
	hash, err := w.OpenPartition(slot.Verity, false)
	if err != nil {
		return verity.Superblock{}, err
	}
	defer hash.Close()
	return verity.Verify(data, hash, root)
}

// chunk is how much of the stream is written at once.
const chunk = 4 << 20

// copyInto writes size bytes of the stream to the start of the partition and checks their
// SHA-256.
func (w *SlotWriter) copyInto(p Partition, stream io.Reader, size int64, sum []byte, what string) error {
	f, err := w.OpenPartition(p, true)
	if errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("partition %d is in use; it must not be written", p.Number)
	}
	if err != nil {
		return fmt.Errorf("open partition %d: %w", p.Number, err)
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, min(chunk, size))
	for off := int64(0); off < size; {
		b := buf[:min(int64(len(buf)), size-off)]
		if _, err := io.ReadFull(stream, b); err != nil {
			return fmt.Errorf("receive the %s: %w", what, err)
		}
		h.Write(b)
		if _, err := f.WriteAt(b, off); err != nil {
			return fmt.Errorf("write the %s: %w", what, err)
		}
		off += int64(len(b))
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("write the %s: %w", what, err)
	}
	if got := h.Sum(nil); !bytes.Equal(got, sum) {
		return fmt.Errorf("the %s's SHA-256 is %x, want %x", what, got, sum)
	}
	return f.Close()
}

// Activate gives the slot the partition UUIDs systemd derives from the image's root hash and the
// labels naming its version, so a UKI booting the image finds its store there.
func (w *SlotWriter) Activate(ctx context.Context, slot Slot, h Header) error {
	dataUUID, verityUUID := PartitionUUIDs(h.RootHash)
	return w.setSlot(ctx, slot, verityUUID, "store-verity_"+h.Version, dataUUID, "store_"+h.Version)
}

// setSlot gives the slot's verity and data partitions UUIDs and labels. It reads the partition
// table again under its lock, and refuses partitions that moved since the slot was found.
func (w *SlotWriter) setSlot(ctx context.Context, slot Slot, verityUUID, verityLabel, dataUUID, dataLabel string) error {
	unlock := w.lockTable()
	defer unlock()
	parts, err := w.readTable(ctx)
	if err != nil {
		return err
	}
	current := map[int]Partition{}
	for _, p := range parts {
		current[p.Number] = p
	}
	for _, set := range []struct {
		p           Partition
		uuid, label string
	}{
		{slot.Verity, verityUUID, verityLabel},
		{slot.Data, dataUUID, dataLabel},
	} {
		p, ok := current[set.p.Number]
		if !ok || p.Start != set.p.Start || p.Size != set.p.Size || p.Type != set.p.Type {
			return fmt.Errorf("partition %d of the slot changed while it was written", set.p.Number)
		}
		if err := w.setPartition(ctx, p, set.uuid, set.label); err != nil {
			return err
		}
	}
	return nil
}

// setPartition gives a partition of the disk a UUID and a label, unless it has them.
func (w *SlotWriter) setPartition(ctx context.Context, p Partition, uuid, label string) error {
	if p.UUID != uuid {
		if err := w.change(fmt.Sprintf("set the UUID of partition %d", p.Number)); err != nil {
			return err
		}
		if _, err := w.sfdisk(ctx, "--no-tell-kernel", "--part-uuid", w.Disk, strconv.Itoa(p.Number), uuid); err != nil {
			return fmt.Errorf("set the UUID of partition %d: %w", p.Number, err)
		}
	}
	if p.Name != label {
		if err := w.change(fmt.Sprintf("label partition %d", p.Number)); err != nil {
			return err
		}
		if _, err := w.sfdisk(ctx, "--no-tell-kernel", "--part-label", w.Disk, strconv.Itoa(p.Number), label); err != nil {
			return fmt.Errorf("label partition %d: %w", p.Number, err)
		}
	}
	return nil
}
