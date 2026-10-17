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
//     as chalkd checks the node's config against the image's schemas.
//  4. Activate: give the slot the partition UUIDs systemd derives from the root hash and the
//     labels naming it, and rename the UKI to <id>_<generation>+<tries>.efi, one generation above
//     the image's highest on the ESP.
//
// An image is its store's root hash; nothing compares versions. systemd-boot orders chalkos's
// entries by their IDs alone, the highest generation first, as every UKI's os-release names the
// same image ID and an empty IMAGE_VERSION: the image installed last boots next, an older one's
// too, and the boot falls back to the entry before once its tries are used up.
//
// The running slot and its UKI are never written.
//
// The installer shares the slot writer, SlotWriter, and the checks of an image's UKI and boot
// loader, CheckUKI and CheckSecureBoot: it writes slot A of the disk it installs a node onto.
package upgrade

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/uki"
)

// Node is the running node an upgrade changes. Tests point it at a disk image and directories.
type Node struct {
	Run node.Runner
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
	OpenPartition func(p Partition, write bool) (PartitionFile, error)
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
	running, err := n.running()
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
	all, err := Entries(n.ESP)
	if err != nil {
		return Result{}, err
	}
	booted, err := Booted(all, n.EFIVars, running)
	if err != nil {
		return Result{}, err
	}
	// The new entry boots before the image's entries an upgrade keeps, the booted one and those
	// booting the running store, only while their names order them by the generation they give, as
	// chalkd names them: from 1 on, without leading zeros. Other names are placed by hand or
	// corrupted.
	for _, e := range all {
		if retired(e, h.ImageID, booted, running) || !ofImage(e, h.ImageID) && e.ID != booted.ID {
			continue
		}
		if _, named, _ := nameGeneration(e.ID, h.ImageID); (named || e.ID == booted.ID) && entryGeneration(e.ID, h.ImageID) == 0 {
			return Result{}, fmt.Errorf("%s on the ESP is not named %s_<generation>.efi with a generation from 1 on and no leading zeros, so the next generation might not boot before it; reinstall the node, or rename the entry", e.File, h.ImageID)
		}
	}
	// The UKI is renamed last, to the image's next generation, above the generation any name on the
	// ESP gives, so it replaces no UKI and shares no entry's ID.
	generation, err := nextGeneration(all, h.ImageID)
	if err != nil {
		return Result{}, err
	}
	// The image boots next already, as after an upgrade without a reboot.
	if staged, ok := stagedEntry(all, h.ImageID, booted); ok && bytes.Equal(staged.RootHash, h.RootHash) && inactive.Holds(h.RootHash) {
		return Result{AlreadyStaged: true, Entry: staged.File}, nil
	}
	// UKIs of other images on the ESP, such as a rescue system's, are left alone; a UKI is of the
	// image its os-release names, or one that cannot be read of the image its name gives.
	entries := slices.DeleteFunc(slices.Clone(all), func(e Entry) bool { return !ofImage(e, h.ImageID) })
	// Until the running image's boot is found good, the image it falls back to must stay.
	if booted.TriesLeft >= 0 {
		for _, e := range entries {
			// A UKI that cannot be read boots no image to fall back to.
			if e.ID != booted.ID && !e.Bad() && e.RootHash != nil && !bytes.Equal(e.RootHash, running) {
				return Result{}, fmt.Errorf("the boot of %s has not been found healthy yet; upgrade once it is, as the upgrade replaces %s, which the node falls back to", booted.File, e.File)
			}
		}
	}
	if err := inactive.Fits(h); err != nil {
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
	wrote, err := w.Write(h, inactive, stream)
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

// running is the root hash of the store the node runs.
func (n *Node) running() ([]byte, error) {
	cmdline, err := os.ReadFile(n.Cmdline)
	if err != nil {
		return nil, fmt.Errorf("read the kernel command line: %w", err)
	}
	hash, err := uki.UsrHash(string(cmdline))
	if err != nil {
		return nil, fmt.Errorf("the running store: %w", err)
	}
	return hash, nil
}

// slots is the slot writer of the node's boot disk.
func (n *Node) slots() *SlotWriter {
	return &SlotWriter{Run: n.Run, Disk: n.Disk, OpenPartition: n.OpenPartition, TableLock: n.TableLock, LockWait: n.LockWait, Change: n.Change}
}

// ofImage reports whether the UKI is of the image: its os-release names the image or, when it
// cannot be read, its name gives a generation of the image.
func ofImage(e Entry, imageID string) bool {
	if e.ImageID == "" {
		_, ok, _ := nameGeneration(e.ID, imageID)
		return ok
	}
	return e.ImageID == imageID
}

// retired reports whether the retirement removes the UKI: one of the image that neither the node
// booted nor boots the running store.
func retired(e Entry, imageID string, booted Entry, running []byte) bool {
	return ofImage(e, imageID) && e.ID != booted.ID && !bytes.Equal(e.RootHash, running)
}

// retire removes every UKI of the image but those booting the running store, along with UKIs an
// upgrade left half written, and then makes the inactive slot one nothing finds.
func (n *Node) retire(ctx context.Context, entries []Entry, imageID string, booted Entry, running []byte, slot Slot) error {
	dir := filepath.Join(n.ESP, linuxDir)
	var remove []string
	files, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list the UKIs on the ESP: %w", err)
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), tempPrefix) {
			remove = append(remove, f.Name())
		}
	}
	for _, e := range entries {
		if retired(e, imageID, booted, running) {
			remove = append(remove, e.File)
		}
	}
	for _, name := range remove {
		if err := n.change("remove " + name); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("remove %s from the ESP: %w", name, err)
		}
	}
	if len(remove) > 0 {
		if err := syncESP(n.ESP); err != nil {
			return err
		}
	}
	return n.slots().Retire(ctx, slot)
}

// receiveUKI writes the UKI to the ESP under a name systemd-boot ignores and checks it. It
// returns the file, also when it fails once the file may exist, and the tries the image asks for.
func (n *Node) receiveUKI(h Header, stream io.Reader) (string, int, error) {
	if err := n.change("write the UKI"); err != nil {
		return "", 0, err
	}
	dir := filepath.Join(n.ESP, linuxDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	tmp := filepath.Join(dir, tempPrefix+hex.EncodeToString(h.RootHash)+".efi")
	tries, err := ReceiveUKI(tmp, h, stream, n.EFIVars)
	return tmp, tries, err
}

// activate makes the slot hold the image as systemd finds it and gives the UKI its name, the
// generation with the tries. Until the rename, nothing boots the slot.
func (n *Node) activate(ctx context.Context, h Header, slot Slot, tmp string, generation uint64, tries int) (string, error) {
	if err := n.slots().Activate(ctx, slot, h); err != nil {
		return "", err
	}
	name := countedEntryName(h.ImageID, generation, tries)
	if err := n.change("name the UKI " + name); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(n.ESP, linuxDir, name)); err != nil {
		return "", fmt.Errorf("name the UKI %s: %w", name, err)
	}
	if err := syncESP(n.ESP); err != nil {
		return "", err
	}
	return name, nil
}
