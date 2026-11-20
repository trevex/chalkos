package image

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

// TestMtoolsNamesAMissingTool names the mtools command that is not on PATH and how to get it,
// and MissingESPTools lists every one.
func TestMtoolsNamesAMissingTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Mtools(context.Background(), "mdir", "::/"); err == nil || !strings.HasPrefix(err.Error(), "mdir is not on PATH") || !strings.Contains(err.Error(), "install mtools") {
		t.Errorf("err = %v", err)
	}
	if got := MissingESPTools(); strings.Join(got, " ") != "mformat mmd mcopy mdir" {
		t.Errorf("missing = %q", got)
	}
}

// newESP makes an empty ESP with /EFI/BOOT and /EFI/Linux in dir, and an EFI binary to copy onto
// it, and returns the ESP's image and the binary's path.
func newESP(t *testing.T, dir string) (raw, efi string) {
	t.Helper()
	for _, tool := range []string{"mkfs.vfat", "mmd", "mcopy", "mdir"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	raw, efi = filepath.Join(dir, "disk.raw"), filepath.Join(dir, "binary.efi")
	if err := os.WriteFile(efi, ukitest.Build(nil), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(raw, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(raw, 32<<20); err != nil {
		t.Fatal(err)
	}
	mtools(t, "mkfs.vfat", raw)
	mtools(t, "mmd", "-i", FAT(raw, 0), "::/EFI", "::/EFI/BOOT", "::/EFI/Linux")
	return raw, efi
}

func mtools(t *testing.T, name string, args ...string) {
	t.Helper()
	if _, err := Mtools(context.Background(), name, args...); err != nil {
		t.Fatal(err)
	}
}

// TestExtractUKI copies the one UKI off an ESP and refuses an ESP with none or two.
func TestExtractUKI(t *testing.T) {
	dir := t.TempDir()
	raw, efi := newESP(t, dir)
	mtools(t, "mcopy", "-i", FAT(raw, 0), efi, "::/EFI/BOOT/BOOTX64.EFI")
	if _, err := extractUKI(context.Background(), raw, 0, dir); err == nil || !strings.Contains(err.Error(), "holds 0 UKIs") {
		t.Errorf("an ESP without a UKI: %v", err)
	}
	mtools(t, "mcopy", "-i", FAT(raw, 0), efi, "::/EFI/Linux/chalkos_2.efi")
	out := t.TempDir()
	if uki, err := extractUKI(context.Background(), raw, 0, out); err != nil || filepath.Base(uki) != "chalkos_2.efi" {
		t.Errorf("extracted %s, %v", uki, err)
	}
	mtools(t, "mcopy", "-i", FAT(raw, 0), efi, "::/EFI/Linux/other.efi")
	if _, err := extractUKI(context.Background(), raw, 0, dir); err == nil || !strings.Contains(err.Error(), "holds 2 UKIs") {
		t.Errorf("an ESP with two UKIs: %v", err)
	}
}

// TestExtractBootLoader copies systemd-boot off an ESP, as an install sends it, and leaves the
// other binaries of /EFI/BOOT, another architecture's boot loader among them, and the UKIs alone.
func TestExtractBootLoader(t *testing.T) {
	dir := t.TempDir()
	raw, efi := newESP(t, dir)
	for _, dst := range []string{"::/EFI/Linux/chalkos_2.efi", "::/EFI/BOOT/fallback.efi", "::/EFI/BOOT/BOOTAA64.EFI"} {
		mtools(t, "mcopy", "-i", FAT(raw, 0), efi, dst)
	}
	if _, err := extractBootLoader(context.Background(), raw, 0, dir, "x86-64"); err == nil || !strings.Contains(err.Error(), "has no /EFI/BOOT/BOOTX64.EFI") {
		t.Errorf("an ESP without a boot loader: %v", err)
	}
	if _, err := extractBootLoader(context.Background(), raw, 0, dir, "riscv64"); err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("an unknown architecture: %v", err)
	}
	mtools(t, "mcopy", "-i", FAT(raw, 0), efi, "::/EFI/BOOT/BOOTX64.EFI")
	loader, err := extractBootLoader(context.Background(), raw, 0, t.TempDir(), "x86-64")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(efi)
	if got, _ := os.ReadFile(loader); filepath.Base(loader) != "BOOTX64.EFI" || string(got) != string(want) {
		t.Errorf("extracted %s of %d bytes", loader, len(got))
	}
}
