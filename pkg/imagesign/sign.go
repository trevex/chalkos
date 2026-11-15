// Package imagesign signs the boot loader and UKIs of chalkos disk images for Secure Boot, in
// place or in a signed copy of an image directory.
package imagesign

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/image"
)

// SignImage signs the boot loader (/EFI/BOOT/BOOT*.EFI) and the UKIs (/EFI/Linux/*.efi) on the
// image's ESP in place with sbsign. Other EFI binaries stay unsigned, so whatever else lands on the
// ESP does not gain the db signature. The UKI's signature covers the store through the verity
// root hash on its command line.
func SignImage(ctx context.Context, raw string, espOffset int64, key, cert string) error {
	fat := image.FAT(raw, espOffset)
	// Only direct children match.
	files, err := image.ListESP(ctx, fat, "::/EFI/BOOT/BOOT*.EFI", "::/EFI/LINUX/*.EFI")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no boot loader or UKI on the ESP of %s", raw)
	}

	tmp, err := os.MkdirTemp("", "imagesign")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	for i, f := range files {
		local := filepath.Join(tmp, fmt.Sprintf("%d.efi", i))
		if _, err := image.Mtools(ctx, "mcopy", "-n", "-i", fat, f, local); err != nil {
			return err
		}
		if err := signFile(ctx, local, key, cert); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if _, err := image.Mtools(ctx, "mcopy", "-o", "-i", fat, local, f); err != nil {
			return err
		}
	}
	return nil
}

// signFile signs an EFI binary in place with sbsign.
func signFile(ctx context.Context, file, key, cert string) error {
	signed := file + ".signed"
	if out, err := exec.CommandContext(ctx, "sbsign", "--key", key, "--cert", cert, "--output", signed, file).CombinedOutput(); err != nil {
		return fmt.Errorf("sbsign %s: %w: %s", file, err, out)
	}
	return os.Rename(signed, file)
}
