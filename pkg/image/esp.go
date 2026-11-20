package image

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/arch"
	"github.com/trevex/chalkos/pkg/fatfs"
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
func ExtractBinaries(raw string, espOffset int64, dir string, bootLoader bool) (_ Binaries, err error) {
	esp, err := fatfs.Open(raw, espOffset, false)
	if err != nil {
		return Binaries{}, err
	}
	defer func() { err = errors.Join(err, esp.Close()) }()
	var b Binaries
	if b.UKI, err = extractUKI(esp, raw, dir); err != nil {
		return Binaries{}, err
	}
	if b.Architecture, err = fileArchitecture(b.UKI); err != nil {
		return Binaries{}, fmt.Errorf("the image's UKI: %w", err)
	}
	if !bootLoader {
		return b, nil
	}
	if b.BootLoader, err = extractBootLoader(esp, raw, dir, b.Architecture); err != nil {
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

// extractUKI copies the one UKI on the ESP of the image raw into dir and returns the copy's path.
func extractUKI(esp *fatfs.FS, raw, dir string) (string, error) {
	found, err := ListESP(esp, "/EFI/LINUX/*.EFI")
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the ESP of %s holds %d UKIs, want one", raw, len(found))
	}
	return copyOut(esp, found[0], dir)
}

// extractBootLoader copies the boot loader of the architecture off the ESP of the image raw,
// /EFI/BOOT/BOOTX64.EFI or /EFI/BOOT/BOOTAA64.EFI, into dir and returns the copy's path.
func extractBootLoader(esp *fatfs.FS, raw, dir, archName string) (string, error) {
	a, ok := arch.ByName(archName)
	if !ok {
		return "", fmt.Errorf("chalkos builds no images for the architecture %q", archName)
	}
	name := a.BootLoader
	found, err := ListESP(esp, "/EFI/BOOT/"+name)
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the ESP of %s has no /EFI/BOOT/%s", raw, name)
	}
	return copyOut(esp, found[0], dir)
}

// ListESP lists the files below /EFI of the ESP whose upper-cased paths match one of the
// patterns, as FAT names are case-insensitive. path.Match's * does not cross "/".
func ListESP(esp *fatfs.FS, patterns ...string) ([]string, error) {
	files, err := esp.List("/EFI")
	if err != nil {
		return nil, err
	}
	var found []string
	for _, f := range files {
		if slices.ContainsFunc(patterns, func(p string) bool { ok, _ := path.Match(p, strings.ToUpper(f)); return ok }) {
			found = append(found, f)
		}
	}
	return found, nil
}

// copyOut copies a file of the ESP into dir.
func copyOut(esp *fatfs.FS, file, dir string) (string, error) {
	data, err := esp.ReadFile(file)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, path.Base(file))
	return dst, os.WriteFile(dst, data, 0o644)
}

// ESPFile is a file WriteESP copies onto an ESP: the local file From to the path To, such as
// /EFI/BOOT/BOOTX64.EFI.
type ESPFile struct{ From, To string }

// WriteESP formats the size bytes at offset in the disk image raw as FAT32, with the label ESP and
// the volume ID, and copies the files onto it in their order, making their directories. The same
// arguments always write the same bytes.
func WriteESP(raw string, offset, size int64, volumeID uint32, files []ESPFile) (err error) {
	if err := fatfs.Format(raw, offset, size, "ESP", volumeID); err != nil {
		return err
	}
	esp, err := fatfs.Open(raw, offset, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, esp.Close()) }()
	for _, f := range files {
		data, err := os.ReadFile(f.From)
		if err != nil {
			return err
		}
		if err := esp.MkdirAll(path.Dir(f.To)); err != nil {
			return err
		}
		if err := esp.WriteFile(f.To, data); err != nil {
			return err
		}
	}
	return nil
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
