package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/trevex/chalkos/pkg/storage"
)

// runGenerate is the systemd generator: it writes units into the generator's normal directory.
func runGenerate(args []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	stateDir := flags.String("state", "/state", "where STATE is mounted")
	cryptsetup := flags.String("cryptsetup", "systemd-cryptsetup", "absolute path of systemd-cryptsetup")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 && flags.NArg() != 3 {
		return errors.New("generate: want the generator directories NORMAL [EARLY LATE]")
	}
	units, err := generate(filepath.Join(*stateDir, "storage"), *cryptsetup)
	if err != nil {
		return err
	}
	return units.write(flags.Arg(0))
}

// unitSet is the generator's output: unit files by name and the units each target wants.
type unitSet struct {
	files map[string]string
	wants map[string][]string
}

// generate builds units for every volume except VAR, which the initrd mounts: a cryptsetup
// service for encrypted volumes, and a mount or swap unit for formatted ones. All are only
// wanted, so a missing disk fails its units without holding up the boot.
func generate(storageDir, cryptsetup string) (unitSet, error) {
	units := unitSet{files: map[string]string{}, wants: map[string][]string{}}
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
		source, cryptUnit := dev, ""
		if v.Encryption == storage.EncryptionTPM2 {
			cryptUnit = "systemd-cryptsetup@" + escapeName(name) + ".service"
			units.add(cryptUnit, "cryptsetup.target", cryptsetupUnit(name, dev, cryptsetup, section.Fallback != storage.FallbackNone))
			source = "/dev/mapper/" + name
		}
		switch {
		case v.Format == "swap":
			units.add(escapePath(source)+".swap", "swap.target", swapUnit(name, source, cryptUnit))
		case v.Format != "" && v.MountPoint != "":
			units.add(escapePath(v.MountPoint)+".mount", "local-fs.target", mountUnit(name, source, v, cryptUnit))
		}
	}
	return units, nil
}

func (u unitSet) add(name, wantedBy, content string) {
	u.files[name] = content
	u.wants[wantedBy] = append(u.wants[wantedBy], name)
}

func (u unitSet) write(dir string) error {
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
After=%[2]s cryptsetup-pre.target systemd-udevd-kernel.socket
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
