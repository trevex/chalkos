// Package upgrade installs a new image into the node's inactive store slot and makes it the next
// boot. systemd-boot counts its boots by the tries in the UKI's name, until the boot is found
// healthy and blessed, and falls back to the running image once the tries are used up.
//
// The node keeps no state of its own: the slots' partition UUIDs and labels and the UKIs on the
// ESP say what is installed. Each step can be stopped at any point and the upgrade run again,
// and at every point the disk boots the running image, or the new one with all its tries:
//
//  1. Retire the inactive slot: remove every UKI of the image but those booting the running store,
//     then give the slot's partitions random UUIDs and the label _empty, so nothing boots it.
//  2. Write the store and its hash tree into the slot, unless it holds them already, and check
//     them against the root hash.
//  3. Write the UKI under a name systemd-boot ignores and check it: it must boot this store, name
//     the node's image and platform, and carry a signature of the firmware's db when Secure
//     Boot is on. Then the caller checks the image itself, reading the store through dm-verity,
//     as chalkd checks the node's config against the image's schemas and keeps it for the image.
//  4. Activate: give the slot the partition UUIDs systemd derives from the root hash and the
//     labels naming it, and rename the UKI to <id>_<generation>+<tries>.efi, one generation above
//     the image's highest on the ESP.
//
// An image is its store's root hash; nothing compares versions. systemd-boot orders chalkos's
// entries by their IDs alone, the highest generation first, as every UKI's os-release names the
// same image ID and an empty IMAGE_VERSION: the image installed last boots next, an older one's
// too, and the boot falls back to the entry before once its tries are used up.
//
// The running slot and its UKI are never written. A rollback, Node.Rollback, makes the image before
// an upgrade boot next again while the inactive slot still holds it: it renames that image's UKI
// to the next generation, as step 4 names a new one. The name carries the UKI's tries while the
// running image's boot was found healthy, so the node falls back to it; before that it carries
// none, so the image, whose boot was found healthy before, stays a known-good entry.
//
// The installer shares the checks of an image's UKI and boot loader, CheckUKI and CheckSecureBoot,
// and writes slot A of the disk it installs a node onto with the same slot writer,
// storeslot.Writer.
package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/storage/gpt"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/uki"
)

// Node is the running node an upgrade changes. Tests point it at a disk image and directories.
type Node struct {
	Run tool.Runner
	// Disk is the boot disk's device.
	Disk string
	// ESP is where the ESP is mounted, EFIVars where efivarfs is, and Cmdline the running kernel's
	// command line.
	ESP, EFIVars, Cmdline string
	// OSRelease is the running image's os-release.
	OSRelease map[string]string
	// Architecture is the node's, as images name theirs.
	Architecture string
	// OpenPartition opens a partition of the boot disk.
	OpenPartition func(p gpt.Partition, write bool) (storeslot.PartitionFile, error)
	// TableLock, when set, is held while the boot disk's partition table is read or changed:
	// chalkd's storage changes take it too.
	TableLock sync.Locker
	// LockWait is how long a read or change of the partition table waits for the disk's lock
	// while another program holds it; 30 seconds when zero.
	LockWait time.Duration
	// Change, when set, is called before each change to the disk, the ESP or the firmware's
	// variables; tests make it fail to stop an upgrade at each of them.
	Change func(what string) error
	// CheckStore, when set, checks the image before it becomes the next boot: Install mounts the
	// written store read-only through dm-verity at StoreMount, /run/chalkd/upgrade-store when
	// empty, and passes it. When the check fails, the slot stays retired and the running image
	// stays the next boot.
	CheckStore func(Store) error
	StoreMount string
}

func (n *Node) change(what string) error {
	if n.Change == nil {
		return nil
	}
	return n.Change(what)
}

// Result is what an upgrade did.
type Result struct {
	// AlreadyInstalled is set when the node runs the image already, and nothing changed.
	AlreadyInstalled bool
	// AlreadyStaged is set when the image boots next already, from Entry, and nothing changed.
	AlreadyStaged bool
	// Entry is the UKI that boots the image from the next boot on.
	Entry string
	// Wrote is set when the store was written; it is not when the slot held it already.
	Wrote bool
}

// StagedRetiredError is the error of an install that found its image staged to boot next already,
// checked it again and retired it, as the check failed: the image no longer boots next. Err is
// the check's error.
type StagedRetiredError struct{ Err error }

