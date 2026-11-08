package storeslot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/trevex/chalkos/pkg/imagepart"
	"github.com/trevex/chalkos/pkg/storage/gpt"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/verity"
)

// Writer writes an image's store into the slots of a disk.
type Writer struct {
	Run tool.Runner
	// Disk is the disk's device.
	Disk string
	// OpenPartition opens a partition of the disk.
	OpenPartition func(p gpt.Partition, write bool) (PartitionFile, error)
	// TableLock, when set, is held while the disk's partition table is read or changed.
	TableLock sync.Locker
	// LockWait is how long a read or change of the partition table waits for the disk's lock
	// while another program holds it; 30 seconds when zero.
	LockWait time.Duration
	// Change, when set, is called before each change to the disk; tests make it fail to stop a
	// write at each of them.
	Change func(what string) error
}

func (w *Writer) change(what string) error {
	if w.Change == nil {
		return nil
	}
	return w.Change(what)
}

// lockTable takes the partition table's lock, when there is one, and returns its release.
func (w *Writer) lockTable() func() {
	if w.TableLock == nil {
		return func() {}
	}
	w.TableLock.Lock()
	return w.TableLock.Unlock
}

// sfdisk runs sfdisk on the disk with its lock, waiting LockWait for it at most.
func (w *Writer) sfdisk() gpt.Sfdisk {
	return gpt.Sfdisk{Run: w.Run, Disk: w.Disk, LockWait: w.LockWait}
}

// ReadTable reads the disk's GPT with sfdisk, under the partition table's lock.
func (w *Writer) ReadTable(ctx context.Context) ([]gpt.Partition, error) {
	unlock := w.lockTable()
	defer unlock()
	return w.readTable(ctx)
}

func (w *Writer) readTable(ctx context.Context) ([]gpt.Partition, error) {
	return w.sfdisk().ReadTable(ctx)
}

// Retire gives the slot's partitions random UUIDs and the label _empty, so nothing boots it.
func (w *Writer) Retire(ctx context.Context, slot Slot) error {
	if err := w.setSlot(ctx, slot, gpt.NewUUID(), EmptyLabel, gpt.NewUUID(), EmptyLabel); err != nil {
		return err
	}
	log.Printf("retired the slot of partitions %d and %d", slot.Verity.Number, slot.Data.Number)
	return nil
}

// Write writes the store and its hash tree that the stream holds into the slot and checks them
// against the root hash, as read back from the disk. A slot that holds them already is left as
// it is, and the stream's copies are skipped. It reports whether it wrote the slot.
func (w *Writer) Write(h Image, slot Slot, stream io.Reader) (bool, error) {
	if w.Holds(slot, h) {
		log.Printf("the slot of partitions %d and %d holds the store %x already", slot.Verity.Number, slot.Data.Number, h.RootHash)
		return false, Skip(h, stream)
	}
	if err := slot.Fits(h); err != nil {
		return false, err
	}
	for _, part := range []struct {
		what string
		p    gpt.Partition
		part imagepart.Part
	}{
		{"store", slot.Data, h.Store},
		{"hash tree", slot.Verity, h.Verity},
	} {
		if err := w.change("write the " + part.what); err != nil {
			return false, err
		}
		if err := w.copyInto(part.p, stream, part.part, part.what); err != nil {
			return false, err
		}
	}
	sb, err := w.verify(slot, h.RootHash)
	if err != nil {
		return false, fmt.Errorf("the written store: %w", err)
	}
	if sb.DataSize() != h.Store.Size || sb.HashSize() != h.Verity.Size {
		return false, fmt.Errorf("the hash tree covers %d bytes of store in %d bytes, but the image has %d and %d", sb.DataSize(), sb.HashSize(), h.Store.Size, h.Verity.Size)
	}
	return true, nil
}

// Holds reports whether the slot's partitions hold the image's store and hash tree, whatever
// their UUIDs and labels. Only a hash tree with the image's root hash is read in full.
func (w *Writer) Holds(slot Slot, h Image) bool {
	hash, err := w.OpenPartition(slot.Verity, false)
	if err != nil {
		return false
	}
	sb, root, err := verity.Root(hash)
	hash.Close()
	if err != nil || !bytes.Equal(root, h.RootHash) || sb.DataSize() != h.Store.Size || sb.HashSize() != h.Verity.Size {
		return false
	}
	_, err = w.verify(slot, h.RootHash)
	return err == nil
}

