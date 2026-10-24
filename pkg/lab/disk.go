package lab

import (
	"context"

	"github.com/trevex/chalkos/pkg/internal/filecopy"
)

// CreateOverlay creates a qcow2 disk of the given virtual size backed by a raw image.
// The overlay may be larger than the image, which gives systemd-repart room to add partitions.
func CreateOverlay(ctx context.Context, backingRaw, path, size string) error {
	return run(ctx, "qemu-img", "create", "-q", "-f", "qcow2", "-F", "raw", "-b", backingRaw, path, size)
}

// CopySparse copies a disk image without materialising its holes, writable for signing and fault
// injection.
func CopySparse(ctx context.Context, src, dst string) error {
	return filecopy.Sparse(ctx, src, dst)
}

// CreateDisk creates an empty qcow2 disk of the given virtual size.
func CreateDisk(ctx context.Context, path, size string) error {
	return run(ctx, "qemu-img", "create", "-q", "-f", "qcow2", path, size)
}
