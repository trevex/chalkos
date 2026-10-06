package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
)

// boot holds the paths the initrd modes work on; tests point them at temporary directories.
type boot struct {
	run  runner
	host storage.Host
	// stateDir is where STATE is mounted.
	stateDir string
	// varDir is where VAR is mounted.
	varDir string
	// bootDisk is udev's link to the disk systemd-boot was loaded from.
	bootDisk string
	// bootPartitions holds udev's links to the boot disk's partitions, by partition label.
	bootPartitions string
	statusFile     string
}

func newBoot() *boot {
	return &boot{
		run:            execRunner{},
		host:           storage.DefaultHost(),
		stateDir:       "/sysroot/state",
		varDir:         "/sysroot/var",
		bootDisk:       "/dev/disk/chalk-boot-disk",
		bootPartitions: "/dev/disk/chalk-boot",
		statusFile:     "/run/chalkos/storage-status.json",
	}
}

func (b *boot) storageDir() string { return filepath.Join(b.stateDir, "storage") }

// openState unlocks STATE when it is encrypted and mounts it. The fallback is always offered:
// it is recorded on STATE itself, so it cannot be known yet.
func (b *boot) openState(ctx context.Context) error {
	return b.open(ctx, "state", filepath.Join(b.bootPartitions, "state"), b.stateDir, true)
}

