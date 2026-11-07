package atomicfile

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestLock refuses a second lock while the first is held, and takes it once that is released.
func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	f, err := Lock(path, 0o600, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path, 0o600, false); !errors.Is(err, ErrLocked) {
		t.Errorf("second Lock: err = %v, want ErrLocked", err)
	}
	f.Close()
	g, err := Lock(path, 0o600, true)
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	g.Close()
}
