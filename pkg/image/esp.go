package image

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

// Binaries are copies of the UKI and the boot loader of a disk image's ESP, and the architecture
// both are built for.
type Binaries struct {
	// UKI and BootLoader are the copies' paths; BootLoader is "" when it was not asked for.
	UKI, BootLoader string
	Architecture    string
}

// ExtractBinaries copies the UKI off the image's ESP into dir and, with bootLoader, the boot loader
// of the UKI's architecture, which must be built for that architecture too. Whatever it writes into
// dir, including a UKI copy left behind when a later step fails, is the caller's to remove.
func ExtractBinaries(ctx context.Context, raw string, espOffset int64, dir string, bootLoader bool) (Binaries, error) {
	var b Binaries
	var err error
	if b.UKI, err = ExtractUKI(ctx, raw, espOffset, dir); err != nil {
		return Binaries{}, err
	}
	if b.Architecture, err = fileArchitecture(b.UKI); err != nil {
		return Binaries{}, fmt.Errorf("the image's UKI: %w", err)
	}
	if !bootLoader {
		return b, nil
	}
	if b.BootLoader, err = ExtractBootLoader(ctx, raw, espOffset, dir, b.Architecture); err != nil {
		return Binaries{}, err
	}
	arch, err := fileArchitecture(b.BootLoader)
	if err != nil {
		return Binaries{}, fmt.Errorf("the image's boot loader: %w", err)
	}
	if arch != b.Architecture {
		return Binaries{}, fmt.Errorf("the image's boot loader is built for %s, its UKI for %s", arch, b.Architecture)
	}
	return b, nil
}

// ExtractUKI copies the one UKI on the image's ESP into dir and returns the copy's path.
func ExtractUKI(ctx context.Context, raw string, espOffset int64, dir string) (string, error) {
	found, err := list(ctx, raw, espOffset, "::/EFI/Linux", "*.EFI")
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the ESP of %s holds %d UKIs, want one", raw, len(found))
	}
	return copyOut(ctx, raw, espOffset, found[0], dir)
}

// ExtractBootLoader copies the boot loader of the architecture off the image's ESP,
// /EFI/BOOT/BOOTX64.EFI or /EFI/BOOT/BOOTAA64.EFI, into dir and returns the copy's path.
func ExtractBootLoader(ctx context.Context, raw string, espOffset int64, dir, arch string) (string, error) {
	name := uki.BootLoaderName(arch)
	if name == "" {
		return "", fmt.Errorf("chalkos builds no images for the architecture %q", arch)
	}
	found, err := list(ctx, raw, espOffset, "::/EFI/BOOT", name)
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the ESP of %s has no /EFI/BOOT/%s", raw, name)
	}
	return copyOut(ctx, raw, espOffset, found[0], dir)
}

// list returns the files of the ESP directory whose upper-cased names match pattern.
func list(ctx context.Context, raw string, espOffset int64, espDir, pattern string) ([]string, error) {
	out, err := mtools(ctx, "mdir", "-b", "-i", fmt.Sprintf("%s@@%d", raw, espOffset), espDir)
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
func copyOut(ctx context.Context, raw string, espOffset int64, file, dir string) (string, error) {
	dst := filepath.Join(dir, path.Base(file))
	if _, err := mtools(ctx, "mcopy", "-n", "-i", fmt.Sprintf("%s@@%d", raw, espOffset), file, dst); err != nil {
		return "", err
	}
	return dst, nil
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

// fileArchitecture is the architecture an EFI binary is built for.
func fileArchitecture(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return uki.Architecture(f)
}