// open unlocks dev as /dev/mapper/<name> if it holds LUKS and mounts the file system at target.
// Without prompt, systemd-cryptsetup only tries the TPM.
func (b *boot) open(ctx context.Context, name, dev, target string, prompt bool) error {
	if _, err := b.run.run(ctx, "udevadm", "wait", "--timeout=60", dev); err != nil {
		return fmt.Errorf("wait for %s: %w", dev, err)
	}
	out, err := b.run.run(ctx, "blkid", "-p", "-o", "value", "-s", "TYPE", dev)
	if err != nil {
		return fmt.Errorf("probe %s: %w", dev, err)
	}
	source := dev
	if strings.TrimSpace(string(out)) == "crypto_LUKS" {
		options := "tpm2-device=auto"
		if !prompt {
			options += ",headless=true"
		}
		if _, err := b.run.run(ctx, "systemd-cryptsetup", "attach", name, dev, "-", options); err != nil {
			if !prompt {
				return fmt.Errorf("the TPM did not unseal %s and the node has no fallback key; the volume must be reset with chalkctl storage reset <node> %s: %w", name, name, err)
			}
			return fmt.Errorf("unlock %s: %w", name, err)
		}
		source = "/dev/mapper/" + name
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	if _, err := b.run.run(ctx, "mount", "-t", "ext4", source, target); err != nil {
		return fmt.Errorf("mount %s: %w", name, err)
	}
	return nil
}

// setUp applies the recorded storage section: it pins disks, runs repart on each, and mounts
// VAR. Only problems with VAR stop the boot; other disks are reported in the status file.
func (b *boot) setUp(ctx context.Context) error {
	status := storage.Status{Disks: map[string]storage.DiskStatus{}}
	err := b.setUpVolumes(ctx, &status)
	if werr := storage.WriteStatus(b.statusFile, status); werr != nil {
		log.Printf("write %s: %v", b.statusFile, werr)
	}
	return err
}

func (b *boot) setUpVolumes(ctx context.Context, status *storage.Status) error {
	section, err := storage.ReadSection(filepath.Join(b.storageDir(), "storage.json"))
	if errors.Is(err, fs.ErrNotExist) {
		log.Print("STATE holds no storage section; /var stays on tmpfs until the node is installed")
		return nil
	}
	if err != nil {
		return err
	}
	status.Installed = true
	if err := storage.WriteDefinitions(filepath.Join(b.storageDir(), "disks"), section); err != nil {
		return err
	}
	pinsFile := filepath.Join(b.storageDir(), "disks.json")
	pins, err := storage.ReadPins(pinsFile)
	if err != nil {
		return err
	}

	devices, err := b.locateDisks(ctx, section, &pins, status)
	if err != nil {
		status.Disks[storage.SystemDisk] = storage.DiskStatus{Error: err.Error()}
		return err
	}
	if err := storage.WritePins(pinsFile, pins); err != nil {
		return err
	}

	for _, name := range section.DiskNames() {
		dev, ok := devices[name]
		if !ok {
			continue
		}
		parts, err := b.repart(ctx, name, dev, section.Disks[name])
		if err != nil {
			log.Printf("disk %s: %v", name, err)
			status.Disks[name] = storage.DiskStatus{Device: dev, Error: err.Error()}
			continue
		}
		pin := pins.Disks[name]
		if pin.Partitions == nil {
			pin.Partitions = map[string]string{}
		}
		for volume, uuid := range parts {
			pin.Partitions[volume] = uuid
		}
		pins.Disks[name] = pin
		status.Disks[name] = storage.DiskStatus{Device: dev}
	}
	if err := storage.WritePins(pinsFile, pins); err != nil {
		return err
	}

	if _, ok := pins.Disks[storage.SystemDisk].Partitions[storage.VarVolume]; !ok {
		log.Print("VAR does not exist; /var stays on tmpfs")
		return nil
	}
	prompt := section.Fallback != storage.FallbackNone
	if err := b.open(ctx, storage.VarVolume, filepath.Join(b.bootPartitions, storage.VarVolume), b.varDir, prompt); err != nil {
		status.Disks[storage.SystemDisk] = storage.DiskStatus{Device: devices[storage.SystemDisk], Error: err.Error()}
		return err
	}
	// repart grows the partition when its size grows; the file system follows here.
	if _, err := b.run.run(ctx, "systemd-growfs", b.varDir); err != nil {
		log.Printf("grow /var: %v", err)
	}
	return nil
}

// locateDisks finds the device of every disk: the boot disk for the system disk, the pinned
// disk for disks resolved before, and the reference's disk otherwise, which it then pins.
// It fails only when the boot disk cannot be found or is not the disk VAR was pinned to.
func (b *boot) locateDisks(ctx context.Context, section storage.Section, pins *storage.Pins, status *storage.Status) (map[string]string, error) {
	bootDisk, err := b.host.ResolvePath(b.bootDisk)
	if err != nil {
		return nil, fmt.Errorf("find the boot disk: %w", err)
	}
	system, pinned := pins.Disks[storage.SystemDisk]
	if pinned && !system.Identity.Same(bootDisk.Identity) {
		return nil, fmt.Errorf("VAR lives on the disk %s, which is missing: the node booted from %s", system.Identity, bootDisk)
	}
	system.Ref = section.Disks[storage.SystemDisk].Ref
	system.Identity = bootDisk.Identity
	pins.Disks[storage.SystemDisk] = system

	devices := map[string]string{storage.SystemDisk: b.bootDisk}
	claimed := map[string]storage.Identity{storage.SystemDisk: bootDisk.Identity}
	for _, name := range section.DiskNames() {
		if name == storage.SystemDisk {
			continue
		}
		disk, err := b.locate(ctx, name, section.Disks[name], *pins, claimed)
		if err != nil {
			log.Printf("disk %s: %v", name, err)
			status.Disks[name] = storage.DiskStatus{Error: err.Error()}
			continue
		}
		if _, ok := pins.Disks[name]; !ok {
			pins.Disks[name] = storage.Pin{Ref: section.Disks[name].Ref, Identity: disk.Identity, Partitions: map[string]string{}}
		}
		claimed[name] = disk.Identity
		devices[name] = disk.Device
	}
	return devices, nil
}

func (b *boot) locate(ctx context.Context, name string, d storage.Disk, pins storage.Pins, claimed map[string]storage.Identity) (storage.BlockDisk, error) {
	if pin, ok := pins.Disks[name]; ok {
		disk, found, err := b.host.Find(pin.Identity)
		if err != nil {
			return storage.BlockDisk{}, err
		}
		if !found {
			return storage.BlockDisk{}, fmt.Errorf("pinned disk %s is missing", pin.Identity)
		}
		return disk, nil
	}
	disk, err := b.host.Resolve(d.Ref)
	if err != nil {
		return storage.BlockDisk{}, err
	}
	for other, id := range claimed {
		if id.Same(disk.Identity) {
			return storage.BlockDisk{}, fmt.Errorf("%s resolves to %s, which disk %s already uses", d.Ref, disk, other)
		}
	}
	if err := b.checkUnused(ctx, disk); err != nil {
		return storage.BlockDisk{}, err
	}
	return disk, nil
}

// checkUnused refuses a newly resolved disk that carries anything but a GPT partition table,
// because repart would write a new partition table over it.
func (b *boot) checkUnused(ctx context.Context, disk storage.BlockDisk) error {
	out, err := b.run.run(ctx, "blkid", "-p", "-o", "export", disk.Device)
	if exitCode(err) == 2 {
		return nil // blkid found no signature: the disk is empty
	}
	if err != nil {
		return fmt.Errorf("probe %s: %w", disk.Device, err)
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	if props["PTTYPE"] == "gpt" {
		return nil
	}
	return fmt.Errorf("%s carries data (%s); wipe it or reference another disk", disk, strings.TrimSpace(props["PTTYPE"]+" "+props["TYPE"]))
}

// repart creates and grows the disk's partitions and returns the PARTUUID of each volume.
func (b *boot) repart(ctx context.Context, name, dev string, d storage.Disk) (map[string]string, error) {
	args := []string{
		"--dry-run=no",
		"--json=short",
		"--definitions=" + filepath.Join(b.storageDir(), "disks", name),
		"--seed=" + d.Seed,
	}
	if name != storage.SystemDisk {
		// The disk was checked to be empty or GPT-partitioned before it was pinned.
		args = append(args, "--empty=allow")
	}
	out, err := b.run.run(ctx, "systemd-repart", append(args, dev)...)
	if err != nil {
		return nil, err
	}
	return storage.ParsePartitions(out)
}