func (e *StagedRetiredError) Error() string { return e.Err.Error() }

func (e *StagedRetiredError) Unwrap() error { return e.Err }

// CheckError is a refusal of the image by CheckStore, as opposed to a failure to mount the store
// it checks or to read the UKI that names the store's system. Err is the refusal.
type CheckError struct{ Err error }

func (e *CheckError) Error() string { return e.Err.Error() }

func (e *CheckError) Unwrap() error { return e.Err }

// Install installs the image whose store, hash tree and UKI the stream holds, in that order, into
// the inactive slot and makes it the next boot. Everything is checked before the slot is
// retired, except what only the received image tells; the slot stays retired until that passed.
func (n *Node) Install(ctx context.Context, h Header, stream io.Reader) (Result, error) {
	if err := h.Validate(); err != nil {
		return Result{}, err
	}
	if h.BootLoaderSize != 0 {
		return Result{}, errors.New("an upgrade leaves the boot loader as it is; the image must not bring one")
	}
	if h.Architecture != n.Architecture {
		return Result{}, fmt.Errorf("the image is built for %s, this machine for %s", h.Architecture, n.Architecture)
	}
	running, err := uki.RunningHash(n.Cmdline)
	if err != nil {
		return Result{}, err
	}
	for _, field := range []struct{ name, image, node string }{
		{"image ID", h.ImageID, n.OSRelease["IMAGE_ID"]},
		{"platform", h.Platform, n.OSRelease["CHALKOS_PLATFORM"]},
	} {
		if field.node == "" {
			return Result{}, fmt.Errorf("the running image names no %s", field.name)
		}
		if field.image != field.node {
			return Result{}, fmt.Errorf("the image's %s is %s, the node's %s", field.name, field.image, field.node)
		}
	}
	if bytes.Equal(h.RootHash, running) {
		return Result{AlreadyInstalled: true}, nil
	}
	w := n.slots()
	parts, err := w.ReadTable(ctx)
	if err != nil {
		return Result{}, err
	}
	_, inactive, err := slots(parts, running)
	if err != nil {
		return Result{}, err
	}
	all, err := esp.Entries(n.ESP)
	if err != nil {
		return Result{}, err
	}
	booted, err := Booted(all, n.EFIVars, running)
	if err != nil {
		return Result{}, err
	}
	// The new entry boots before the entries an upgrade keeps (the booted one and those booting
	// the running store) only while their names order them by generation, as chalkd names them:
	// from 1 on, without leading zeros. Other names were placed by hand or are corrupted, and
	// systemd-boot may sort one above the new entry. Examples are chalkos_5a.efi and
	// chalkos_7+2147483648.efi, whose ID is the whole name.
	for _, e := range all {
		if retired(e, h.ImageID, booted, running) || !ofImage(e, h.ImageID) && e.ID != booted.ID {
			continue
		}
		if err := misnamed(e, h.ImageID); err != nil {
			return Result{}, err
		}
	}
	// The UKI is renamed last, to the image's next generation, above the generation any name on the
	// ESP gives, so it replaces no UKI and shares no entry's ID.
	generation, err := esp.NextGeneration(all, h.ImageID)
	if err != nil {
		return Result{}, err
	}
	// The image boots next already, as after an upgrade without a reboot. The check runs again: the
	// upgrade brings the config the image boots with, which replaces the one staged with it.
	if staged, ok := esp.StagedEntry(all, h.ImageID, booted); ok && bytes.Equal(staged.RootHash, h.RootHash) && inactive.Holds(h.RootHash) {
		if err := n.recheck(ctx, h, inactive, staged, all, booted, running); err != nil {
			return Result{}, err
		}
		return Result{AlreadyStaged: true, Entry: staged.File}, nil
	}
	// UKIs of other images on the ESP, such as a rescue system's, are left alone; a UKI is of the
	// image its os-release names, or one that cannot be read of the image its name gives.
	entries := slices.DeleteFunc(slices.Clone(all), func(e esp.Entry) bool { return !ofImage(e, h.ImageID) })
	// Until the running image's boot is found good, the image it falls back to must stay.
	if booted.TriesLeft >= 0 {
		for _, e := range entries {
			// A UKI that cannot be read boots no image to fall back to.
			if e.ID != booted.ID && !e.Bad() && e.RootHash != nil && !bytes.Equal(e.RootHash, running) {
				return Result{}, fmt.Errorf("the boot of %s has not been found healthy yet; upgrade once it is, as the upgrade replaces %s, which the node falls back to", booted.File, e.File)
			}
		}
	}
	if err := inactive.Fits(h.Store()); err != nil {
		return Result{}, err
	}

	// From here on the steps change the disk: a client that goes away must not kill sfdisk while it
	// writes the partition table. Its stream ends, which stops the upgrade between steps.
	ctx = context.WithoutCancel(ctx)
	if n.CheckStore != nil {
		// A check an earlier upgrade did not finish, as when chalkd was stopped during it, may have
		// left the store it checked open, which keeps the slot's partitions busy for the write.
		n.closeStore(ctx, n.storeDir(), true, true)
	}
	if err := n.retire(ctx, all, h.ImageID, booted, running, inactive); err != nil {
		return Result{}, err
	}
	wrote, err := w.Write(h.Store(), inactive, stream)
	if err != nil {
		return Result{}, err
	}
	tmp, tries, err := n.receiveUKI(h, stream)
	// A UKI under its temporary name boots nothing, but takes space on the ESP that the next
	// upgrade needs; once activated it has its name, and should this removal fail, the next
	// retirement removes it.
	defer func() {
		if tmp == "" {
			return
		}
		if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			log.Printf("remove %s: %v", tmp, rerr)
		}
	}()
	if err == nil && n.CheckStore != nil {
		err = n.checkStore(ctx, h, inactive, tmp)
	}
	var entry string
	if err == nil {
		entry, err = n.activate(ctx, h, inactive, tmp, generation, tries)
	}
	if err != nil {
		return Result{}, err
	}
	log.Printf("installed %s %x into partitions %d and %d; it boots next as %s", h.ImageID, h.RootHash, inactive.Verity.Number, inactive.Data.Number, entry)
	return Result{Entry: entry, Wrote: wrote}, nil
}

