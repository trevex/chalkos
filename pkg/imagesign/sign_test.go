package imagesign

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

func TestSignImageSignsOnlyBootLoaderAndUKIs(t *testing.T) {
	dir := t.TempDir()
	efi := filepath.Join(dir, "binary.efi")
	if err := os.WriteFile(efi, ukitest.Build(nil), 0o644); err != nil {
		t.Fatal(err)
	}
	key, cert, _ := imagetest.Signer(t, dir, "db")

	// 1 MiB of padding stands in for the partition table, followed by a 64 MiB FAT file system.
	const offset = 1 << 20
	disk := filepath.Join(dir, "disk.raw")
	if err := os.WriteFile(disk, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(disk, offset+64<<20); err != nil {
		t.Fatal(err)
	}
	var files []image.ESPFile
	for _, f := range []string{"/EFI/BOOT/BOOTX64.EFI", "/EFI/Linux/chalkos_1.efi", "/EFI/tools/shell.efi"} {
		files = append(files, image.ESPFile{From: efi, To: f})
	}
	if err := image.WriteESP(disk, offset, 64<<20, 1, files); err != nil {
		t.Fatal(err)
	}

	if err := SignImage(disk, offset, key, cert); err != nil {
		t.Fatal(err)
	}

	verify := func(f string) error { return signedBy(t, imagetest.ReadESP(t, disk, offset, f), cert) }
	for _, f := range []string{"/EFI/BOOT/BOOTX64.EFI", "/EFI/Linux/chalkos_1.efi"} {
		if err := verify(f); err != nil {
			t.Errorf("%s is not signed: %v", f, err)
		}
	}
	if verify("/EFI/tools/shell.efi") == nil {
		t.Error("/EFI/tools/shell.efi was signed")
	}
}
