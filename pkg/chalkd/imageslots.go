package chalkd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/arch"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// imageSlots are the node's two image slots on its boot disk, which upgrades, activations, image
// rollbacks and installs in place of a pulled image change and staged config checks read.
// bootDisk is the node's; tests supply their own.
type imageSlots interface {
	// Install installs an upgrade's image with the config and the fallback secret the upgrade
	// delivers, stages it and, unless stageOnly is set, activates it. Next to the result it returns
	// the config it wrote to the image's slot; none when no slot was written, as for an image the
	// node runs already or the known-good image kept with its config.
	Install(ctx context.Context, h upgrade.Header, config, secret string, stageOnly bool, image io.Reader) (upgrade.Result, imageConfig, error)
	// Activate makes the image staged in the inactive slot, by its root hash, boot next, as
	// upgrade.Node.Activate does.
	Activate(ctx context.Context, rootHash []byte) (upgrade.Result, error)
	// Reactivate makes the image the inactive slot holds, by its root hash, boot next again, as a
	// rollback does; record is called once the rollback's checks passed, before the image is
	// staged, as upgrade.Node.Rollback does.
	Reactivate(ctx context.Context, rootHash []byte, record func() error) (upgrade.Result, error)
	// Read reads the store of an image the inactive slot holds, as an upgrade checks one, and
	// passes it to read with the image's NixOS system in it. The caller holds the Server's lock
	// (s.mu), which bootDisk reads the partition table under.
	Read(ctx context.Context, rootHash []byte, read func(st imageStore, system string) error) error
	// Place installs the image an install in place pulled into the inactive slot and stages it,
	// as Install does with stageOnly, but check, not an upgrade's config, has the image's checker
	// check the config, in the store st with the NixOS system system. An image the slot holds
	// already is checked again. The caller, Install, holds the Server's lock (s.mu) alone and not
	// the lock of upgrades (s.upgrading): Upgrade, ActivateImage, RollbackImage and trials run in
	// normal mode only, and Install in maintenance mode only.
	Place(ctx context.Context, h upgrade.Header, check func(st imageStore, system string) error, image io.Reader) (upgrade.Result, error)
	// ActivatePlaced makes the image Place staged, by its root hash, boot next, as Activate does,
	// once STATE keeps its config slot. The caller holds the Server's lock (s.mu), as for Place.
	ActivatePlaced(ctx context.Context, rootHash []byte) (upgrade.Result, error)
}

// imageSlots are s's image slots: those tests supply, or else the boot disk's.
func (s *Server) imageSlots() imageSlots {
	if s.slots != nil {
		return s.slots
	}
	return bootDisk{s}
}

// bootDisk is the node's boot disk, whose image slots an upgrade.Node changes.
type bootDisk struct{ s *Server }

func (d bootDisk) Install(ctx context.Context, h upgrade.Header, text, secret string, stageOnly bool, image io.Reader) (upgrade.Result, imageConfig, error) {
	s := d.s
	n, err := s.upgradeNode()
	if err != nil {
		return upgrade.Result{}, imageConfig{}, err
	}
	var staged imageConfig
	n.CheckStore = func(st upgrade.Store) error {
		c, err := s.stageConfig(ctx, st, st.Toplevel, h.Platform, h.RootHash, text, secret)
		if err != nil {
			return err
		}
		staged = c
		return nil
	}
	install := n.Upgrade
	if stageOnly {
		install = n.Install
	}
	res, err := install(ctx, h, image)
	return res, staged, err
}

func (d bootDisk) Activate(ctx context.Context, rootHash []byte) (upgrade.Result, error) {
	n, err := d.s.upgradeNode()
	if err != nil {
		return upgrade.Result{}, err
	}
	n.CheckStaged = d.s.checkStagedSlot
	return n.Activate(ctx, rootHash)
}

func (d bootDisk) Reactivate(ctx context.Context, rootHash []byte, record func() error) (upgrade.Result, error) {
	n, err := d.s.upgradeNode()
	if err != nil {
		return upgrade.Result{}, err
	}
	return n.Rollback(ctx, rootHash, d.s.rolledBackTo(), record)
}

func (d bootDisk) Read(ctx context.Context, rootHash []byte, read func(st imageStore, system string) error) error {
	n, err := d.s.upgradeNodeLocked()
	if err != nil {
		return err
	}
	return n.ReadImage(ctx, rootHash, func(st upgrade.Store) error { return read(st, st.Toplevel) })
}

func (d bootDisk) Place(ctx context.Context, h upgrade.Header, check func(st imageStore, system string) error, image io.Reader) (upgrade.Result, error) {
	n, err := d.s.upgradeNodeLocked()
	if err != nil {
		return upgrade.Result{}, err
	}
	n.CheckStore = func(st upgrade.Store) error { return check(st, st.Toplevel) }
	return n.Install(ctx, h, image)
}

func (d bootDisk) ActivatePlaced(ctx context.Context, rootHash []byte) (upgrade.Result, error) {
	n, err := d.s.upgradeNodeLocked()
	if err != nil {
		return upgrade.Result{}, err
	}
	n.CheckStaged = d.s.keepsStagedSlot
	return n.Activate(ctx, rootHash)
}

// upgradeNode is the node an upgrade changes. It reads and changes the boot disk's partition
// table under s.mu, which ApplyConfig and ResetVolume change the boot disk's partitions under.
func (s *Server) upgradeNode() (*upgrade.Node, error) {
	n, err := s.upgradeNodeLocked()
	if err != nil {
		return nil, err
	}
	n.TableLock = &s.mu
	return n, nil
}

// upgradeNodeLocked is upgradeNode for a caller that holds s.mu.
func (s *Server) upgradeNodeLocked() (*upgrade.Node, error) {
	boot, err := s.Host.ResolvePath(s.Paths.BootDisk)
	if err != nil {
		return nil, fmt.Errorf("find the disk the node booted from: %w", err)
	}
	return &upgrade.Node{
		Run:           s.Run,
		Ops:           s.hostOps(),
		Disk:          boot.Device,
		ESP:           s.Paths.ESP,
		EFIVars:       s.Paths.EFIVars,
		Cmdline:       s.Paths.Cmdline,
		StoreMount:    filepath.Join(s.Paths.RunDir, "upgrade-store"),
		OSRelease:     readOSRelease(s.Paths.OSRelease),
		Architecture:  arch.Native().Name,
		OpenPartition: storeslot.OpenPartition,
	}, nil
}
