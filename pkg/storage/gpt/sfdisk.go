package gpt

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/tool"
)

// DefaultLockWait is how long Sfdisk waits for a disk's lock when LockWait is zero.
const DefaultLockWait = 30 * time.Second

// lockRetry is how often Sfdisk tries again to take a disk's lock another program holds.
const lockRetry = 250 * time.Millisecond

// Sfdisk runs sfdisk on one disk with the disk's BSD lock, as systemd-repart takes it and as udev
// waits for before it probes the disk. sfdisk takes the lock before it reads or writes anything.
// While another program holds it, sfdisk runs again for a bounded time instead of waiting
// unbounded: its callers hold locks of their own, such as chalkd's, that other requests wait for.
type Sfdisk struct {
	Run tool.Runner
	// Disk is the disk's device.
	Disk string
	// LockWait is how long a run waits for the disk's lock; DefaultLockWait when zero.
	LockWait time.Duration
}

// Exec runs sfdisk with the arguments, which name the disk where sfdisk expects it.
func (s Sfdisk) Exec(ctx context.Context, args ...string) ([]byte, error) {
	wait := s.LockWait
	if wait == 0 {
		wait = DefaultLockWait
	}
	deadline := time.Now().Add(wait)
	for {
		out, err := s.Run.Run(ctx, "sfdisk", append([]string{"--lock=nonblock"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "already locked") {
			return out, err
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("another program kept %s locked for %v: %w", s.Disk, wait, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(lockRetry, time.Until(deadline))):
		}
	}
}

// ReadTable reads the disk's GPT.
func (s Sfdisk) ReadTable(ctx context.Context) ([]Partition, error) {
	out, err := s.Exec(ctx, "--json", s.Disk)
	if err != nil {
		return nil, fmt.Errorf("read the partition table of %s: %w", s.Disk, err)
	}
	return ParseTable(s.Disk, out)
}
