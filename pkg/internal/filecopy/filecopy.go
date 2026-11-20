// Package filecopy copies files: disk images without their holes, and plain files.
package filecopy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// sparseBlock is the unit Sparse leaves a hole for when it holds only zeros.
const sparseBlock = 64 << 10

// Sparse copies a disk image to dst without materialising its holes: as cp --sparse=always does,
// it leaves a hole for every block of zeros, also where the source holds the zeros, as an image
// restored from a binary cache does. Images in the Nix store are read-only, so the copy is made
// writable, for signing and fault injection.
func Sparse(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := sparseCopy(ctx, in, out); err != nil {
		out.Close()
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, 0o644)
}

// sparseCopy writes the blocks of in that are not all zeros to out, at their offsets, and sizes out
// as in.
func sparseCopy(ctx context.Context, in, out *os.File) error {
	buf, zeros := make([]byte, sparseBlock), make([]byte, sparseBlock)
	var off int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(in, buf)
		if n > 0 && !bytes.Equal(buf[:n], zeros[:n]) {
			if _, err := out.WriteAt(buf[:n], off); err != nil {
				return err
			}
		}
		off += int64(n)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return out.Truncate(off)
		}
		if err != nil {
			return err
		}
	}
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
