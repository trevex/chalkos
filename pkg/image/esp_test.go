package image

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// TestExtractUKI copies the one UKI off an ESP and refuses an ESP with none or two.
func TestExtractUKI(t *testing.T) {
	for _, tool := range []string{"mkfs.vfat", "mmd", "mcopy", "mdir"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	efi := os.Getenv("CHALKOS_TEST_EFI")
	if efi == "" {
		t.Skip("CHALKOS_TEST_EFI not set")
	}
	dir := t.TempDir()
	raw := filepath.Join(dir, "disk.raw")
	if err := os.WriteFile(raw, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(raw, 32<<20); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "mkfs.vfat", raw)
	fat := raw + "@@0"
	mustRun(t, "mmd", "-i", fat, "::/EFI", "::/EFI/BOOT", "::/EFI/Linux")
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/BOOT/BOOTX64.EFI")
	if _, err := extractUKI(context.Background(), raw, 0, dir); err == nil || !strings.Contains(err.Error(), "holds 0 UKIs") {
		t.Errorf("an ESP without a UKI: %v", err)
	}
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/Linux/chalkos_2.efi")
	out := filepath.Join(dir, "out")
	os.Mkdir(out, 0o755)
	uki, err := extractUKI(context.Background(), raw, 0, out)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(uki) != "chalkos_2.efi" {
		t.Errorf("extracted %s", uki)
	}
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/Linux/other.efi")
	if _, err := extractUKI(context.Background(), raw, 0, dir); err == nil || !strings.Contains(err.Error(), "holds 2 UKIs") {
		t.Errorf("an ESP with two UKIs: %v", err)
	}
}

// TestExtractBootLoader copies systemd-boot off an ESP, as an install sends it, and leaves the
// other binaries of /EFI/BOOT, another architecture's boot loader among them, and the UKIs alone.
func TestExtractBootLoader(t *testing.T) {
	for _, tool := range []string{"mkfs.vfat", "mmd", "mcopy", "mdir"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	efi := os.Getenv("CHALKOS_TEST_EFI")
	if efi == "" {
		t.Skip("CHALKOS_TEST_EFI not set")
	}
	dir := t.TempDir()
	image := filepath.Join(dir, "disk.raw")
	if err := os.WriteFile(image, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(image, 32<<20); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "mkfs.vfat", image)
	fat := image + "@@0"
	mustRun(t, "mmd", "-i", fat, "::/EFI", "::/EFI/BOOT", "::/EFI/Linux")
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/Linux/chalkos_2.efi")
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/BOOT/fallback.efi")
	// Another architecture's boot loader is no x86-64 image's.
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/BOOT/BOOTAA64.EFI")
	if _, err := extractBootLoader(context.Background(), image, 0, dir, "x86-64"); err == nil || !strings.Contains(err.Error(), "has no /EFI/BOOT/BOOTX64.EFI") {
		t.Errorf("an ESP without a boot loader: %v", err)
	}
	if _, err := extractBootLoader(context.Background(), image, 0, dir, "riscv64"); err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("an unknown architecture: %v", err)
	}
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/BOOT/BOOTX64.EFI")
	out := filepath.Join(dir, "out")
	os.Mkdir(out, 0o755)
	loader, err := extractBootLoader(context.Background(), image, 0, out, "x86-64")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(efi)
	if got, _ := os.ReadFile(loader); filepath.Base(loader) != "BOOTX64.EFI" || string(got) != string(want) {
		t.Errorf("extracted %s of %d bytes", loader, len(got))
	}
}
