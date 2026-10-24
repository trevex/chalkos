// Package imagesign signs the boot loader and UKIs of chalkos disk images for Secure Boot, in
// place or in a signed copy of an image directory.
package imagesign

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/image"
)

// SignImage signs the boot loader (/EFI/BOOT/BOOT*.EFI) and the UKIs (/EFI/Linux/*.efi) on
// the image's ESP in place with sbsign. Other EFI binaries stay unsigned so that whatever
// else lands on the ESP does not gain the cluster's db signature.
// The image's verity-protected store is untouched; the UKI's signature covers the store
// because the UKI command line carries the store's verity root hash.
func SignImage(ctx context.Context, raw string, espOffset int64, key, cert string) error {
	fat := fmt.Sprintf("%s@@%d", raw, espOffset)
	files, err := listSignedBinaries(ctx, fat)
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
		if err := SignFile(ctx, local, key, cert); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if _, err := image.Mtools(ctx, "mcopy", "-o", "-i", fat, local, f); err != nil {
			return err
		}
	}
	return nil
}

// signedPatterns are matched against upper-cased paths because FAT names are
// case-insensitive. path.Match's * does not cross "/", so only direct children match.
var signedPatterns = []string{"::/EFI/BOOT/BOOT*.EFI", "::/EFI/LINUX/*.EFI"}

func listSignedBinaries(ctx context.Context, fat string) ([]string, error) {
	out, err := image.Mtools(ctx, "mdir", "-/", "-b", "-i", fat, "::/EFI")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		for _, p := range signedPatterns {
			if ok, _ := path.Match(p, strings.ToUpper(line)); ok {
				files = append(files, line)
				break
			}
		}
	}
	return files, nil
}

// SignFile signs an EFI binary in place with sbsign.
func SignFile(ctx context.Context, file, key, cert string) error {
	signed := file + ".signed"
	if out, err := exec.CommandContext(ctx, "sbsign", "--key", key, "--cert", cert, "--output", signed, file).CombinedOutput(); err != nil {
		return fmt.Errorf("sbsign %s: %w: %s", file, err, out)
	}
	return os.Rename(signed, file)
}
