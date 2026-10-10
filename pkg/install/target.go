package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/trevex/chalkos/pkg/storage"
)

// Disk is a disk opened for wiping.
type Disk interface {
	io.WriterAt
	Sync() error
	// RereadPartitions makes the kernel drop the partitions the disk had.
	RereadPartitions() error
	Close() error
}

// blockDevice is a whole disk opened exclusively: the kernel refuses while any of its
// partitions is mounted or held by a device mapper or RAID device.
type blockDevice struct{ *os.File }

// lockRetry is how often openExclusive tries again to lock a disk another process holds.
const lockRetry = 100 * time.Millisecond

func openExclusive(ctx context.Context, path string) (Disk, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_EXCL, 0)
	if err != nil {
		return nil, err
	}
	// udev probes a disk, and rereads its partition table, when it is closed after writing; the
	// lock keeps udev waiting until the kernel dropped the old partitions, so the two never race.
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return blockDevice{f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("the target disk is locked by another process: %s: %w", path, ctx.Err())
		case <-time.After(lockRetry):
		}
	}
}

// blkrrpart is the BLKRRPART ioctl. The kernel allows it to the disk's exclusive opener.
const blkrrpart = 0x125f

func (d blockDevice) RereadPartitions() error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, d.Fd(), blkrrpart, 0); errno != 0 {
		return fmt.Errorf("reread the partition table of %s: %w", d.Name(), errno)
	}
	return nil
}

// GPT partition types of the store partitions.
const (
	typeUsrX86       = "8484680c-9521-48c6-9c11-b0720656f69e"
	typeUsrX86Verity = "77ff5f63-e7b6-4633-acf4-1565b864c0e6"
	typeUsrArm       = "b0e01050-ee5f-4390-949a-9101b17104e9"
	typeUsrArmVerity = "6e11a4e7-fbca-4ded-b9e9-e1a512bb664e"
)

// mib is how much of a disk's start and end is zeroed to drop its partition tables.
const mib = 1 << 20

// refuseBootDisk refuses the disk the installer runs from. Only a missing boot-disk link lets
// the install go ahead without knowing that disk; any other failure to resolve it refuses.
func (i *Installer) refuseBootDisk(target storage.BlockDisk) error {
	boot, err := i.Host.ResolvePath(i.BootDisk)
	if err != nil {
		link := filepath.Join(i.Host.DevRoot, strings.TrimPrefix(i.BootDisk, "/dev/"))
		if _, lerr := os.Lstat(link); errors.Is(lerr, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("find the disk the installer runs from: %w", err)
	}
	if boot.Name == target.Name {
		return fmt.Errorf("the target disk %s is the disk the installer runs from", target)
	}
	return nil
}

// describe names a partition found on the target.
func describe(p partition) string {
	switch p.Type {
	case typeESP:
		return "ESP"
	case typeUsrX86, typeUsrArm:
		return "chalkos store"
	case typeUsrX86Verity, typeUsrArmVerity:
		return "chalkos store verity"
	}
	if p.Type != storage.PartitionType(p.Name) {
		return fmt.Sprintf("type %s, label %q", p.Type, p.Name)
	}
	switch p.Name {
	case stateLabel:
		return "chalkos STATE"
	case "var":
		return "chalkos VAR"
	default:
		return "chalkos volume " + p.Name
	}
}

var (
	bootEntryRE = regexp.MustCompile(`^Boot([0-9A-Fa-f]{4})\*?\s`)
	gptPartRE   = regexp.MustCompile(`HD\([0-9a-fA-Fx]+,GPT,([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)
)

// bootLabel is the label of the UEFI boot entries the installer adds.
const bootLabel = "chalkos"

type bootEntry struct {
	num, label string
	// partUUID is the GPT partition UUID the entry boots from, lower case; empty when the entry
	// does not name one.
	partUUID string
}

// parseBootEntries reads the entries of efibootmgr --verbose. A label is known only when a tab
// ends it.
func parseBootEntries(out string) []bootEntry {
	var entries []bootEntry
	for _, line := range strings.Split(out, "\n") {
		m := bootEntryRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		e := bootEntry{num: m[1]}
		if label, _, ok := strings.Cut(line[len(m[0]):], "\t"); ok {
			e.label = strings.TrimSpace(label)
		}
		if p := gptPartRE.FindStringSubmatch(line); p != nil {
			e.partUUID = strings.ToLower(p[1])
		}
		entries = append(entries, e)
	}
	return entries
}

// addBootEntry adds a UEFI boot entry for the target's boot loader, unless an earlier attempt
// added it, and makes it the next boot. efibootmgr puts it first in the boot order too, so the
// target keeps booting afterwards.
func (i *Installer) addBootEntry(ctx context.Context, disk storage.BlockDisk) error {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	esp, err := table.findType(typeESP)
	if err != nil {
		return err
	}
	// The ESP's partition UUID is random, so it names the target's entries alone.
	find := func() (*bootEntry, []bootEntry, error) {
		out, err := i.Run.Run(ctx, "efibootmgr", "--verbose")
		if err != nil {
			return nil, nil, fmt.Errorf("list UEFI boot entries: %w", err)
		}
		entries := parseBootEntries(string(out))
		for n, e := range entries {
			if e.label == bootLabel && e.partUUID == esp.UUID {
				return &entries[n], entries, nil
			}
		}
		return nil, entries, nil
	}
	created, entries, err := find()
	if err != nil {
		return err
	}
	if created == nil {
		if err := i.change("add the UEFI boot entry"); err != nil {
			return err
		}
		if _, err := i.Run.Run(ctx, "efibootmgr", "--create", "--disk", disk.Device, "--part", strconv.Itoa(esp.Number), "--label", bootLabel, "--loader", i.Loader); err != nil {
			return fmt.Errorf("add a UEFI boot entry: %w", err)
		}
		if created, entries, err = find(); err != nil {
			return err
		}
		if created == nil {
			return fmt.Errorf("the UEFI boot entry for the ESP %s is missing after creating it", esp.UUID)
		}
	}
	if err := i.change("boot the target next"); err != nil {
		return err
	}
	if _, err := i.Run.Run(ctx, "efibootmgr", "--bootnext", created.num); err != nil {
		return fmt.Errorf("boot the target next: %w", err)
	}
	i.deleteStaleBootEntries(ctx, entries, created.num)
	return nil
}

// deleteStaleBootEntries deletes the entries earlier installs added whose ESP is on no disk any
// more, such as one whose image was written again. An entry whose ESP is present, or that
// cannot be told to be absent, stays. The node is installed already, so failures are logged.
func (i *Installer) deleteStaleBootEntries(ctx context.Context, entries []bootEntry, keep string) {
	for _, e := range entries {
		if e.num == keep || e.label != bootLabel || e.partUUID == "" {
			continue
		}
		_, err := os.Lstat(filepath.Join(i.Host.DevRoot, "disk", "by-partuuid", e.partUUID))
		if !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if _, err := i.Run.Run(ctx, "efibootmgr", "--delete-bootnum", "--bootnum", e.num); err != nil {
			log.Printf("delete the stale UEFI boot entry Boot%s: %v", e.num, err)
		}
	}
}
