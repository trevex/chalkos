package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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

// diskLockWait is how long openExclusive waits for a disk another process keeps locked, as sfdisk
// does: the install holds chalkd's lock meanwhile.
var diskLockWait = 30 * time.Second

func openExclusive(ctx context.Context, path string) (Disk, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_EXCL, 0)
	if err != nil {
		return nil, err
	}
	// udev probes a disk, and rereads its partition table, when it is closed after writing; the
	// lock keeps udev waiting until the kernel dropped the old partitions, so the two never race.
	deadline := time.Now().Add(diskLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return blockDevice{f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("another program kept the target disk %s locked for %v", path, diskLockWait)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("the target disk is locked by another process: %s: %w", path, ctx.Err())
		case <-time.After(min(lockRetry, time.Until(deadline))):
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

// mib is how much of a disk's start and end is zeroed to drop its partition tables.
const mib = 1 << 20

// wipe drops the target's partition tables and the signatures at its start: both GPT copies are
// zeroed, as is the disk's first MiB, where most file systems and volume managers keep theirs.
func (i *Installer) wipe(ctx context.Context, target storage.BlockDisk) (err error) {
	if err := i.change("wipe the target disk"); err != nil {
		return err
	}
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
	if _, err := f.WriteAt(zero, 0); err != nil {
		return fmt.Errorf("clear the start of the target disk: %w", err)
	}
	if size := int64(target.Identity.Size); size >= 2*mib {
		if _, err := f.WriteAt(zero, size-mib); err != nil {
			return fmt.Errorf("clear the end of the target disk: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	// The kernel still knows the partitions the disk had.
	return f.RereadPartitions()
}
