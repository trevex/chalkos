// Package node applies a node's storage section on the node: it unlocks and mounts STATE,
// pins disks, creates volumes with systemd-repart, mounts VAR, and generates units for the
// other volumes. The initrd, the generator and chalkd share it.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
)

// Storage holds what the storage setup works on; tests point the paths at temporary directories.
type Storage struct {
	Run  Runner
	Host storage.Host
	// StateDir is where STATE is mounted.
	StateDir string
	// VarDir is where VAR is mounted.
	VarDir string
	// BootDisk is the disk holding the system region: udev's link to the disk systemd-boot was
	// loaded from.
	BootDisk string
	// BootPartitions holds udev's links to the boot disk's partitions, by partition label.
	BootPartitions string
	StatusFile     string
}

// Default is the storage setup of the initrd.
func Default() *Storage {
	return &Storage{
		Run:            ExecRunner{},
		Host:           storage.DefaultHost(),
		StateDir:       "/sysroot/state",
		VarDir:         "/sysroot/var",
		BootDisk:       "/dev/disk/chalk-boot-disk",
		BootPartitions: "/dev/disk/chalk-boot",
		StatusFile:     "/run/chalkos/storage-status.json",
	}
}

// StorageDir is where STATE records the storage section, definitions and pins.
func (s *Storage) StorageDir() string { return filepath.Join(s.StateDir, "storage") }

// OpenState unlocks STATE when it is encrypted and mounts it. The fallback is always offered:
// it is recorded on STATE itself, so it cannot be known yet.
func (s *Storage) OpenState(ctx context.Context) error {
	return s.Open(ctx, "state", filepath.Join(s.BootPartitions, "state"), s.StateDir, true, false)
}

// Open unlocks dev as /dev/mapper/<name> if it holds LUKS and mounts the file system at target.
// Without prompt, systemd-cryptsetup only tries the TPM. With encrypted, a device without LUKS
// is refused, so data meant to be encrypted never lands on a plain file system.
func (s *Storage) Open(ctx context.Context, name, dev, target string, prompt, encrypted bool) error {
	if _, err := s.Run.Run(ctx, "udevadm", "wait", "--timeout=60", dev); err != nil {
		return fmt.Errorf("wait for %s: %w", dev, err)
	}
	out, err := s.Run.Run(ctx, "blkid", "-p", "-o", "value", "-s", "TYPE", dev)
	if err != nil {
		return fmt.Errorf("probe %s: %w", dev, err)
	}
	source := dev
	fsType := strings.TrimSpace(string(out))
	if encrypted && fsType != "crypto_LUKS" {
		return fmt.Errorf("volume %s is meant to be encrypted, but %s holds %q instead of LUKS; refusing to mount it", name, dev, fsType)
	}
	if fsType == "crypto_LUKS" {
		options := "tpm2-device=auto"
		if !prompt {
			options += ",headless=true"
		}
		if _, err := s.Run.Run(ctx, "systemd-cryptsetup", "attach", name, dev, "-", options); err != nil {
			if !prompt && unsealFailed(err) {
				return fmt.Errorf("the TPM did not unseal %s and the node has no fallback key; the volume must be reset with chalkctl storage reset <node> %s: %w", name, name, err)
			}
			return fmt.Errorf("unlock %s: %w", name, err)
		}
		source = "/dev/mapper/" + name
	}
	if err := s.checkFileSystem(ctx, name, source); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	if _, err := s.Run.Run(ctx, "mount", "-t", "ext4", source, target); err != nil {
		return fmt.Errorf("mount %s: %w", name, err)
	}
	return nil
}

// checkFileSystem repairs what a crash or power loss left behind before the ext4 file system on
// dev is mounted; nothing else checks it, as it is mounted outside fstab. e2fsck exits with 1
// when it corrected errors.
func (s *Storage) checkFileSystem(ctx context.Context, name, dev string) error {
	_, err := s.Run.Run(ctx, "e2fsck", "-p", dev)
	var te *ToolError
	switch {
	case err == nil, errors.As(err, &te) && te.Code == 1:
		return nil
	case te != nil && te.Code&4 != 0:
		return fmt.Errorf("the file system of %s has errors e2fsck -p cannot repair; check %s by hand: %w", name, dev, err)
	default:
		return fmt.Errorf("check the file system of %s: %w", name, err)
	}
}

