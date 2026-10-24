package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/tool"
)

// What blkid reports as the TYPE of a LUKS device and of an ext4 file system.
const (
	ContentLUKS = "crypto_LUKS"
	ContentExt4 = "ext4"
)

// unitNames is what Change and DeleteVolume pass Generate for systemd-cryptsetup: they read only
// the units' names, and systemd runs the generator itself.
const unitNames = "systemd-cryptsetup"

// internalError is a failure of the node itself, such as writing STATE or reloading systemd, as
// opposed to a refusal its disks or units cause.
type internalError struct{ error }

func (e internalError) Unwrap() error { return e.error }

// IsInternal reports whether an error of Change or DeleteVolume is a failure of the node itself
// rather than a refusal its disks or units cause.
func IsInternal(err error) bool {
	var ie internalError
	return errors.As(err, &ie)
}

// ApplyAll applies the section as Apply does, but fails when any disk could not take its
// volumes: an install and a change of the node's storage create every volume they record.
func (s *Storage) ApplyAll(ctx context.Context, section storage.Section) (storage.Pins, error) {
	status := storage.Status{Disks: map[string]storage.DiskStatus{}}
	pins, err := s.Apply(ctx, section, &status)
	if err != nil {
		return storage.Pins{}, err
	}
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(status.Disks)) {
		if e := status.Disks[name].Error; e != "" {
			problems = append(problems, fmt.Sprintf("disk %s: %s", name, e))
		}
	}
	if len(problems) > 0 {
		return storage.Pins{}, errors.New(strings.Join(problems, "; "))
	}
	return pins, nil
}

// RecordSection records the section in SectionFile as the storage the node applied. Its callers
// write it once the section's volumes exist and are enrolled, so STATE never records a volume
// the disks lack: every boot sets up what it records, and chalkd compares a delivered section
// with it to find the changes still to make.
func (s *Storage) RecordSection(section storage.Section) error {
	data, err := json.Marshal(section)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.SectionFile(), data, 0o600)
}

// EncryptedVolumes returns the devices of the named volumes that are encrypted, by volume name.
// partition finds a system disk volume's partition by its label; a volume on another disk is
// found by the PARTUUID pins record for it.
func EncryptedVolumes(section storage.Section, pins storage.Pins, names []string, partition func(label string) (string, error)) (map[string]string, error) {
	devices := map[string]string{}
	for _, name := range names {
		v, ok := section.Volumes[name]
		if !ok || v.Encryption != storage.EncryptionTPM2 {
			continue
		}
		if v.Disk == storage.SystemDisk {
			dev, err := partition(v.Label)
			if err != nil {
				return nil, fmt.Errorf("volume %s: %w", name, err)
			}
			devices[name] = dev
			continue
		}
		uuid := pins.Disks[v.Disk].Partitions[name]
		if uuid == "" {
			return nil, fmt.Errorf("volume %s was not created", name)
		}
		devices[name] = storage.PartUUIDPath(uuid)
	}
	return devices, nil
}

// Enroll adds the fallback keyslot to each LUKS device, by volume name, unlocking it with the TPM.
//   - The secret only travels in the environment.
//   - Earlier password slots are replaced, so a repeated install does not pile up keyslots.
//   - TPM2 slots are never wiped.
func (s *Storage) Enroll(ctx context.Context, devices map[string]string, secret string) error {
	for _, name := range slices.Sorted(maps.Keys(devices)) {
		dev := devices[name]
		found, err := Content(ctx, s.Run, dev)
		if err != nil {
			return err
		}
		if found != ContentLUKS {
			return fmt.Errorf("volume %s is meant to be encrypted, but %s holds %q instead of LUKS", name, dev, found)
		}
		if _, err := s.Run.RunWithEnv(ctx, []string{"NEWPASSWORD=" + secret}, "systemd-cryptenroll", "--unlock-tpm2-device=auto", "--password", "--wipe-slot=password", dev); err != nil {
			return fmt.Errorf("enroll the fallback key on %s: %w", name, err)
		}
	}
	return nil
}

