package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateOverlayGrowsVirtualSize(t *testing.T) {
	requireTools(t, "qemu-img")
	dir := t.TempDir()
	raw := filepath.Join(dir, "base.raw")
	if err := os.WriteFile(raw, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "disk.qcow2")

	if err := CreateOverlay(context.Background(), raw, overlay, "4M"); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("qemu-img", "info", "--output=json", overlay).Output()
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		VirtualSize     int64  `json:"virtual-size"`
		BackingFilename string `json:"backing-filename"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		t.Fatal(err)
	}
	if info.VirtualSize != 4<<20 || info.BackingFilename != raw {
		t.Fatalf("info = %+v, want 4 MiB backed by %s", info, raw)
	}
}

func TestCopySparseProducesWritableCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	content := append(make([]byte, 64<<10), []byte("tail")...)
	if err := os.WriteFile(src, content, 0o444); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")

	if err := CopySparse(context.Background(), src, dst); err != nil {
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
