// Package imagesign signs the boot loader and UKIs of chalkos disk images for Secure Boot, in
// place or in a signed copy of an image directory.
package imagesign

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/fatfs"
	"github.com/trevex/chalkos/pkg/image"
)

// SignImage signs the boot loader (/EFI/BOOT/BOOT*.EFI) and the UKIs (/EFI/Linux/*.efi) on the
// image's ESP in place with sbsign. Other EFI binaries stay unsigned, so whatever else lands on the
// ESP does not gain the db signature. The UKI's signature covers the store through the verity
// root hash on its command line.
func SignImage(ctx context.Context, raw string, espOffset int64, key, cert string) (err error) {
	esp, err := fatfs.Open(raw, espOffset, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, esp.Close()) }()
	// Only direct children match.
	files, err := image.ListESP(esp, "/EFI/BOOT/BOOT*.EFI", "/EFI/LINUX/*.EFI")
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
		data, err := esp.ReadFile(f)
		if err != nil {
			return err
		}
		local := filepath.Join(tmp, fmt.Sprintf("%d.efi", i))
		if err := os.WriteFile(local, data, 0o600); err != nil {
			return err
		}
		if err := signFile(ctx, local, key, cert); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if data, err = os.ReadFile(local); err != nil {
			return err
		}
		if err := esp.WriteFile(f, data); err != nil {
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
