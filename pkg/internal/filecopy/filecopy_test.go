package filecopy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSparseLeavesHolesForZeros copies a file that holds 64 MiB of written zeros between its data
// into a copy that allocates little more than the data, and is the same byte for byte.
func TestSparseLeavesHolesForZeros(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	content := append(append([]byte("head"), make([]byte, 64<<20)...), []byte("tail")...)
	if err := os.WriteFile(src, content, 0o444); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := Sparse(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("copy differs from source")
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if allocated := info.Sys().(*syscall.Stat_t).Blocks * 512; allocated > 1<<20 {
		t.Errorf("the copy allocates %d bytes for %d bytes of data", allocated, 8)
	}
}

func TestSparseProducesWritableCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	content := append(make([]byte, 64<<10), []byte("tail")...)
	if err := os.WriteFile(src, content, 0o444); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")

	if err := Sparse(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("copy differs from source")
	}
	f, err := os.OpenFile(dst, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("copy is not writable: %v", err)
	}
	f.Close()
}
