// Package install turns a node in maintenance mode into an installed node: in place, when the
// node's image already runs from the boot disk, or from the installer, which first lays out a
// target disk and writes the node's image's parts to it. Every step can be repeated, so an
// interrupted install can run again; the node counts as installed once STATE holds the installed
// marker, written last.
package install

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/gpt"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/uki"
)

// Installer holds what installing works on; tests point it at temporary directories and a
// fake runner.
type Installer struct {
	Run  tool.Runner
	Host storage.Host
	// StateDir is where the target's STATE is mounted while installing.
	StateDir string
	// BootDisk is udev's link to the disk the running image booted from.
	BootDisk string
	// Definitions holds the running image's repart definitions of the system region.
	Definitions string
	// WorkDir holds definitions and status while installing.
	WorkDir string
	// MountInfo is the mount table of chalkd's mount namespace.
	MountInfo string
	// OpenDisk opens a disk for wiping, waiting for its lock a bounded time or until ctx is done.
	OpenDisk func(ctx context.Context, path string) (Disk, error)
	// Architecture is the machine's, as images name theirs: x86-64 or arm64.
	Architecture string
	// EFIVars is where efivarfs is, whose db and dbx the image's UKI and boot loader are checked
	// against when Secure Boot is on.
	EFIVars string
	// OpenPartition opens a partition of the target disk for its store slots.
	OpenPartition func(p gpt.Partition, write bool) (storeslot.PartitionFile, error)
	// Change, when set, is called before each change to the target disk, its ESP, STATE and the
	// firmware's boot entries; tests make it fail to stop an install at each of them.
	Change func(what string) error
	Now    func() time.Time
}

func (i *Installer) change(what string) error {
	if i.Change == nil {
		return nil
	}
	return i.Change(what)
}

// ForNode installs the running node in place, on the STATE it booted with.
func ForNode() *Installer { return defaults("/state") }

// ForInstaller installs a node from the installer onto a target disk, whose STATE it mounts in
// chalkd's runtime directory.
func ForInstaller() *Installer { return defaults("/run/chalkd/target") }

func defaults(stateDir string) *Installer {
	return &Installer{
		Run:           tool.ExecRunner{},
		Host:          storage.DefaultHost(),
		StateDir:      stateDir,
		BootDisk:      "/dev/disk/chalk-boot-disk",
		Definitions:   "/etc/chalkos/repart.d",
		WorkDir:       "/run/chalkd/install",
		MountInfo:     "/proc/self/mountinfo",
		OpenDisk:      openExclusive,
		Architecture:  uki.GoArchitecture(runtime.GOARCH),
		EFIVars:       "/sys/firmware/efi/efivars",
		OpenPartition: storeslot.OpenPartition,
		Now:           time.Now,
	}
}

// InPlace installs the node on the disk it booted from, where the node's image already runs.
func (i *Installer) InPlace(ctx context.Context, req Request, rootHash []byte) error {
	if err := req.validate(); err != nil {
		return err
	}
	if len(rootHash) == 0 {
		return errors.New("the request names no root hash of the running image, whose config slot the install writes")
	}
	if installed, err := Installed(i.StateDir); err != nil {
		return err
	} else if installed {
		return errors.New("the node is installed already")
	}
	boot, err := i.Host.ResolvePath(i.BootDisk)
	if err != nil {
		return fmt.Errorf("find the disk the node booted from: %w", err)
	}
	ref := req.Section.Disks[storage.SystemDisk].Ref
	system, err := i.Host.Resolve(ref)
	if err != nil {
		return fmt.Errorf("resolve the system disk %s: %w", ref, err)
	}
	if system.Name != boot.Name {
		return fmt.Errorf("the config places the system on %s, but the node runs from %s; install it with the installer instead", system, boot)
	}
	defs, err := readDefinitions(i.Definitions)
	if err != nil {
		return err
	}
	if err := i.closeState(ctx); err != nil {
		return err
	}
	if err := i.installOn(ctx, boot, defs, req, rootHash); err != nil {
		return err
	}
	if err := i.randomizeESP(ctx, boot); err != nil {
		return err
	}
	return i.markInstalled()
}

// installOn runs the steps both flows share on disk, which holds the node's image: STATE, the
// node's volumes, the fallback key and the config slot of the image whose store has the root
// hash, with the identity. defs are the image's repart definitions of the system region.
func (i *Installer) installOn(ctx context.Context, disk storage.BlockDisk, defs map[string]string, req Request, rootHash []byte) error {
	state, err := i.openState(ctx, disk, defs, req.Section.Encryption)
	if err != nil {
		return err
	}
	st := i.storage(disk)
	pins, err := i.applyStorage(ctx, st, req.Section)
	if err != nil {
		return err
	}
	if err := i.enrollFallback(ctx, st, disk, req, pins, state); err != nil {
		return err
	}
	if err := st.RecordSection(req.Section); err != nil {
		return err
	}
	return i.writeIdentity(req, rootHash)
}

// applyStorage creates the node's volumes on its disks: every disk must take its own.
func (i *Installer) applyStorage(ctx context.Context, st *node.Storage, section storage.Section) (storage.Pins, error) {
	if err := i.change("apply the storage section"); err != nil {
		return storage.Pins{}, err
	}
	return st.ApplyAll(ctx, section)
}

// enrollFallback adds the fallback keyslot to STATE, at the device state, and to every encrypted
// volume, unless the node has no fallback.
func (i *Installer) enrollFallback(ctx context.Context, st *node.Storage, disk storage.BlockDisk, req Request, pins storage.Pins, state string) error {
	if req.Section.Fallback == storage.FallbackNone {
		return nil
	}
	devices, err := i.encryptedDevices(ctx, disk, req.Section, pins, state)
	if err != nil {
		return err
	}
	if err := i.change("enroll the fallback key"); err != nil {
		return err
	}
	return st.Enroll(ctx, devices, req.FallbackSecret)
}

// encryptedDevices lists the LUKS devices that get the fallback keyslot: STATE and every
// encrypted volume, by volume name. A volume of the system disk is found in the disk's partition
// table: udev's links to the boot disk's partitions do not lead to an installer's target, and a
// PARTUUID may also be on another disk the node was installed on before.
func (i *Installer) encryptedDevices(ctx context.Context, disk storage.BlockDisk, section storage.Section, pins storage.Pins, state string) (map[string]string, error) {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return nil, err
	}
	devices, err := node.EncryptedVolumes(section, pins, slices.Collect(maps.Keys(section.Volumes)), func(label string) (string, error) {
		p, err := table.find(label, storage.PartitionType(label))
		return p.Node, err
	})
	if err != nil {
		return nil, err
	}
	if section.Encryption == storage.EncryptionTPM2 {
		devices[stateLabel] = state
	}
	return devices, nil
}

// storage opens and changes the node's volumes on the disk.
func (i *Installer) storage(disk storage.BlockDisk) *node.Storage {
	return &node.Storage{
		Run:        i.Run,
		Host:       i.Host,
		StateDir:   i.StateDir,
		BootDisk:   disk.Device,
		StatusFile: filepath.Join(i.WorkDir, "storage-status.json"),
	}
}

// slotWriter writes the target's store slots.
func (i *Installer) slotWriter(target storage.BlockDisk) *storeslot.Writer {
	return &storeslot.Writer{Run: i.Run, Disk: target.Device, OpenPartition: i.OpenPartition, Change: i.Change}
}
