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
//     the node's image, cluster and role, and carry a signature of the firmware's db when Secure
//     Boot is on.
//  4. Activate: give the slot the partition UUIDs systemd derives from the root hash and the
//     labels naming the version, prefer the new entry in systemd-boot, and rename the UKI to
//     <id>_<version>+<tries>.efi.
//
// The running slot and its UKI are never written.
//
// The installer shares the slot writer, SlotWriter, and the checks of an image's UKI and boot
// loader, CheckUKI and CheckSecureBoot: it writes slot A of the disk it installs a node onto.
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
	running, err := n.running()
	if err != nil {
		return Result{}, err
	}
	for _, field := range []struct{ name, image, node string }{
		{"image ID", h.ImageID, n.OSRelease["IMAGE_ID"]},
		{"cluster", h.Cluster, n.OSRelease["CHALKOS_CLUSTER"]},
		{"role", h.Role, n.OSRelease["CHALKOS_ROLE"]},
	} {
		if field.node == "" {
			return Result{}, fmt.Errorf("the running image names no %s", field.name)
		}
		if field.image != field.node {
			return Result{}, fmt.Errorf("the image's %s is %s, the node's %s", field.name, field.image, field.node)
		}
	}
	version := n.OSRelease["IMAGE_VERSION"]
	if h.Version == version {
		if bytes.Equal(h.RootHash, running) {
			return Result{AlreadyInstalled: true}, nil
		}
		return Result{}, fmt.Errorf("the node runs version %s with another root hash; build the image with a new version", version)
	}
	// Its partitions would be the running ones, by the UUIDs derived from the root hash.
	if bytes.Equal(h.RootHash, running) {
		return Result{}, fmt.Errorf("the node runs this store as version %s, not %s", version, h.Version)
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
	// UKIs of other images on the ESP, such as a rescue system's, are left alone; a UKI is of the
	// image its os-release names.
	entries := slices.DeleteFunc(slices.Clone(all), func(e Entry) bool { return e.ImageID != h.ImageID })
	// The UKI is renamed last; it must not replace a UKI that stays or share its entry ID.
	id := entryID(h.ImageID, h.Version)
	for _, e := range all {
		if e.ID == id && !retired(e, h.ImageID, booted, running) {
			return Result{}, fmt.Errorf("%s on the ESP has the entry ID %s, which the image needs; remove it, or build the image with a new version", e.File, id)
		}
	}
	for _, e := range entries {
		if e.Version == h.Version && !bytes.Equal(e.RootHash, h.RootHash) {
			return Result{}, fmt.Errorf("the ESP holds version %s with another root hash in %s; build the image with a new version", h.Version, e.File)
		}
	}
	// Until the running image's boot is found good, the image it falls back to must stay.
	if booted.TriesLeft >= 0 {
		for _, e := range entries {
			if e.ID != booted.ID && !e.Bad() && !bytes.Equal(e.RootHash, running) {
				return Result{}, fmt.Errorf("the boot of %s has not been found healthy yet; upgrade once it is, as the upgrade replaces %s, which the node falls back to", booted.File, e.File)
			}
		}
	}
	if h.StoreSize > inactive.Data.Bytes() || h.VeritySize > inactive.Verity.Bytes() {
		return Result{}, fmt.Errorf("the image's store of %d bytes and hash tree of %d bytes do not fit the slot's partitions of %d and %d bytes", h.StoreSize, h.VeritySize, inactive.Data.Bytes(), inactive.Verity.Bytes())
	}

	// From here on the steps change the disk: a client that goes away must not kill sfdisk while it
	// writes the partition table. Its stream ends, which stops the upgrade between steps.
	ctx = context.WithoutCancel(ctx)
	if err := n.retire(ctx, all, h.ImageID, booted, running, inactive); err != nil {
		return Result{}, err
	}
	wrote, err := w.Write(h, inactive, stream)
	if err != nil {
		return Result{}, err
	}
	tmp, tries, err := n.receiveUKI(h, stream)
	var entry string
	if err == nil {
		entry, err = n.activate(ctx, h, inactive, tmp, tries)
	}
	if err != nil {
		// A UKI under its temporary name boots nothing, but takes space on the ESP that the next
		// upgrade needs; should this fail, the next retirement removes it.
		if tmp != "" {
			if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				log.Printf("remove %s: %v", tmp, rerr)
			}
		}
		return Result{}, err
	}
	log.Printf("installed %s %s into partitions %d and %d; it boots next as %s", h.ImageID, h.Version, inactive.Verity.Number, inactive.Data.Number, entry)
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

// readTable reads the boot disk's GPT.
func (n *Node) readTable(ctx context.Context) ([]Partition, error) {
	return n.slots().ReadTable(ctx)
}

// retired reports whether the retirement removes the UKI: one of the image that neither the node
// booted nor boots the running store.
func retired(e Entry, imageID string, booted Entry, running []byte) bool {
	return e.ImageID == imageID && e.ID != booted.ID && !bytes.Equal(e.RootHash, running)
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
	tmp := filepath.Join(dir, tempPrefix+h.Version+".efi")
	tries, err := ReceiveUKI(tmp, h, stream, n.EFIVars)
	return tmp, tries, err
}

// activate makes the slot hold the image as systemd finds it, prefers its entry in systemd-boot,
// and gives the UKI its name. Until the rename, nothing boots the slot.
func (n *Node) activate(ctx context.Context, h Header, slot Slot, tmp string, tries int) (string, error) {
	if err := n.slots().Activate(ctx, slot, h); err != nil {
		return "", err
	}
	// systemd-boot boots the newest version first; preferring the entry boots an older one, and
	// skips it once its tries are used up.
	id := entryID(h.ImageID, h.Version)
	if err := n.change("prefer " + id); err != nil {
		return "", err
	}
	if err := setLoaderString(n.EFIVars, "LoaderEntryPreferred", id, func() error {
		return n.change("write LoaderEntryPreferred")
	}); err != nil {
		return "", err
	}
	name := entryName(h.ImageID, h.Version, tries)
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
