package imagesign

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SignImage signs every EFI binary under /EFI on the image's ESP in place with sbsign.
// The image's verity-protected store is untouched; the UKI's signature covers the store
// because the UKI command line carries the store's verity root hash.
func SignImage(ctx context.Context, image string, esp Partition, key, cert string) error {
	fat := fmt.Sprintf("%s@@%d", image, esp.Offset)
	files, err := listEFIBinaries(ctx, fat)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no EFI binaries on the ESP of %s", image)
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

func listEFIBinaries(ctx context.Context, fat string) ([]string, error) {
	out, err := mtools(ctx, "mdir", "-/", "-b", "-i", fat, "::/EFI")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.EqualFold(filepath.Ext(line), ".efi") {
			files = append(files, line)
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
