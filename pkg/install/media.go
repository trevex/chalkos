package install

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/trevex/chalkos/pkg/storage/node"
)

// MediaRequest installs from installer media: the role image is streamed to a target disk.
type MediaRequest struct {
	Request
	// Target is the disk the role image is written to.
	Target storage.Ref
	// Image is the raw role image; ImageSize and ImageSHA256 describe it.
	Image       io.Reader
	ImageSize   int64
	ImageSHA256 []byte
	// SystemDefinitions are the role image's repart definitions of the system region, by file
	// name. The installer's own may differ: each role chooses its partition sizes.
	SystemDefinitions map[string]string
	// WipeDisk allows overwriting a target that carries data, including an installed node.
	WipeDisk bool
}

// Disk is a disk opened for writing the image.
type Disk interface {
	io.WriterAt
	Sync() error
	// RereadPartitions makes the kernel read the partition table the image brings.
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
	// lock keeps udev waiting until the kernel knows the image's partitions, so the two never race.
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

// GPT partition types of the store partitions a role image brings, besides its ESP.
const (
	typeUsrX86       = "8484680c-9521-48c6-9c11-b0720656f69e"
	typeUsrX86Verity = "77ff5f63-e7b6-4633-acf4-1565b864c0e6"
	typeUsrArm       = "b0e01050-ee5f-4390-949a-9101b17104e9"
	typeUsrArmVerity = "6e11a4e7-fbca-4ded-b9e9-e1a512bb664e"
)

// imagePartitions is how many partitions a role image brings: its ESP and store slot A's
// verity and data partitions. The system region adds slot B together with STATE.
const imagePartitions = 3

// mib is how much of a disk's start and end is zeroed to drop old partition tables, and how
// much of the image is held back until it is verified.
const mib = 1 << 20

// FromMedia writes the role image to the target disk and installs the node there. The UEFI
// boot entry it adds makes the next boot start from the target.
func (i *Installer) FromMedia(ctx context.Context, req MediaRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	if req.ImageSize <= 0 || len(req.ImageSHA256) != sha256.Size {
		return errors.New("the image's size and SHA-256 are required")
	}
	target, err := i.Host.Resolve(req.Target)
	if err != nil {
		return fmt.Errorf("resolve the target disk %s: %w", req.Target, err)
	}
	if err := i.refuseBootDisk(target); err != nil {
		return err
	}
	if uint64(req.ImageSize) > target.Identity.Size {
		return fmt.Errorf("the image has %d bytes, more than the target disk %s holds", req.ImageSize, target)
	}
	if !req.WipeDisk {
		if err := i.checkTarget(ctx, target); err != nil {
			return err
		}
	}

	// An install that a crashed chalkd left behind holds the target's STATE.
	if err := i.closeState(ctx); err != nil {
		return err
	}
	if err := i.writeImage(ctx, target, req); err != nil {
		return err
	}
	err = i.installOn(ctx, target, req.SystemDefinitions, req.Request)
	if err == nil {
		err = i.addBootEntry(ctx, target)
	}
	if err != nil {
		// Leave the target free, so the install can run again.
		if cerr := i.closeState(ctx); cerr != nil {
			return fmt.Errorf("%w; releasing the target's STATE failed too: %v", err, cerr)
		}
		return err
	}
	return nil
}

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

// checkTarget refuses a disk with data unless it is empty or carries only what an earlier
// attempt wrote before creating STATE: the image's ESP and store partitions. STATE, VAR and
// volume partitions have the same types in every cluster, so any of them may belong to an
// installed node. An ESP without a store may be a separate ESP disk or another system's boot
// disk.
func (i *Installer) checkTarget(ctx context.Context, disk storage.BlockDisk) error {
	out, err := i.Run.Run(ctx, "blkid", "-p", "-o", "export", disk.Device)
	var te *node.ToolError
	// blkid exits with 2 and says nothing when it finds nothing.
	if errors.As(err, &te) && te.Code == 2 && strings.TrimSpace(te.Stderr) == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("probe the target disk %s: %w", disk.Device, err)
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	if props["PTTYPE"] != "gpt" || props["TYPE"] != "" {
		return fmt.Errorf("the target disk %s carries data (%s); pass --wipe-disk to replace it", disk, strings.TrimSpace(props["PTTYPE"]+" "+props["TYPE"]))
	}
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	if len(table.Partitions) == 0 {
		return nil
	}
	store := map[string]bool{typeUsrX86: true, typeUsrX86Verity: true, typeUsrArm: true, typeUsrArmVerity: true}
	var found, all []string
	hasStore := false
	for _, p := range table.Partitions {
		desc := fmt.Sprintf("partition %d (%s)", p.Number, describe(p))
		all = append(all, desc)
		switch {
		case store[p.Type]:
			hasStore = true
		case p.Type != typeESP:
			found = append(found, desc)
		}
	}
	switch {
	case len(found) > 0:
		return fmt.Errorf("the target disk %s carries %s; pass --wipe-disk to replace it", disk, strings.Join(found, ", "))
	case !hasStore:
		return fmt.Errorf("the target disk %s carries %s and no chalkos store; pass --wipe-disk to replace it", disk, strings.Join(all, ", "))
	case len(table.Partitions) > imagePartitions:
		return fmt.Errorf("the target disk %s carries %d partitions, more than a role image's %d; pass --wipe-disk to replace it", disk, len(table.Partitions), imagePartitions)
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

// writeImage writes the streamed image to the disk and verifies its size and SHA-256. The
// image's first MiB, which holds its partition table, is written only after the rest is
// verified and on the disk, so nothing boots from or recognises a partial image; a short, long
// or corrupted image leaves the disk's first MiB zeroed.
func (i *Installer) writeImage(ctx context.Context, target storage.BlockDisk, req MediaRequest) (err error) {
	f, err := i.OpenDisk(ctx, target.Device)
	if errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("the target disk %s is in use: one of its partitions is mounted or held by another device", target)
	}
	if err != nil {
		return fmt.Errorf("open the target disk: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zero := make([]byte, mib)
	// The disk's old partition table must not describe the image's data while it is written,
	// and its old backup GPT at the end would otherwise outlive the image's partition table.
	if _, err := f.WriteAt(zero, 0); err != nil {
		return fmt.Errorf("clear the start of the target disk: %w", err)
	}
	if size := int64(target.Identity.Size); size >= 2*mib {
		if _, err := f.WriteAt(zero, size-mib); err != nil {
			return fmt.Errorf("clear the end of the target disk: %w", err)
		}
	}

	h := sha256.New()
	src := io.TeeReader(io.LimitReader(req.Image, req.ImageSize+1), h)
	head := make([]byte, min(req.ImageSize, mib))
	n, err := io.ReadFull(src, head)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		err = nil
	}
	if err == nil && n == len(head) {
		var rest int64
		rest, err = io.Copy(io.NewOffsetWriter(f, int64(len(head))), src)
		n += int(rest)
	}
	switch {
	case err != nil:
		err = fmt.Errorf("write the image: %w", err)
	case int64(n) != req.ImageSize:
		err = fmt.Errorf("the image stream had %d bytes, but the image has %d", n, req.ImageSize)
	case !bytes.Equal(h.Sum(nil), req.ImageSHA256):
		err = fmt.Errorf("the image's SHA-256 is %x, want %x", h.Sum(nil), req.ImageSHA256)
	default:
		if err := f.Sync(); err != nil {
			return err
		}
		if _, err := f.WriteAt(head, 0); err != nil {
			return fmt.Errorf("write the image's partition table: %w", err)
		}
		if err := f.Sync(); err != nil {
			return err
		}
		// The kernel still knows the partitions the disk had before.
		return f.RereadPartitions()
	}
	if _, zerr := f.WriteAt(zero, 0); zerr != nil {
		return fmt.Errorf("%w; clearing the start of the target disk failed too: %v", err, zerr)
	}
	f.Sync()
	return err
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

// addBootEntry adds a UEFI boot entry for the target's boot loader and makes it the next boot.
// efibootmgr puts it first in the boot order too, so the target keeps booting afterwards.
func (i *Installer) addBootEntry(ctx context.Context, disk storage.BlockDisk) error {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	esp, err := table.findType(typeESP)
	if err != nil {
		return err
	}
	if _, err := i.Run.Run(ctx, "efibootmgr", "--create", "--disk", disk.Device, "--part", strconv.Itoa(esp.Number), "--label", bootLabel, "--loader", i.Loader); err != nil {
		return fmt.Errorf("add a UEFI boot entry: %w", err)
	}
	// The ESP's partition UUID was just randomised, so it names the new entry alone.
	out, err := i.Run.Run(ctx, "efibootmgr", "--verbose")
	if err != nil {
		return fmt.Errorf("list UEFI boot entries: %w", err)
	}
	entries := parseBootEntries(string(out))
	var created *bootEntry
	for n, e := range entries {
		if e.partUUID == esp.UUID {
			created = &entries[n]
			break
		}
	}
	if created == nil {
		return fmt.Errorf("the UEFI boot entry for the ESP %s is missing after creating it", esp.UUID)
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
