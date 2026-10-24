package filecopy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
