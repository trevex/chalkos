// Package imagesign signs the boot loader and UKIs of chalkos disk images for Secure Boot, and
// copies them off an image for an install or upgrade.
package imagesign

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/uki"
)

// SignImage signs the boot loader (/EFI/BOOT/BOOT*.EFI) and the UKIs (/EFI/Linux/*.efi) on
// the image's ESP in place with sbsign. Other EFI binaries stay unsigned so that whatever
// else lands on the ESP does not gain the cluster's db signature.
// The image's verity-protected store is untouched; the UKI's signature covers the store
// because the UKI command line carries the store's verity root hash.
func SignImage(ctx context.Context, image string, espOffset int64, key, cert string) error {
	fat := fmt.Sprintf("%s@@%d", image, espOffset)
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

// ExtractUKI copies the one UKI on the image's ESP into dir and returns the copy's path, for an
// install or upgrade, which writes the UKI by itself.
func ExtractUKI(ctx context.Context, image string, espOffset int64, dir string) (string, error) {
	return extract(ctx, image, espOffset, dir, "::/EFI/Linux", "*.EFI", "UKIs")
}

// ExtractBootLoader copies the boot loader of the architecture off the image's ESP,
// /EFI/BOOT/BOOTX64.EFI or /EFI/BOOT/BOOTAA64.EFI, into dir and returns the copy's path, for an
// install, which writes the boot loader by itself.
func ExtractBootLoader(ctx context.Context, image string, espOffset int64, dir, arch string) (string, error) {
	name := uki.BootLoaderName(arch)
	if name == "" {
		return "", fmt.Errorf("chalkos builds no images for the architecture %q", arch)
	}
	found, err := list(ctx, image, espOffset, "::/EFI/BOOT", name)
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the ESP of %s has no /EFI/BOOT/%s", image, name)
	}
	return copyOut(ctx, image, espOffset, found[0], dir)
}

// extract copies the one file of the ESP directory whose upper-cased name matches pattern.
func extract(ctx context.Context, image string, espOffset int64, dir, espDir, pattern, what string) (string, error) {
	found, err := list(ctx, image, espOffset, espDir, pattern)
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the ESP of %s holds %d %s, want one", image, len(found), what)
	}
	return copyOut(ctx, image, espOffset, found[0], dir)
}

// list returns the files of the ESP directory whose upper-cased names match pattern.
func list(ctx context.Context, image string, espOffset int64, espDir, pattern string) ([]string, error) {
	out, err := mtools(ctx, "mdir", "-b", "-i", fmt.Sprintf("%s@@%d", image, espOffset), espDir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if ok, _ := path.Match(pattern, strings.ToUpper(path.Base(line))); ok {
			found = append(found, line)
		}
	}
	return found, nil
}

// copyOut copies a file of the ESP into dir.
func copyOut(ctx context.Context, image string, espOffset int64, file, dir string) (string, error) {
	dst := filepath.Join(dir, path.Base(file))
	if _, err := mtools(ctx, "mcopy", "-n", "-i", fmt.Sprintf("%s@@%d", image, espOffset), file, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// SignFile signs an EFI binary in place with sbsign.
func SignFile(ctx context.Context, file, key, cert string) error {
	signed := file + ".signed"
	if out, err := exec.CommandContext(ctx, "sbsign", "--key", key, "--cert", cert, "--output", signed, file).CombinedOutput(); err != nil {
		return fmt.Errorf("sbsign %s: %w: %s", file, err, out)
	}
	return os.Rename(signed, file)
}
