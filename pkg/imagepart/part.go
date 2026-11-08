// Package imagepart describes the parts an image is sent in, its store, the store's hash tree,
// its UKI and its boot loader, by size and SHA-256, and receives them from a stream.
package imagepart

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
)

// Part is a part of an image: its size in bytes and the SHA-256 of its content.
type Part struct {
	Size   int64
	SHA256 []byte
}

// chunk is how much of a part is read and written at once.
const chunk = 4 << 20

// Receive reads the part, exactly its size, from r and writes it to w, then checks its SHA-256.
// The part's name, what, names it in errors. Whatever follows the part in r is left unread.
func (p Part) Receive(w io.Writer, r io.Reader, what string) error {
	sum := sha256.New()
	buf := make([]byte, min(chunk, p.Size))
	for left := p.Size; left > 0; {
		b := buf[:min(int64(len(buf)), left)]
		if _, err := io.ReadFull(r, b); err != nil {
			return fmt.Errorf("receive the %s: %w", what, err)
		}
		sum.Write(b)
		if _, err := w.Write(b); err != nil {
			return fmt.Errorf("write the %s: %w", what, err)
		}
		left -= int64(len(b))
	}
	if got := sum.Sum(nil); !bytes.Equal(got, p.SHA256) {
		return fmt.Errorf("the %s's SHA-256 is %x, want %x", what, got, p.SHA256)
	}
	return nil
}