func (w *Writer) verify(slot Slot, root []byte) (verity.Superblock, error) {
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

// copyInto writes the part that the stream holds to the start of the partition and checks its
// SHA-256.
func (w *Writer) copyInto(p gpt.Partition, stream io.Reader, part imagepart.Part, what string) error {
	f, err := w.OpenPartition(p, true)
	if errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("partition %d is in use; it must not be written", p.Number)
	}
	if err != nil {
		return fmt.Errorf("open partition %d: %w", p.Number, err)
	}
	defer f.Close()
	if err := part.Receive(io.NewOffsetWriter(f, 0), stream, what); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("write the %s: %w", what, err)
	}
	return f.Close()
}

// Activate gives the slot the partition UUIDs systemd derives from the image's root hash, so a
// UKI booting the image finds its store there, and the labels naming the store by its root hash.
func (w *Writer) Activate(ctx context.Context, slot Slot, h Image) error {
	dataUUID, verityUUID := PartitionUUIDs(h.RootHash)
	id := LabelID(h.RootHash)
	return w.setSlot(ctx, slot, verityUUID, "store-verity_"+id, dataUUID, "store_"+id)
}

// setSlot gives the slot's verity and data partitions UUIDs and labels. It reads the partition
// table again under its lock, and refuses partitions that moved since the slot was found.
func (w *Writer) setSlot(ctx context.Context, slot Slot, verityUUID, verityLabel, dataUUID, dataLabel string) error {
	unlock := w.lockTable()
	defer unlock()
	parts, err := w.readTable(ctx)
	if err != nil {
		return err
	}
	current := map[int]gpt.Partition{}
	for _, p := range parts {
		current[p.Number] = p
	}
	for _, set := range []struct {
		p           gpt.Partition
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
func (w *Writer) setPartition(ctx context.Context, p gpt.Partition, uuid, label string) error {
	if p.UUID != uuid {
		if err := w.change(fmt.Sprintf("set the UUID of partition %d", p.Number)); err != nil {
			return err
		}
		if _, err := w.sfdisk().Exec(ctx, "--no-tell-kernel", "--part-uuid", w.Disk, strconv.Itoa(p.Number), uuid); err != nil {
			return fmt.Errorf("set the UUID of partition %d: %w", p.Number, err)
		}
	}
	if p.Name != label {
		if err := w.change(fmt.Sprintf("label partition %d", p.Number)); err != nil {
			return err
		}
		if _, err := w.sfdisk().Exec(ctx, "--no-tell-kernel", "--part-label", w.Disk, strconv.Itoa(p.Number), label); err != nil {
			return fmt.Errorf("label partition %d: %w", p.Number, err)
		}
	}
	return nil
}

// WriteActive writes the image's store into the slot and activates it, unless the slot holds it
// already, active and labelled for it: then the stream's copies are skipped. The slot is retired
// first, so a slot written in part never carries the UUIDs a UKI finds its store by.
func (w *Writer) WriteActive(ctx context.Context, slot Slot, h Image, stream io.Reader) error {
	if slot.Holds(h.RootHash) && slot.Label() == LabelID(h.RootHash) && w.Holds(slot, h) {
		log.Printf("the slot of partitions %d and %d holds the store %x already", slot.Verity.Number, slot.Data.Number, h.RootHash)
		return Skip(h, stream)
	}
	if err := slot.Fits(h); err != nil {
		return err
	}
	if err := w.Retire(ctx, slot); err != nil {
		return err
	}
	if _, err := w.Write(h, slot, stream); err != nil {
		return err
	}
	return w.Activate(ctx, slot, h)
}

// Skipper is a stream that passes over bytes without reading them, as a pull passes over the
// layers of parts a slot holds already without fetching them.
type Skipper interface {
	Skip(n int64) error
}

// Skip receives the store and its hash tree that the stream holds for a slot that holds them
// already, and checks their SHA-256 all the same: every part of an image is checked. A Skipper
// passes over them without reading them instead: the slot holds them already, as Holds found by
// reading its hash tree and verifying its store against the root hash.
func Skip(h Image, stream io.Reader) error {
	// What the stream would carry need not be read: Holds verified the slot's store and hash tree
	// against the header's root hash and sizes, and the slot keeps them.
	if s, ok := stream.(Skipper); ok {
		if err := s.Skip(h.Store.Size + h.Verity.Size); err != nil {
			return fmt.Errorf("pass over the store and hash tree: %w", err)
		}
		return nil
	}
	if err := h.Store.Receive(io.Discard, stream, "store"); err != nil {
		return err
	}
	return h.Verity.Receive(io.Discard, stream, "hash tree")
}
