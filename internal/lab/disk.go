package lab

import (
	"context"
	"io"
	"os"
)

// CreateOverlay creates a qcow2 disk of the given virtual size backed by a raw image.
// The overlay may be larger than the image, which gives systemd-repart room to add partitions.
func CreateOverlay(ctx context.Context, backingRaw, path, size string) error {
	return run(ctx, "qemu-img", "create", "-q", "-f", "qcow2", "-F", "raw", "-b", backingRaw, path, size)
}

// CopySparse copies a disk image without materialising its holes. Images in the Nix store are
// read-only, so the copy is made writable for signing and fault injection.
func CopySparse(ctx context.Context, src, dst string) error {
	if err := run(ctx, "cp", "--sparse=always", src, dst); err != nil {
		return err
	}
	return os.Chmod(dst, 0o644)
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