// slots is the slot writer of the node's boot disk.
func (n *Node) slots() *storeslot.Writer {
	return &storeslot.Writer{Run: n.Run, Disk: n.Disk, OpenPartition: n.OpenPartition, TableLock: n.TableLock, LockWait: n.LockWait, Change: n.Change}
}

// ofImage reports whether the UKI is of the image: its os-release names the image or, when it
// does not parse as a UKI, its name gives a generation of the image. A UKI whose os-release names
// no IMAGE_ID is another system's, which systemd-boot sorts by its ID, such as nixos.
func ofImage(e esp.Entry, imageID string) bool {
	if !e.Readable {
		_, ok, _ := esp.NameGeneration(e.ID, imageID)
		return ok
	}
	return e.ImageID == imageID
}

// retired reports whether the retirement removes the UKI: one of the image that neither the node
// booted nor boots the running store.
func retired(e esp.Entry, imageID string, booted esp.Entry, running []byte) bool {
	return ofImage(e, imageID) && e.ID != booted.ID && !bytes.Equal(e.RootHash, running)
}

// misnamed is the error of an entry an upgrade or a rollback leaves on the ESP whose name chalkd
// would not give it: the next generation might not boot before it.
func misnamed(e esp.Entry, imageID string) error {
	if esp.EntryGeneration(e.ID, imageID) != 0 {
		return nil
	}
	return fmt.Errorf("%s on the ESP is not named %s_<generation>.efi with a generation from 1 on and no leading zeros, so the next generation might not boot before it; reinstall the node, or rename the entry", e.File, imageID)
}

// recheck checks the image a staged entry boots, as an upgrade checks one it writes. When the
// check fails:
//   - the slot and the entry are retired, as after a failed check of a written image, and the
//     running image stays the next boot. The error is a StagedRetiredError, which holds a
//     CheckError when CheckStore refused the image.
//   - when the retirement fails too, the staged image still boots next, and the error says so.
//   - while the running image's boot has not been found healthy, nothing is retired: the staged
//     image is the one the node falls back to.
func (n *Node) recheck(ctx context.Context, h Header, slot storeslot.Slot, staged esp.Entry, all []esp.Entry, booted esp.Entry, running []byte) error {
	if n.CheckStore == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	n.closeStore(ctx, n.storeDir(), true, true)
	err := n.checkStore(ctx, h, slot, filepath.Join(n.ESP, esp.LinuxDir, staged.File))
	if err == nil {
		return nil
	}
	// Until the running image's boot is found healthy, the staged image, which a rollback left, is
	// the one the node falls back to, so it stays.
	if booted.TriesLeft >= 0 {
		return err
	}
	if rerr := n.retire(ctx, all, h.ImageID, booted, running, slot); rerr != nil {
		return errors.Join(err, fmt.Errorf("retire the staged image, which still boots next: %w", rerr))
	}
	return &StagedRetiredError{Err: err}
}

