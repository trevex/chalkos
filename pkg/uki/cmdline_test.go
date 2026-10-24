package uki

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunningHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cmdline")
	if _, err := RunningHash(path); err == nil || !strings.Contains(err.Error(), "read the kernel command line") {
		t.Fatalf("a missing command line: err = %v", err)
	}
	os.WriteFile(path, []byte("init=/x usrhash="+strings.Repeat("ab", 32)+"\n"), 0o644)
	got, err := RunningHash(path)
	if err != nil || !bytes.Equal(got, bytes.Repeat([]byte{0xab}, 32)) {
		t.Fatalf("RunningHash = %x, %v", got, err)
	}
	os.WriteFile(path, []byte("init=/x\n"), 0o644)
	if _, err := RunningHash(path); err == nil || !strings.HasPrefix(err.Error(), "the running store: ") {
		t.Fatalf("no usrhash=: err = %v", err)
	}
}