// SetUp applies the recorded storage section: it pins disks, runs repart on each, and mounts
// VAR. Only problems with VAR stop the boot; other disks are reported in the status file.
func (s *Storage) SetUp(ctx context.Context) error {
	status := storage.Status{Disks: map[string]storage.DiskStatus{}}
	err := s.setUpVolumes(ctx, &status)
	if werr := storage.WriteStatus(s.StatusFile, status); werr != nil {
		log.Printf("write %s: %v", s.StatusFile, werr)
	}
	return err
}

func (s *Storage) setUpVolumes(ctx context.Context, status *storage.Status) error {
	section, err := storage.ReadSection(filepath.Join(s.StorageDir(), "storage.json"))
	if errors.Is(err, fs.ErrNotExist) {
		log.Print("STATE holds no storage section; /var stays on tmpfs until the node is installed")
		return nil
	}
	if err != nil {
		return err
	}
	status.Installed = true
	pins, err := s.Apply(ctx, section, status)
	if err != nil {
		return err
	}

	if _, ok := pins.Disks[storage.SystemDisk].Partitions[storage.VarVolume]; !ok {
		log.Print("VAR does not exist; /var stays on tmpfs")
		return nil
	}
	prompt := section.Fallback != storage.FallbackNone
	encrypted := section.Volumes[storage.VarVolume].Encryption == storage.EncryptionTPM2
	if err := s.Open(ctx, storage.VarVolume, filepath.Join(s.BootPartitions, storage.VarVolume), s.VarDir, prompt, encrypted); err != nil {
		status.Disks[storage.SystemDisk] = storage.DiskStatus{Device: status.Disks[storage.SystemDisk].Device, Error: err.Error()}
		return err
	}
	// repart grows the partition when its size grows; the file system follows here.
	if _, err := s.Run.Run(ctx, "systemd-growfs", s.VarDir); err != nil {
		log.Printf("grow /var: %v", err)
	}
	return nil
}

