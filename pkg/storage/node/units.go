package node

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/trevex/chalkos/pkg/storage"
)

// Units are the generator's output: unit files by name, the volume each unit belongs to, and
// the units each target wants.
type Units struct {
	files  map[string]string
	owners map[string]string
	wants  map[string][]string
}

// Generate builds units for every volume except VAR, which the initrd mounts: a cryptsetup
// service for encrypted volumes, and a mount or swap unit for formatted ones. All are only
// wanted, so a missing disk fails its units without holding up the boot.
func Generate(storageDir, cryptsetup string) (Units, error) {
	units := Units{files: map[string]string{}, owners: map[string]string{}, wants: map[string][]string{}}
	section, err := storage.ReadSection(filepath.Join(storageDir, "storage.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return units, nil
	}
	if err != nil {
		return units, err
	}
	pins, err := storage.ReadPins(filepath.Join(storageDir, "disks.json"))
	if err != nil {
		return units, err
	}

	names := make([]string, 0, len(section.Volumes))
	for name := range section.Volumes {
		if name != storage.VarVolume {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		v := section.Volumes[name]
		uuid := pins.Disks[v.Disk].Partitions[name]
		if uuid == "" {
			log.Printf("volume %s has no partition yet; no units written", name)
			continue
		}
		dev := "/dev/disk/by-partuuid/" + uuid
		// Another disk carrying a copy of the system disk has the same PARTUUIDs; only the
		// boot disk's partitions get these links.
		if v.Disk == storage.SystemDisk {
			dev = "/dev/disk/chalk-boot/" + v.Label
		}
		source, cryptUnit := dev, ""
		if v.Encryption == storage.EncryptionTPM2 {
			cryptUnit = "systemd-cryptsetup@" + escapeName(name) + ".service"
			if err := units.add(name, cryptUnit, "cryptsetup.target", cryptsetupUnit(name, dev, cryptsetup, section.Fallback != storage.FallbackNone)); err != nil {
				return units, err
			}
			source = "/dev/mapper/" + name
		}
		switch {
		case v.Format == "swap":
			err = units.add(name, escapePath(source)+".swap", "swap.target", swapUnit(name, source, cryptUnit))
		case v.Format != "" && v.MountPoint != "":
			err = units.add(name, escapePath(v.MountPoint)+".mount", "local-fs.target", mountUnit(name, source, v, cryptUnit))
		}
		if err != nil {
			return units, err
		}
	}
	return units, nil
}

// add refuses a unit name another volume already uses: one unit would silently replace the
// other, such as two volumes with the same mount point.
func (u Units) add(volume, name, wantedBy, content string) error {
	if other, ok := u.owners[name]; ok {
		return fmt.Errorf("volumes %s and %s both need the unit %s", other, volume, name)
	}
	u.files[name] = content
	u.owners[name] = volume
	u.wants[wantedBy] = append(u.wants[wantedBy], name)
	return nil
}

func (u Units) Write(dir string) error {
	for name, content := range u.files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	for target, units := range u.wants {
		wantsDir := filepath.Join(dir, target+".wants")
		if err := os.MkdirAll(wantsDir, 0o755); err != nil {
			return err
		}
		for _, unit := range units {
			if err := os.Symlink("../"+unit, filepath.Join(wantsDir, unit)); err != nil {
				return err
			}
		}
	}
	return nil
}

const header = "# Written by chalkos-storage from /state/storage.\n"

func deviceUnit(path string) string { return escapePath(path) + ".device" }

// cryptsetupUnit unlocks a volume with its TPM2 key. It waits for tpm2.target, which systemd
// starts once a TPM the firmware reported is usable, but does not pull it in: on a node without
// a TPM that would wait for a device that never appears.
func cryptsetupUnit(name, dev, cryptsetup string, prompt bool) string {
	options := "tpm2-device=auto"
	if !prompt {
		options += ",headless=true"
	}
	return header + fmt.Sprintf(`[Unit]
Description=Unlock chalkos volume %[1]s
DefaultDependencies=no
IgnoreOnIsolate=true
BindsTo=%[2]s
After=%[2]s cryptsetup-pre.target systemd-udevd-kernel.socket tpm2.target
Before=umount.target
Conflicts=umount.target

[Service]
Type=oneshot
RemainAfterExit=yes
TimeoutSec=infinity
KeyringMode=shared
OOMScoreAdjust=500
ExecStart=%[3]s attach '%[1]s' '%[4]s' '-' '%[5]s'
ExecStop=%[3]s detach '%[1]s'
`, name, deviceUnit(dev), cryptsetup, dev, options)
}

func requires(cryptUnit string) string {
	if cryptUnit == "" {
		return ""
	}
	return "Requires=" + cryptUnit + "\nAfter=" + cryptUnit + "\n"
}

func mountUnit(name, source string, v storage.Volume, cryptUnit string) string {
	return header + fmt.Sprintf(`[Unit]
Description=chalkos volume %s
After=local-fs-pre.target
%sWants=systemd-growfs@%s.service

[Mount]
What=%s
Where=%s
Type=%s
Options=nofail
`, name, requires(cryptUnit), escapePath(v.MountPoint), source, v.MountPoint, v.Format)
}

func swapUnit(name, source, cryptUnit string) string {
	return header + fmt.Sprintf(`[Unit]
Description=chalkos swap volume %s
%s
[Swap]
What=%s
Options=nofail
`, name, requires(cryptUnit), source)
}
