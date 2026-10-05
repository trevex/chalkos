package imagesign

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// SignImage signs the boot loader (/EFI/BOOT/BOOT*.EFI) and the UKIs (/EFI/Linux/*.efi) on
// the image's ESP in place with sbsign. Other EFI binaries stay unsigned so that whatever
// else lands on the ESP does not gain the cluster's db signature.
// The image's verity-protected store is untouched; the UKI's signature covers the store
// because the UKI command line carries the store's verity root hash.
func SignImage(ctx context.Context, image string, esp Partition, key, cert string) error {
	fat := fmt.Sprintf("%s@@%d", image, esp.Offset)
	files, err := listSignedBinaries(ctx, fat)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no boot loader or UKI on the ESP of %s", image)
	}

	tmp, err := os.MkdirTemp("", "imagesign")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	for i, f := range files {
		unsigned := filepath.Join(tmp, fmt.Sprintf("%d.efi", i))
		signed := unsigned + ".signed"
		if _, err := mtools(ctx, "mcopy", "-n", "-i", fat, f, unsigned); err != nil {
			return err
		}
		if out, err := exec.CommandContext(ctx, "sbsign", "--key", key, "--cert", cert, "--output", signed, unsigned).CombinedOutput(); err != nil {
			return fmt.Errorf("sbsign %s: %w: %s", f, err, out)
		}
		if _, err := mtools(ctx, "mcopy", "-o", "-i", fat, signed, f); err != nil {
			return err
		}
	}
	return nil
}

// signedPatterns are matched against upper-cased paths because FAT names are
// case-insensitive. path.Match's * does not cross "/", so only direct children match.
var signedPatterns = []string{"::/EFI/BOOT/BOOT*.EFI", "::/EFI/LINUX/*.EFI"}

func listSignedBinaries(ctx context.Context, fat string) ([]string, error) {
	out, err := mtools(ctx, "mdir", "-/", "-b", "-i", fat, "::/EFI")
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

// mtools runs an mtools command. MTOOLS_SKIP_CHECK stops mtools from rejecting FAT geometry
// that does not match a physical floppy or disk.
func mtools(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}