// Apply writes the section's repart definitions, pins its disks, and creates and grows the
// volumes on each disk with systemd-repart, recording their PARTUUIDs in the pins it returns.
// It fails only when the system disk cannot be used; problems with other disks are recorded in
// status, so their volumes are missing while the rest of the node works.
func (s *Storage) Apply(ctx context.Context, section storage.Section, status *storage.Status) (storage.Pins, error) {
	if err := storage.WriteDefinitions(filepath.Join(s.StorageDir(), "disks"), section); err != nil {
		return storage.Pins{}, err
	}
	pinsFile := filepath.Join(s.StorageDir(), "disks.json")
	pins, err := storage.ReadPins(pinsFile)
	if err != nil {
		return storage.Pins{}, err
	}

	devices, err := s.locateDisks(ctx, section, &pins, status)
	if err != nil {
		status.Disks[storage.SystemDisk] = storage.DiskStatus{Error: err.Error()}
		return storage.Pins{}, err
	}
	if err := storage.WritePins(pinsFile, pins); err != nil {
		return storage.Pins{}, err
	}

	for _, name := range section.DiskNames() {
		dev, ok := devices[name]
		if !ok {
			continue
		}
		parts, err := s.repart(ctx, name, dev, section.Disks[name])
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
	return pins, storage.WritePins(pinsFile, pins)
}

// locateDisks finds the device of every disk: the boot disk for the system disk, the pinned
// disk for disks resolved before, and the reference's disk otherwise, which it then pins.
// It fails only when the boot disk cannot be found or is not the disk VAR was pinned to.
func (s *Storage) locateDisks(ctx context.Context, section storage.Section, pins *storage.Pins, status *storage.Status) (map[string]string, error) {
	bootDisk, err := s.Host.ResolvePath(s.BootDisk)
	if err != nil {
		return nil, fmt.Errorf("find the boot disk: %w", err)
	}
	system, pinned := pins.Disks[storage.SystemDisk]
	if pinned && !system.Identity.Same(bootDisk.Identity) {
		return nil, fmt.Errorf("VAR lives on the disk %s, which is missing: the node booted from %s", system.Identity, bootDisk)
	}

	devices := map[string]string{}
	claimed := []claim{{storage.SystemDisk, bootDisk}}
	fail := func(name string, err error) {
		log.Printf("disk %s: %v", name, err)
		status.Disks[name] = storage.DiskStatus{Error: err.Error()}
	}

	// A pin the next boot cannot match would stop that boot as if VAR's disk were missing.
	if !pinned && !bootDisk.Identity.Recognisable() {
		fail(storage.SystemDisk, unrecognisable(bootDisk))
	} else {
		system.Ref = section.Disks[storage.SystemDisk].Ref
		if pinned {
			system.Identity, _ = system.Identity.Update(bootDisk.Identity)
		} else {
			system.Identity = bootDisk.Identity
		}
		pins.Disks[storage.SystemDisk] = system
		devices[storage.SystemDisk] = s.BootDisk
	}

	// Every pinned disk is claimed before any reference is resolved, so a new reference can
	// never land on a disk that holds another disk name's volumes, whatever the names' order.
	var unpinned []string
	for _, name := range section.DiskNames() {
		if name == storage.SystemDisk {
			continue
		}
		pin, ok := pins.Disks[name]
		if !ok {
			unpinned = append(unpinned, name)
			continue
		}
		disk, found, err := s.Host.Find(pin.Identity)
		if err == nil && !found {
			err = fmt.Errorf("pinned disk %s is missing", pin.Identity)
		}
		if err == nil {
			err = checkUnclaimed(name, disk, claimed)
		}
		// A missing disk still keeps its identity from being pinned to another name.
		claimed = append(claimed, claim{name, storage.BlockDisk{Device: disk.Device, Identity: pin.Identity}})
		if err != nil {
			fail(name, err)
			continue
		}
		// A pin that matched by port alone may have found another disk in that port, and repart
		// runs with --empty=allow; so a pinned disk is checked like a new one before it is changed.
		if err := s.checkUnused(ctx, disk, section.Disks[name]); err != nil {
			fail(name, fmt.Errorf("pinned disk %s at %s now holds data chalkos did not create; refusing to touch it: %w", name, disk.Device, err))
			continue
		}
		// A pin made before udev reported every identifier gains the missing ones.
		if id, changed := pin.Identity.Update(disk.Identity); changed {
			pin.Identity = id
			pins.Disks[name] = pin
		}
		devices[name] = disk.Device
	}

	if len(unpinned) == 0 {
		return devices, nil
	}
	// Identities come from the udev database, which udev fills in as it processes each disk; a
	// disk pinned before that would be pinned by a weaker identity than it has.
	if _, err := s.Run.Run(ctx, "udevadm", "settle", "--timeout=30"); err != nil {
		for _, name := range unpinned {
			fail(name, fmt.Errorf("udev has not processed every disk, so the reference is resolved on a later boot: %w", err))
		}
		return devices, nil
	}
	for _, name := range unpinned {
		d := section.Disks[name]
		disk, err := s.resolveNew(ctx, d, claimed)
		if err != nil {
			fail(name, err)
			continue
		}
		pins.Disks[name] = storage.Pin{Ref: d.Ref, Identity: disk.Identity, Partitions: map[string]string{}}
		claimed = append(claimed, claim{name, disk})
		devices[name] = disk.Device
	}
	return devices, nil
}

// claim is a physical disk that a disk name holds.
type claim struct {
	name string
	disk storage.BlockDisk
}

// checkUnclaimed refuses a disk that another disk name already holds.
func checkUnclaimed(name string, disk storage.BlockDisk, claimed []claim) error {
	for _, c := range claimed {
		if c.name == name {
			continue
		}
		if (c.disk.Device != "" && c.disk.Device == disk.Device) || c.disk.Identity.Same(disk.Identity) {
			return fmt.Errorf("%s is the disk %s already uses", disk, c.name)
		}
	}
	return nil
}

func unrecognisable(disk storage.BlockDisk) error {
	return fmt.Errorf("%s cannot be recognised again: udev reports no WWN, serial or path for it; it is not pinned and is tried again on the next boot", disk)
}

// resolveNew resolves the reference of a disk that is not pinned yet and checks that the disk
// may be pinned and partitioned.
func (s *Storage) resolveNew(ctx context.Context, d storage.Disk, claimed []claim) (storage.BlockDisk, error) {
	disk, err := s.Host.Resolve(d.Ref)
	if err != nil {
		return storage.BlockDisk{}, err
	}
	if err := checkUnclaimed("", disk, claimed); err != nil {
		return storage.BlockDisk{}, fmt.Errorf("%s: %w", d.Ref, err)
	}
	if !disk.Identity.Recognisable() {
		return storage.BlockDisk{}, unrecognisable(disk)
	}
	if err := s.checkUnused(ctx, disk, d); err != nil {
		return storage.BlockDisk{}, err
	}
	return disk, nil
}

// checkUnused refuses an extra disk unless it is provably unused: blkid finds nothing
// on it, or it carries a GPT whose partitions all have types of this disk's definitions, which
// repart takes over. repart would otherwise write a new partition table over foreign data.
func (s *Storage) checkUnused(ctx context.Context, disk storage.BlockDisk, d storage.Disk) error {
	out, err := s.Run.Run(ctx, "blkid", "-p", "-o", "export", disk.Device)
	var te *ToolError
	// blkid also exits with 2 when it cannot read the disk, but then it complains.
	if errors.As(err, &te) && te.Code == 2 && strings.TrimSpace(te.Stderr) == "" {
		return nil
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
	if props["PTTYPE"] != "gpt" {
		return fmt.Errorf("%s carries data (%s); wipe it or reference another disk", disk, strings.TrimSpace(props["PTTYPE"]+" "+props["TYPE"]))
	}

	out, err = s.Run.Run(ctx, "sfdisk", "--json", disk.Device)
	if err != nil {
		return fmt.Errorf("read the partition table of %s: %w", disk.Device, err)
	}
	var dump struct {
		PartitionTable struct {
			Label      string `json:"label"`
			Partitions []struct {
				Node string `json:"node"`
				Type string `json:"type"`
			} `json:"partitions"`
		} `json:"partitiontable"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		return fmt.Errorf("read the partition table of %s: %w", disk.Device, err)
	}
	if dump.PartitionTable.Label != "gpt" {
		return fmt.Errorf("%s carries a %q partition table where blkid found a GPT; wipe it or reference another disk", disk, dump.PartitionTable.Label)
	}
	types := d.PartitionTypes()
	for _, p := range dump.PartitionTable.Partitions {
		if !types[strings.ToLower(p.Type)] {
			return fmt.Errorf("%s carries data: %s has the partition type %s, which none of the disk's definitions sets; wipe it or reference another disk", disk, p.Node, p.Type)
		}
	}
	return nil
}

// repart creates and grows the disk's partitions and returns the PARTUUID of each volume.
func (s *Storage) repart(ctx context.Context, name, dev string, d storage.Disk) (map[string]string, error) {
	// An empty --seed= makes repart fall back to a seed of its own, so the partition UUIDs
	// would not be the ones the section determines.
	if d.Seed == "" {
		return nil, fmt.Errorf("the storage section has no seed for disk %s", name)
	}
	args := []string{
		"--dry-run=no",
		"--json=short",
		"--definitions=" + filepath.Join(s.StorageDir(), "disks", name),
		"--seed=" + d.Seed,
	}
	if name != storage.SystemDisk {
		// The disk was checked to be unused before it was pinned.
		args = append(args, "--empty=allow")
	}
	out, err := s.Run.Run(ctx, "systemd-repart", append(args, dev)...)
	if err != nil {
		return nil, err
	}
	return storage.ParsePartitions(out)
}

// unsealFailed reports whether systemd-cryptsetup failed because no key was available: the TPM
// did not unseal one and headless mode forbade asking for another. Other failures, such as an
// unreadable device, are no reason to reset the volume.
func unsealFailed(err error) bool {
	var te *ToolError
	if !errors.As(err, &te) {
		return false
	}
	stderr := strings.ToLower(te.Stderr)
	return strings.Contains(stderr, "tpm2") || strings.Contains(stderr, "headless")
}