// retire removes every UKI of the image but those booting the running store, along with UKIs an
// upgrade left half written, and then makes the inactive slot one nothing finds.
func (n *Node) retire(ctx context.Context, entries []esp.Entry, imageID string, booted esp.Entry, running []byte, slot storeslot.Slot) error {
	remove, err := esp.Leftovers(n.ESP, "")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if retired(e, imageID, booted, running) {
			remove = append(remove, e.File)
		}
	}
	if err := esp.Remove(n.ESP, remove, n.Change); err != nil {
		return err
	}
	return n.slots().Retire(ctx, slot)
}

// receiveUKI writes the UKI to the ESP under a name systemd-boot ignores and checks it. It
// returns the file, also when it fails once the file may exist, and the tries the image asks for.
func (n *Node) receiveUKI(h Header, stream io.Reader) (string, int, error) {
	if err := n.change("write the UKI"); err != nil {
		return "", 0, err
	}
	tmp, err := esp.Stage(n.ESP, h.RootHash)
	if err != nil {
		return "", 0, err
	}
	tries, err := ReceiveUKI(tmp, h, stream, n.EFIVars)
	if err != nil {
		return tmp, 0, err
	}
	if err := ExpectEnd(stream, "UKI"); err != nil {
		return tmp, 0, err
	}
	return tmp, tries, nil
}

// ExpectEnd checks that the stream ends after its last part, named last. A stream that goes on,
// or fails at its end, such as a pull whose last layer is longer than the header says, is refused.
func ExpectEnd(stream io.Reader, last string) error {
	extra, err := io.Copy(io.Discard, io.LimitReader(stream, 1))
	if err != nil {
		return fmt.Errorf("receive the %s: %w", last, err)
	}
	if extra > 0 {
		return fmt.Errorf("the stream goes on after the %s", last)
	}
	return nil
}

// activate makes the slot hold the image as systemd finds it and gives the UKI its name, the
// generation with the tries. Until the rename, nothing boots the slot.
func (n *Node) activate(ctx context.Context, h Header, slot storeslot.Slot, tmp string, generation uint64, tries int) (string, error) {
	if err := n.slots().Activate(ctx, slot, h.Store()); err != nil {
		return "", err
	}
	name := esp.CountedEntryName(h.ImageID, generation, tries)
	if err := esp.Name(n.ESP, tmp, name, n.Change); err != nil {
		return "", err
	}
	return name, nil
}

// slots finds the boot disk's two store slots, pairing verity and data partitions in disk order,
// and tells the one holding the running image from the other.
func slots(parts []gpt.Partition, running []byte) (booted, inactive storeslot.Slot, err error) {
	pairs, err := storeslot.StoreSlots("the boot disk", parts)
	if err != nil {
		return storeslot.Slot{}, storeslot.Slot{}, err
	}
	// The node found its store by these UUIDs; on two slots, which one it runs is unknown.
	if pairs[0].Holds(running) && pairs[1].Holds(running) {
		return storeslot.Slot{}, storeslot.Slot{}, errors.New("both slots carry the running store's partition UUIDs; which one the node runs is unknown")
	}
	dataUUID, verityUUID := storeslot.PartitionUUIDs(running)
	for i, s := range pairs {
		if !s.Holds(running) {
			continue
		}
		other := pairs[1-i]
		for _, uuid := range []string{other.Data.UUID, other.Verity.UUID} {
			if uuid == dataUUID || uuid == verityUUID {
				return storeslot.Slot{}, storeslot.Slot{}, fmt.Errorf("the inactive slot carries the running store's partition UUID %s; which partition the node runs is unknown", uuid)
			}
		}
		return s, other, nil
	}
	return storeslot.Slot{}, storeslot.Slot{}, errors.New("no slot of the boot disk holds the running image's store")
}
