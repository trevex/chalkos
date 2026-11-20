package image

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/fatfs"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

// newESP makes an empty ESP with /EFI/BOOT and /EFI/Linux in dir and returns it opened, with an
// EFI binary to copy onto it.
func newESP(t *testing.T, dir string) (esp *fatfs.FS, efi []byte) {
	t.Helper()
	raw := filepath.Join(dir, "disk.raw")
	if err := os.WriteFile(raw, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(raw, 64<<20); err != nil {
		t.Fatal(err)
	}
	if err := fatfs.Format(raw, 0, 64<<20, "ESP", 1); err != nil {
		t.Fatal(err)
	}
	esp, err := fatfs.Open(raw, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { esp.Close() })
	for _, dir := range []string{"/EFI/BOOT", "/EFI/Linux"} {
		if err := esp.MkdirAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	return esp, ukitest.Build(nil)
}

// put copies data onto the ESP as name.
func put(t *testing.T, esp *fatfs.FS, name string, data []byte) {
	t.Helper()
	if err := esp.MkdirAll(path.Dir(name)); err != nil {
		t.Fatal(err)
	}
	if err := esp.WriteFile(name, data); err != nil {
		t.Fatal(err)
	}
}

// TestExtractUKI copies the one UKI off an ESP and refuses an ESP with none or two.
func TestExtractUKI(t *testing.T) {
	dir := t.TempDir()
	esp, efi := newESP(t, dir)
	put(t, esp, "/EFI/BOOT/BOOTX64.EFI", efi)
	if _, err := extractUKI(esp, "disk.raw", dir); err == nil || !strings.Contains(err.Error(), "holds 0 UKIs") {
		t.Errorf("an ESP without a UKI: %v", err)
	}
	put(t, esp, "/EFI/Linux/chalkos_2.efi", efi)
	out := t.TempDir()
	if uki, err := extractUKI(esp, "disk.raw", out); err != nil || filepath.Base(uki) != "chalkos_2.efi" {
		t.Errorf("extracted %s, %v", uki, err)
	}
	put(t, esp, "/EFI/Linux/other.efi", efi)
	if _, err := extractUKI(esp, "disk.raw", dir); err == nil || !strings.Contains(err.Error(), "holds 2 UKIs") {
		t.Errorf("an ESP with two UKIs: %v", err)
	}
}

// TestExtractBootLoader copies systemd-boot off an ESP, as an install sends it, and leaves the
// other binaries of /EFI/BOOT, another architecture's boot loader among them, and the UKIs alone.
func TestExtractBootLoader(t *testing.T) {
	dir := t.TempDir()
	esp, efi := newESP(t, dir)
	for _, dst := range []string{"/EFI/Linux/chalkos_2.efi", "/EFI/BOOT/fallback.efi", "/EFI/BOOT/BOOTAA64.EFI"} {
		put(t, esp, dst, efi)
	}
	if _, err := extractBootLoader(esp, "disk.raw", dir, "x86-64"); err == nil || !strings.Contains(err.Error(), "has no /EFI/BOOT/BOOTX64.EFI") {
		t.Errorf("an ESP without a boot loader: %v", err)
	}
	if _, err := extractBootLoader(esp, "disk.raw", dir, "riscv64"); err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("an unknown architecture: %v", err)
	}
	put(t, esp, "/EFI/BOOT/BOOTX64.EFI", efi)
	loader, err := extractBootLoader(esp, "disk.raw", t.TempDir(), "x86-64")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(loader); filepath.Base(loader) != "BOOTX64.EFI" || string(got) != string(efi) {
		t.Errorf("extracted %s of %d bytes", loader, len(got))
	}
}
