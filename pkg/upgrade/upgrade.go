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
package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/verity"
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

// Header describes the image an upgrade installs, whose store, hash tree and UKI follow it.
type Header struct {
	ImageID, Version, Cluster, Role string
	RootHash                        []byte
	StoreSize, VeritySize, UKISize  int64
	StoreSHA256, VeritySHA256       []byte
	UKISHA256                       []byte
}

// VersionPattern is what an image version may be: what a GPT label holds after "store-verity_"
// and what systemd-boot keeps unchanged in an entry's ID.
var VersionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.~^-]{0,22}$`)

// maxUKI bounds the UKI an upgrade takes; it must fit the ESP beside the running one.
const maxUKI = 512 << 20

func (h Header) validate() error {
	switch {
	case !VersionPattern.MatchString(h.Version):
		return fmt.Errorf("the version %q is not 1 to 23 characters of a-z, 0-9, '.', '~', '^' and '-'", h.Version)
	case h.ImageID == "" || h.Cluster == "" || h.Role == "":
		return errors.New("the image's ID, cluster and role are required")
	case len(h.RootHash) != sha256.Size:
		return errors.New("the root hash is not a SHA-256")
	case len(h.StoreSHA256) != sha256.Size || len(h.VeritySHA256) != sha256.Size || len(h.UKISHA256) != sha256.Size:
		return errors.New("the SHA-256 sums of the store, the hash tree and the UKI are required")
	case h.StoreSize <= 0 || h.VeritySize <= 0 || h.UKISize <= 0 || h.UKISize > maxUKI:
		return errors.New("the sizes of the store, the hash tree and the UKI are required")
	}
	return nil
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
	if err := h.validate(); err != nil {
		return Result{}, err
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
	unlock := n.lockTable()
	parts, err := n.readTable(ctx)
	if err != nil {
		unlock()
		return Result{}, err
	}
	_, inactive, err := slots(parts, running)
	unlock()
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
	wrote, err := n.write(h, inactive, stream)
	if err != nil {
		return Result{}, err
	}
	tmp, tries, err := n.receiveUKI(h, stream)
	if err != nil {
		return Result{}, err
	}
	entry, err := n.activate(ctx, h, inactive, tmp, tries)
	if err != nil {
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

// lockTable takes the partition table's lock, when there is one, and returns its release.
func (n *Node) lockTable() func() {
	if n.TableLock == nil {
		return func() {}
	}
	n.TableLock.Lock()
	return n.TableLock.Unlock
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
	if err := n.setSlot(ctx, slot, randomUUID(), emptyLabel, randomUUID(), emptyLabel); err != nil {
		return err
	}
	log.Printf("retired the slot of partitions %d and %d", slot.Verity.Number, slot.Data.Number)
	return nil
}

// write writes the store and its hash tree into the slot and checks them against the root hash.
// A slot that holds them already is left as it is, and the stream's copies are skipped.
func (n *Node) write(h Header, slot Slot, stream io.Reader) (bool, error) {
	if n.holds(slot, h) {
		log.Printf("the slot of partitions %d and %d holds the store of %s already", slot.Verity.Number, slot.Data.Number, h.Version)
		if _, err := io.CopyN(io.Discard, stream, h.StoreSize+h.VeritySize); err != nil {
			return false, fmt.Errorf("receive the image: %w", err)
		}
		return false, nil
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
		if err := n.change("write the " + part.what); err != nil {
			return false, err
		}
		if err := n.copyInto(part.p, stream, part.size, part.sum, part.what); err != nil {
			return false, err
		}
	}
	sb, err := n.verify(slot, h.RootHash)
	if err != nil {
		return false, fmt.Errorf("the written store: %w", err)
	}
	if sb.DataSize() != h.StoreSize || sb.HashSize() != h.VeritySize {
		return false, fmt.Errorf("the hash tree covers %d bytes of store in %d bytes, but the image has %d and %d", sb.DataSize(), sb.HashSize(), h.StoreSize, h.VeritySize)
	}
	return true, nil
}

// holds reports whether the slot holds the image's store and hash tree. Only a hash tree with
// the image's root hash is read in full.
func (n *Node) holds(slot Slot, h Header) bool {
	hash, err := n.OpenPartition(slot.Verity, false)
	if err != nil {
		return false
	}
	sb, root, err := verity.Root(hash)
	hash.Close()
	if err != nil || !bytes.Equal(root, h.RootHash) || sb.DataSize() != h.StoreSize || sb.HashSize() != h.VeritySize {
		return false
	}
	_, err = n.verify(slot, h.RootHash)
	return err == nil
}

func (n *Node) verify(slot Slot, root []byte) (verity.Superblock, error) {
	data, err := n.OpenPartition(slot.Data, false)
	if err != nil {
		return verity.Superblock{}, err
	}
	defer data.Close()
	hash, err := n.OpenPartition(slot.Verity, false)
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
func (n *Node) copyInto(p Partition, stream io.Reader, size int64, sum []byte, what string) error {
	f, err := n.OpenPartition(p, true)
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

// receiveUKI writes the UKI to the ESP under a name systemd-boot ignores and checks it. It
// returns the file and the tries the image asks for.
func (n *Node) receiveUKI(h Header, stream io.Reader) (string, int, error) {
	if err := n.change("write the UKI"); err != nil {
		return "", 0, err
	}
	dir := filepath.Join(n.ESP, linuxDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	tmp := filepath.Join(dir, tempPrefix+h.Version+".efi")
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, fmt.Errorf("write the UKI to the ESP: %w", err)
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(f, sum), stream, h.UKISize); err != nil {
		return "", 0, fmt.Errorf("receive the UKI: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", 0, fmt.Errorf("write the UKI to the ESP: %w", err)
	}
	if got := sum.Sum(nil); !bytes.Equal(got, h.UKISHA256) {
		return "", 0, fmt.Errorf("the UKI's SHA-256 is %x, want %x", got, h.UKISHA256)
	}
	img, err := uki.Read(f)
	if err != nil {
		return "", 0, err
	}
	for _, field := range []struct{ name, uki, header string }{
		{"image ID", img.ID(), h.ImageID},
		{"version", img.Version(), h.Version},
		{"cluster", img.Cluster(), h.Cluster},
		{"role", img.Role(), h.Role},
	} {
		if field.uki != field.header {
			return "", 0, fmt.Errorf("the UKI's %s is %q, but the upgrade names %q", field.name, field.uki, field.header)
		}
	}
	if hash, err := img.UsrHash(); err != nil || !bytes.Equal(hash, h.RootHash) {
		return "", 0, errors.New("the UKI boots another store than the upgrade carries")
	}
	tries, err := strconv.Atoi(img.BootTries())
	if err != nil || tries < 1 {
		return "", 0, fmt.Errorf("the UKI's boot tries %q are not a positive number", img.BootTries())
	}
	db, dbx, enforced, err := secureBootDatabases(n.EFIVars)
	if err != nil {
		return "", 0, err
	}
	if enforced {
		if err := uki.VerifySignature(f, h.UKISize, db, dbx); err != nil {
			return "", 0, fmt.Errorf("Secure Boot would refuse the UKI: %w", err)
		}
	}
	return tmp, tries, f.Close()
}

// activate makes the slot hold the image as systemd finds it, prefers its entry in systemd-boot,
// and gives the UKI its name. Until the rename, nothing boots the slot.
func (n *Node) activate(ctx context.Context, h Header, slot Slot, tmp string, tries int) (string, error) {
	dataUUID, verityUUID := PartitionUUIDs(h.RootHash)
	if err := n.setSlot(ctx, slot, verityUUID, "store-verity_"+h.Version, dataUUID, "store_"+h.Version); err != nil {
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

// setSlot gives the slot's verity and data partitions UUIDs and labels. It reads the partition
// table again under its lock, and refuses partitions that moved since the slot was found.
func (n *Node) setSlot(ctx context.Context, slot Slot, verityUUID, verityLabel, dataUUID, dataLabel string) error {
	unlock := n.lockTable()
	defer unlock()
	parts, err := n.readTable(ctx)
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
			return fmt.Errorf("partition %d of the inactive slot changed during the upgrade", set.p.Number)
		}
		if err := n.setPartition(ctx, p, set.uuid, set.label); err != nil {
			return err
		}
	}
	return nil
}
