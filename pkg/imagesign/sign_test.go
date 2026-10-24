package imagesign

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/trevex/chalkos/pkg/image/imagetest"
)

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func TestSignImageSignsOnlyBootLoaderAndUKIs(t *testing.T) {
	for _, tool := range []string{"mkfs.vfat", "mmd", "mcopy", "mdir", "sbsign", "sbverify"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	efi := os.Getenv("CHALKOS_TEST_EFI")
	if efi == "" {
		t.Skip("CHALKOS_TEST_EFI not set")
	}
	dir := t.TempDir()
	key, cert, _ := imagetest.Signer(t, dir, "db")

	// 1 MiB of padding stands in for the partition table, followed by a 32 MiB FAT filesystem.
	const offset = 1 << 20
	image := filepath.Join(dir, "disk.raw")
	if err := os.WriteFile(image, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(image, offset+32<<20); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "mkfs.vfat", "--offset", strconv.Itoa(offset/512), image, strconv.Itoa(32<<10))
	fat := fmt.Sprintf("%s@@%d", image, offset)
	mustRun(t, "mmd", "-i", fat, "::/EFI", "::/EFI/BOOT", "::/EFI/Linux", "::/EFI/tools")
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/BOOT/BOOTX64.EFI")
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/Linux/chalkos_1.efi")
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/tools/shell.efi")

	if err := SignImage(context.Background(), image, offset, key, cert); err != nil {
		t.Fatal(err)
	}

	verify := func(f string) error {
		out := filepath.Join(dir, "check.efi")
		os.Remove(out)
		mustRun(t, "mcopy", "-n", "-i", fat, f, out)
		return exec.Command("sbverify", "--cert", cert, out).Run()
	}
	for _, f := range []string{"::/EFI/BOOT/BOOTX64.EFI", "::/EFI/Linux/chalkos_1.efi"} {
		if err := verify(f); err != nil {
			t.Errorf("%s is not signed: %v", f, err)
		}
	}
	if verify("::/EFI/tools/shell.efi") == nil {
		t.Error("::/EFI/tools/shell.efi was signed")
	}
}

// TestSignFile signs an EFI binary in place.
func TestSignFile(t *testing.T) {
	for _, tool := range []string{"sbsign", "sbverify"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	efi := os.Getenv("CHALKOS_TEST_EFI")
	if efi == "" {
		t.Skip("CHALKOS_TEST_EFI not set")
	}
	dir := t.TempDir()
	key, cert, _ := imagetest.Signer(t, dir, "db")
	data, err := os.ReadFile(efi)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "BOOTX64.EFI")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SignFile(context.Background(), file, key, cert); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("sbverify", "--cert", cert, file).Run(); err != nil {
		t.Errorf("the file is not signed: %v", err)
	}
}
