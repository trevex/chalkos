// Package filecopy copies files: disk images without their holes, and plain files.
package filecopy

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
)

// Sparse copies a disk image without materialising its holes. Images in the Nix store are
// read-only, so the copy is made writable, for signing and fault injection.
func Sparse(ctx context.Context, src, dst string) error {
	if out, err := exec.CommandContext(ctx, "cp", "--sparse=always", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("cp --sparse=always %s %s: %w: %s", src, dst, err, out)
	}
	return os.Chmod(dst, 0o644)
}

// File copies a file's content to a new file with perm.
func File(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
