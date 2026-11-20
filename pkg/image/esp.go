package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/arch"
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
	if b.UKI, err = extractUKI(ctx, raw, espOffset, dir); err != nil {
		return Binaries{}, err
	}
	if b.Architecture, err = fileArchitecture(b.UKI); err != nil {
		return Binaries{}, fmt.Errorf("the image's UKI: %w", err)
	}
	if !bootLoader {
		return b, nil
	}
	if b.BootLoader, err = extractBootLoader(ctx, raw, espOffset, dir, b.Architecture); err != nil {
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

// extractUKI copies the one UKI on the image's ESP into dir and returns the copy's path.
func extractUKI(ctx context.Context, raw string, espOffset int64, dir string) (string, error) {
	found, err := ListESP(ctx, FAT(raw, espOffset), "::/EFI/LINUX/*.EFI")
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the ESP of %s holds %d UKIs, want one", raw, len(found))
	}
	return copyOut(ctx, raw, espOffset, found[0], dir)
}

// extractBootLoader copies the boot loader of the architecture off the image's ESP,
// /EFI/BOOT/BOOTX64.EFI or /EFI/BOOT/BOOTAA64.EFI, into dir and returns the copy's path.
func extractBootLoader(ctx context.Context, raw string, espOffset int64, dir, archName string) (string, error) {
	a, ok := arch.ByName(archName)
	if !ok {
		return "", fmt.Errorf("chalkos builds no images for the architecture %q", archName)
	}
	name := a.BootLoader
	found, err := ListESP(ctx, FAT(raw, espOffset), "::/EFI/BOOT/"+name)
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the ESP of %s has no /EFI/BOOT/%s", raw, name)
	}
	return copyOut(ctx, raw, espOffset, found[0], dir)
}

// FAT is mtools' name of the FAT file system at offset in the disk image raw.
func FAT(raw string, offset int64) string { return fmt.Sprintf("%s@@%d", raw, offset) }

// ListESP lists the files below ::/EFI of the FAT file system fat whose upper-cased paths match
// one of the patterns, as FAT names are case-insensitive. path.Match's * does not cross "/".
func ListESP(ctx context.Context, fat string, patterns ...string) ([]string, error) {
	out, err := Mtools(ctx, "mdir", "-/", "-b", "-i", fat, "::/EFI")
	if err != nil {
		return nil, err
	}
	var found []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if slices.ContainsFunc(patterns, func(p string) bool { ok, _ := path.Match(p, strings.ToUpper(line)); return ok }) {
			found = append(found, line)
		}
	}
	return found, nil
}

// copyOut copies a file of the ESP into dir.
func copyOut(ctx context.Context, raw string, espOffset int64, file, dir string) (string, error) {
	dst := filepath.Join(dir, path.Base(file))
	if _, err := Mtools(ctx, "mcopy", "-n", "-i", FAT(raw, espOffset), file, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// ESPFile is a file WriteESP copies onto an ESP: the local file From to the path To, such as
// ::/EFI/BOOT/BOOTX64.EFI.
type ESPFile struct{ From, To string }

// espTools are the mtools commands WriteESP and the readers of an ESP run.
var espTools = []string{"mformat", "mmd", "mcopy", "mdir"}

// MissingESPTools are the mtools commands that write and read an ESP and are not on PATH; none
// when mtools is installed.
func MissingESPTools() []string {
	var missing []string
	for _, tool := range espTools {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	return missing
}

// espEpoch is the time WriteESP gives the ESP's files and directories, 1980-01-01 in UTC, the
// earliest a FAT time stamp holds, so that the same files always make the same ESP.
const espEpoch = "315532800"

// WriteESP formats the FAT file system at offset in the disk image raw as FAT32 of sectors
// 512-byte sectors, with the label ESP and the volume ID, and copies the files onto it, making
// their directories. It gives every file and directory the same time, so that the same
// arguments always write the same bytes.
func WriteESP(ctx context.Context, raw string, offset, sectors int64, volumeID uint32, files []ESPFile) error {
	fat := FAT(raw, offset)
	env := []string{"SOURCE_DATE_EPOCH=" + espEpoch, "TZ=UTC"}
	if _, err := runMtools(ctx, env, "mformat", "-i", fat, "-F", "-T", fmt.Sprint(sectors), "-N", fmt.Sprintf("%08x", volumeID), "-v", "ESP", "::"); err != nil {
		return err
	}
	made := map[string]bool{"::": true}
	for _, f := range files {
		var dirs []string
		for dir := path.Dir(f.To); !made[dir]; dir = path.Dir(dir) {
			made[dir] = true
			dirs = append([]string{dir}, dirs...)
		}
		if len(dirs) > 0 {
			if _, err := runMtools(ctx, env, "mmd", append([]string{"-i", fat}, dirs...)...); err != nil {
				return err
			}
		}
		if _, err := runMtools(ctx, env, "mcopy", "-i", fat, f.From, f.To); err != nil {
			return err
		}
	}
	return nil
}

// Mtools runs an mtools command and returns its output. MTOOLS_SKIP_CHECK stops mtools from
// rejecting FAT geometry that does not match a physical floppy or disk.
func Mtools(ctx context.Context, name string, args ...string) (string, error) {
	return runMtools(ctx, nil, name, args...)
}

// runMtools is Mtools with the variables env added to its environment. It names the tool and
// mtools when the tool is not on PATH.
func runMtools(ctx context.Context, env []string, name string, args ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", fmt.Errorf("%s is not on PATH: chalkos reads and writes an image's ESP with mtools; install mtools, which the flake's chalkctl and chalklab bring", name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(append(os.Environ(), "MTOOLS_SKIP_CHECK=1"), env...)
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
