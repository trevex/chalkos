package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"

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
	// WipeDisk allows overwriting a target that carries data other than chalkos partitions.
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

func openExclusive(path string) (Disk, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_EXCL, 0)
	if err != nil {
		return nil, err
	}
	// udev probes a disk, and rereads its partition table, when it is closed after writing; the
	// lock keeps udev waiting until the kernel knows the image's partitions, so the two never race.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return blockDevice{f}, nil
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

// mib is how much of a disk's start and end is zeroed to drop old partition tables.
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
	if boot, err := i.Host.ResolvePath(i.BootDisk); err == nil && boot.Name == target.Name {
		return fmt.Errorf("the target disk %s is the disk the installer runs from", target)
	}
	if uint64(req.ImageSize) > target.Identity.Size {
		return fmt.Errorf("the image has %d bytes, more than the target disk %s holds", req.ImageSize, target)
	}
	if !req.WipeDisk {
		if err := i.checkTarget(ctx, target, req.Section); err != nil {
			return err
		}
	}

	if err := i.writeImage(target, req); err != nil {
		return err
	}
	if err := i.installOn(ctx, target, req.SystemDefinitions, req.Request); err != nil {
		return err
	}
	return i.addBootEntry(ctx, target)
}

// checkTarget refuses a disk with data unless it carries only partitions of a chalkos node:
// the image's ESP and store, STATE, and the volumes of the node's system disk.
func (i *Installer) checkTarget(ctx context.Context, disk storage.BlockDisk, section storage.Section) error {
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
	if props["PTTYPE"] != "gpt" {
		return fmt.Errorf("the target disk %s carries data (%s); use --wipe-disk to overwrite it", disk, strings.TrimSpace(props["PTTYPE"]+" "+props["TYPE"]))
	}
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	allowed := section.Disks[storage.SystemDisk].PartitionTypes()
	for _, t := range []string{typeESP, typeUsrX86, typeUsrX86Verity, typeUsrArm, typeUsrArmVerity, storage.PartitionType(stateLabel)} {
		allowed[t] = true
	}
	var foreign []string
	for _, p := range table.Partitions {
		if !allowed[p.Type] {
			foreign = append(foreign, fmt.Sprintf("%s (type %s, label %q)", p.Node, p.Type, p.Name))
		}
	}
	if len(foreign) > 0 {
		return fmt.Errorf("the target disk %s carries partitions chalkos did not create: %s; use --wipe-disk to overwrite it", disk, strings.Join(foreign, ", "))
	}
	return nil
}

// writeImage writes the streamed image to the start of the disk and verifies its SHA-256. A
// short, long or corrupted image leaves the disk's first MiB zeroed, so nothing boots from or
// recognises a partial image.
func (i *Installer) writeImage(target storage.BlockDisk, req MediaRequest) (err error) {
	f, err := i.OpenDisk(target.Device)
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
	// The disk's old backup GPT at its end would otherwise outlive the image's partition table.
	if size := int64(target.Identity.Size); size >= 2*mib {
		if _, err := f.WriteAt(zero, size-mib); err != nil {
			return fmt.Errorf("clear the end of the target disk: %w", err)
		}
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(io.NewOffsetWriter(f, 0), h), io.LimitReader(req.Image, req.ImageSize+1))
	switch {
	case err != nil:
		err = fmt.Errorf("write the image: %w", err)
	case n != req.ImageSize:
		err = fmt.Errorf("the image stream had %d bytes, but the image has %d", n, req.ImageSize)
	case !bytes.Equal(h.Sum(nil), req.ImageSHA256):
		err = fmt.Errorf("the image's SHA-256 is %x, want %x", h.Sum(nil), req.ImageSHA256)
	default:
		if err := f.Sync(); err != nil {
			return err
		}
		// The kernel still knows the partitions the disk had before.
		return f.RereadPartitions()
	}
	if _, zerr := f.WriteAt(zero, 0); zerr != nil {
		return fmt.Errorf("%w; clearing the partial image failed too: %v", err, zerr)
	}
	f.Sync()
	return err
}

var bootEntryRE = regexp.MustCompile(`^Boot([0-9A-Fa-f]{4})\*?\s`)

// addBootEntry adds a UEFI boot entry for the target's boot loader and makes it the next boot.
func (i *Installer) addBootEntry(ctx context.Context, disk storage.BlockDisk) error {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	esp, err := table.findType(typeESP)
	if err != nil {
		return err
	}
	if _, err := i.Run.Run(ctx, "efibootmgr", "--create", "--disk", disk.Device, "--part", strconv.Itoa(esp.Number), "--label", "chalkos", "--loader", i.Loader); err != nil {
		return fmt.Errorf("add a UEFI boot entry: %w", err)
	}
	// The ESP's partition UUID was just randomised, so it names the new entry alone.
	out, err := i.Run.Run(ctx, "efibootmgr", "--verbose")
	if err != nil {
		return fmt.Errorf("list UEFI boot entries: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		m := bootEntryRE.FindStringSubmatch(line)
		if m != nil && strings.Contains(strings.ToLower(line), esp.UUID) {
			if _, err := i.Run.Run(ctx, "efibootmgr", "--bootnext", m[1]); err != nil {
				return fmt.Errorf("boot the target next: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("the UEFI boot entry for the ESP %s is missing after creating it", esp.UUID)
}