// Content returns what blkid finds on a device once udev processed it, such as crypto_LUKS or
// ext4; empty when it finds nothing.
func Content(ctx context.Context, run tool.Runner, dev string) (string, error) {
	if _, err := run.Run(ctx, "udevadm", "settle", "--timeout=30"); err != nil {
		return "", err
	}
	out, err := run.Run(ctx, "blkid", "-p", "-o", "value", "-s", "TYPE", dev)
	if foundNothing(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("probe %s: %w", dev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// foundNothing reports whether blkid failed because it found no signature: it exits with 2 and
// says nothing. It also exits with 2 when it cannot read the device, but then it complains.
func foundNothing(err error) bool {
	var te *tool.Error
	return errors.As(err, &te) && te.Code == 2 && strings.TrimSpace(te.Stderr) == ""
}

// Change makes the additive changes a delivered section makes to the recorded one:
//  1. It stops the units of volumes that move to another mount point.
//  2. repart creates and grows partitions, and new encrypted volumes get the fallback keyslot.
//  3. It records the section, reloads systemd and starts the units of new and moved volumes.
//
// An error IsInternal reports is a failure of the node itself; any other is a refusal.
func (s *Storage) Change(ctx context.Context, recorded, section storage.Section, secret string, changes []storage.Change) error {
	var added []string
	for name := range section.Volumes {
		if _, ok := recorded.Volumes[name]; !ok {
			added = append(added, name)
		}
	}
	sort.Strings(added)
	old, err := s.Generate(unitNames)
	if err != nil {
		return internalError{err}
	}
	var moved []string
	for _, c := range changes {
		if _, ok := recorded.Volumes[c.Volume]; ok && recorded.Volumes[c.Volume].MountPoint != section.Volumes[c.Volume].MountPoint {
			moved = append(moved, c.Volume)
		}
	}
	for _, name := range moved {
		if err := s.stopUnits(ctx, old.Of(name)); err != nil {
			return err
		}
	}

	pins, err := s.ApplyAll(ctx, section)
	if err != nil {
		return err
	}
	if section.Fallback != storage.FallbackNone {
		devices, err := EncryptedVolumes(section, pins, added, func(label string) (string, error) {
			return filepath.Join(s.BootPartitions, label), nil
		})
		if err != nil {
			return err
		}
		if err := s.Enroll(ctx, devices, secret); err != nil {
			return err
		}
	}
	if err := s.RecordSection(section); err != nil {
		return internalError{fmt.Errorf("record the storage section: %w", err)}
	}
	if _, err := s.Run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return internalError{err}
	}
	units, err := s.Generate(unitNames)
	if err != nil {
		return internalError{err}
	}
	var start []string
	for _, name := range append(added, moved...) {
		start = append(start, units.Of(name)...)
	}
	if len(start) > 0 {
		if _, err := s.Run.Run(ctx, "systemctl", append([]string{"start"}, start...)...); err != nil {
			return fmt.Errorf("start the units of new volumes: %w", err)
		}
	}
	return nil
}

func (s *Storage) stopUnits(ctx context.Context, units []string) error {
	if len(units) == 0 {
		return nil
	}
	if _, err := s.Run.Run(ctx, "systemctl", append([]string{"stop"}, units...)...); err != nil {
		return fmt.Errorf("stop %s: %w", strings.Join(units, ", "), err)
	}
	return nil
}

// DeleteVolume stops a volume's units, wipes its partition and deletes it. It first checks that
// the partition lies on the disk the volume's disk name is pinned to and carries the PARTUUID
// recorded for the volume. The link is resolved once, and the commands act on the partition that
// was checked. An error IsInternal reports is a failure of the node itself; any other is a
// refusal.
func (s *Storage) DeleteVolume(ctx context.Context, name string, v storage.Volume, pins storage.Pins) error {
	units, err := s.Generate(unitNames)
	if err != nil {
		return internalError{err}
	}
	pin, ok := pins.Disks[v.Disk]
	uuid := strings.ToLower(pin.Partitions[name])
	if !ok || uuid == "" {
		return fmt.Errorf("volume %s has no recorded partition", name)
	}
	var link, want string
	if v.Disk == storage.SystemDisk {
		link = filepath.Join(s.BootPartitions, v.Label)
		boot, err := s.Host.ResolvePath(s.BootDisk)
		if err != nil {
			return fmt.Errorf("find the boot disk: %w", err)
		}
		want = boot.Name
	} else {
		link = storage.PartUUIDPath(uuid)
		disk, found, err := s.Host.Find(pin.Identity)
		if err != nil || !found {
			return fmt.Errorf("the pinned disk of volume %s is missing", name)
		}
		want = disk.Name
	}
	disk, part, err := s.Host.PartitionOf(link)
	if err != nil {
		return fmt.Errorf("find the partition of volume %s: %w", name, err)
	}
	if disk != want {
		return fmt.Errorf("the partition of volume %s is on %s, not on its pinned disk %s; refusing to touch it", name, disk, want)
	}
	if part.UUID != uuid {
		return fmt.Errorf("%s has the PARTUUID %q, not the %s recorded for volume %s; refusing to touch it", part.Device, part.UUID, uuid, name)
	}
	stop := units.Of(name)
	slices.Reverse(stop)
	if err := s.stopUnits(ctx, stop); err != nil {
		return err
	}
	if _, err := s.Run.Run(ctx, "wipefs", "--all", part.Device); err != nil {
		return internalError{fmt.Errorf("wipe volume %s: %w", name, err)}
	}
	if _, err := s.Run.Run(ctx, "sfdisk", "--lock", "--delete", "/dev/"+disk, strconv.Itoa(part.Number)); err != nil {
		return internalError{fmt.Errorf("delete the partition of volume %s: %w", name, err)}
	}
	delete(pin.Partitions, name)
	pins.Disks[v.Disk] = pin
	if err := storage.WritePins(s.PinsFile(), pins); err != nil {
		return internalError{err}
	}
	return nil
}
