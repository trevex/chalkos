// Package install turns a node in maintenance mode into an installed node: in place, when the
// node's image already runs from the boot disk, or from the installer, which first lays out a
// target disk and writes the node's image's parts to it. Every step can be repeated, so an
// interrupted install can run again; the node counts as installed once STATE holds the installed
// marker, written last.
package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/trevex/chalkos/pkg/arch"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/gpt"
	"github.com/trevex/chalkos/pkg/storage/hostops"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/tool"
)

// Installer holds the tools and paths an install uses.
type Installer struct {
	Run  tool.Runner
	Host storage.Host
	// Ops mounts the target's file systems and opens its volumes.
	Ops hostops.Ops
	// StateDir is where the target's STATE is mounted while installing.
	StateDir string
	// BootDisk is udev's link to the disk the running image booted from.
	BootDisk string
	// Definitions holds the running image's repart definitions of the system region.
	Definitions string
	// WorkDir holds definitions and status while installing.
	WorkDir string
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
	// firmware's boot entries; its error stops the install there.
	Change func(what string) error
	Now    func() time.Time
}

func (i *Installer) change(what string) error {
	if i.Change == nil {
		return nil
	}
	return i.Change(what)
}

// ForNode installs the running node in place, on the STATE it booted with, mounted at stateDir,
// with chalkd's runtime directory runDir to work in.
func ForNode(stateDir, runDir string) *Installer {
	run := tool.ExecRunner{}
	return &Installer{
		Run:           run,
		Ops:           hostops.System(run),
		Host:          storage.DefaultHost(),
		StateDir:      stateDir,
		BootDisk:      "/dev/disk/chalk-boot-disk",
		Definitions:   "/etc/chalkos/repart.d",
		WorkDir:       filepath.Join(runDir, "install"),
		OpenDisk:      openExclusive,
		Architecture:  arch.Native().Name,
		EFIVars:       "/sys/firmware/efi/efivars",
		OpenPartition: storeslot.OpenPartition,
		Now:           time.Now,
	}
}

// ForInstaller installs a node from the installer onto a target disk, whose STATE it mounts in
// chalkd's runtime directory runDir, where it works too.
func ForInstaller(runDir string) *Installer {
	return ForNode(filepath.Join(runDir, "target"), runDir)
}

// InPlace installs the node on the disk it booted from. rootHash is the image the node boots once
// installed, whose config slot the install writes: the running image, or one req.Place places in
// the inactive slot, which req.ActivateImage makes boot next. The node keeps the disk layout of
// the image it booted from.
func (i *Installer) InPlace(ctx context.Context, req Request, rootHash []byte) error {
	boot, err := i.checkInPlace(req, rootHash)
	if err != nil {
		return err
	}
	if req.Place != nil {
		checked, err := req.Place(ctx)
		if err != nil {
			return err
		}
		var id identity.Identity
		if err := json.Unmarshal(checked.Config.Identity, &id); err != nil {
			return fmt.Errorf("parse the identity the image's checker gave: %w", err)
		}
		req.Section, req.Config, req.Layers = id.Storage, checked.Config, checked.Layers
		// The placed image's checker may translate the storage otherwise than chalkd did.
		if boot, err = i.checkInPlace(req, rootHash); err != nil {
			return err
		}
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
	if req.ActivateImage != nil {
		if err := req.ActivateImage(ctx); err != nil {
			return err
		}
	}
	return i.markInstalled()
}

// checkInPlace refuses an install in place of req before anything changes: a request that does
// not validate or names no identity or root hash, a node installed already, and a system disk
// that is not the one the node booted from, which it returns.
func (i *Installer) checkInPlace(req Request, rootHash []byte) (storage.BlockDisk, error) {
	if err := req.validate(); err != nil {
		return storage.BlockDisk{}, err
	}
	if len(req.Config.Identity) == 0 {
		return storage.BlockDisk{}, errors.New("no identity")
	}
	if len(rootHash) == 0 {
		return storage.BlockDisk{}, errors.New("the request names no root hash of the image the node boots, whose config slot the install writes")
	}
	if installed, err := Installed(i.StateDir); err != nil {
		return storage.BlockDisk{}, err
	} else if installed {
		return storage.BlockDisk{}, errors.New("the node is installed already")
	}
	boot, err := i.Host.ResolvePath(i.BootDisk)
	if err != nil {
		return storage.BlockDisk{}, fmt.Errorf("find the disk the node booted from: %w", err)
	}
	ref := req.Section.Disks[storage.SystemDisk].Ref
	system, err := i.Host.Resolve(ref)
	if err != nil {
		return storage.BlockDisk{}, fmt.Errorf("resolve the system disk %s: %w", ref, err)
	}
	if system.Name != boot.Name {
		return storage.BlockDisk{}, fmt.Errorf("the config places the system on %s, but the node runs from %s; install it with the installer instead", system, boot)
	}
	return boot, nil
}

// installOn runs the steps both flows share on disk: STATE, the node's volumes, the fallback key
// and the config slot of the image of rootHash, with the identity. defs are the image's repart
// definitions of the system region. The config is checked already: in place by the running
// image or the checker of the image chalkd placed in the inactive slot, from the installer by the
// image's checker on slot A's store.
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

// encryptedDevices lists the LUKS devices that get the fallback keyslot: STATE first, then the
// encrypted volumes as EncryptedVolumes orders them. A system disk volume is found in the disk's
// partition table, as udev's boot disk links do not lead to an installer's target and a PARTUUID
// may also be on a disk the node was installed on before.
func (i *Installer) encryptedDevices(ctx context.Context, disk storage.BlockDisk, section storage.Section, pins storage.Pins, state string) ([]node.EncryptedVolume, error) {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return nil, err
	}
	devices, err := node.EncryptedVolumes(section, pins, slices.Collect(maps.Keys(section.Volumes)), func(label string) (string, error) {
		p, err := table.find(label)
		return p.Node, err
	}, false)
	if err != nil {
		return nil, err
	}
	if section.Encryption == storage.EncryptionTPM2 {
		devices = append([]node.EncryptedVolume{{Name: stateLabel, Device: state}}, devices...)
	}
	return devices, nil
}

// storage opens and changes the node's volumes on the disk.
func (i *Installer) storage(disk storage.BlockDisk) *node.Storage {
	return &node.Storage{
		Run:        i.Run,
		Host:       i.Host,
		Ops:        i.Ops,
		StateDir:   i.StateDir,
		BootDisk:   disk.Device,
		StatusFile: filepath.Join(i.WorkDir, "storage-status.json"),
	}
}

// slotWriter writes the target's store slots.
func (i *Installer) slotWriter(target storage.BlockDisk) *storeslot.Writer {
	return &storeslot.Writer{Run: i.Run, Disk: target.Device, OpenPartition: i.OpenPartition, Change: i.Change}
}
