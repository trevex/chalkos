package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/hostops"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/upgrade"
)

const (
	stateLabel      = "state"
	installedMarker = "installed"
	stateMapperName = "state"
)

// Installed reports whether STATE, mounted at stateDir, holds the installed marker. Only a
// marker that does not exist means not installed; any other error is returned, so callers
// never take an unreadable STATE for an empty one.
func Installed(stateDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(stateDir, installedMarker))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("check whether the node is installed: %w", err)
	}
}

// closeState unmounts STATE from StateDir and closes its LUKS device: the STATE the node booted
// with, so it can be recreated, or the target's, so the target disk is free again. A STATE that
// is not mounted or not open is fine.
func (i *Installer) closeState(ctx context.Context) error {
	return hostops.Release(ctx, i.Ops, "STATE", i.StateDir, stateMapperName, i.Ops.DetachLUKS)
}

// prepareState makes the disk's STATE match the node's encryption policy. A STATE that does
// not match is empty, as the node is not installed yet, so it is deleted and created again.
func (i *Installer) prepareState(ctx context.Context, disk storage.BlockDisk, defs map[string]string, policy string) error {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	want := hostops.ContentExt4
	if policy == storage.EncryptionTPM2 {
		want = hostops.ContentLUKS
	}
	if state, err := table.find(stateLabel, storage.PartitionType(stateLabel)); err == nil {
		found, err := hostops.Content(ctx, i.Run, state.Node)
		if err != nil {
			return err
		}
		if found == want {
			return nil
		}
		log.Printf("STATE holds %q, but the node's encryption policy is %s; creating it again", found, policy)
		if _, err := i.sfdisk(disk.Device).Exec(ctx, "--delete", disk.Device, strconv.Itoa(state.Number)); err != nil {
			return fmt.Errorf("delete STATE: %w", err)
		}
	} else if !errors.Is(err, errNotFound) {
		return err
	}

	dir := filepath.Join(i.WorkDir, "system")
	if err := writeDefinitions(dir, defs, policy); err != nil {
		return err
	}
	if _, err := tool.Run(ctx, i.Run, "systemd-repart", "--dry-run=no", "--seed=random", "--definitions="+dir, "--tpm2-pcrs=7", disk.Device); err != nil {
		return fmt.Errorf("create the system region on %s: %w", disk.Device, err)
	}
	return nil
}

// checkState refuses a target whose STATE belongs to an installed node or to another machine:
// one that holds the installed marker, or that this machine's TPM does not open. A STATE that
// holds no file system yet is installed over. It reports whether STATE holds a config slot, which
// an earlier attempt writes once the image's checker passed its config.
func (i *Installer) checkState(ctx context.Context, target storage.BlockDisk, table partitionTable) (bool, error) {
	state, err := table.find(stateLabel, storage.PartitionType(stateLabel))
	if err != nil {
		return false, err
	}
	found, err := hostops.Content(ctx, i.Run, state.Node)
	if err != nil {
		return false, err
	}
	switch found {
	case "":
		return false, nil
	case hostops.ContentLUKS, hostops.ContentExt4:
	default:
		return false, fmt.Errorf("STATE on the target disk %s holds %q; pass --wipe-disk to replace it", target, found)
	}
	if err := i.openStateReadOnly(ctx, state.Node, found == hostops.ContentLUKS); err != nil {
		return false, fmt.Errorf("STATE on the target disk %s does not open on this machine, so it belongs to another node: %w; pass --wipe-disk to replace it", target, err)
	}
	installed, err := Installed(i.StateDir)
	var slots []os.DirEntry
	if err == nil {
		slots, err = os.ReadDir(filepath.Join(i.StateDir, "config"))
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	}
	if cerr := i.closeState(ctx); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if installed {
		return false, fmt.Errorf("the target disk %s holds an installed node; pass --wipe-disk to replace it", target)
	}
	return len(slots) > 0, nil
}

// openStateReadOnly opens STATE to look at it, without writing to the disk: LUKS attached
// read-only with the TPM alone, no file system check, and the file system mounted read-only
// without replaying its journal.
func (i *Installer) openStateReadOnly(ctx context.Context, dev string, luks bool) error {
	source := dev
	if luks {
		if err := i.Ops.AttachLUKS(ctx, stateMapperName, dev, hostops.LUKSOptions{ReadOnly: true}); err != nil {
			return err
		}
		source = hostops.Mapper(stateMapperName)
	}
	if err := os.MkdirAll(i.StateDir, 0o755); err != nil {
		return err
	}
	return i.Ops.Mount(ctx, source, i.StateDir, "ext4", "ro,noload")
}

// markInstalled writes the installed marker to STATE, last: until it is there, the node is not
// installed and the install runs again.
func (i *Installer) markInstalled() error {
	if err := i.change("mark the node installed"); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(i.StateDir, installedMarker), []byte(i.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// openState makes the disk's STATE match the encryption policy, opens it and mounts it at
// StateDir. It returns STATE's device.
func (i *Installer) openState(ctx context.Context, disk storage.BlockDisk, defs map[string]string, policy string) (string, error) {
	if err := i.prepareState(ctx, disk, defs, policy); err != nil {
		return "", err
	}
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return "", err
	}
	state, err := table.find(stateLabel, storage.PartitionType(stateLabel))
	if err != nil {
		return "", err
	}
	if err := i.change("open STATE"); err != nil {
		return "", err
	}
	if err := i.storage(disk).Open(ctx, stateMapperName, state.Node, i.StateDir, node.OpenOptions{Encrypted: policy == storage.EncryptionTPM2}); err != nil {
		// prepareState keeps a STATE that matches the policy, so every retry finds this one
		// again; only a wiped disk gets a new one.
		if errors.Is(err, node.ErrNotUnsealed) {
			return "", fmt.Errorf("%w; STATE was sealed on another machine or under other Secure Boot settings, so install the node from the installer with --wipe-disk to replace it", err)
		}
		return "", err
	}
	return state.Node, nil
}

// writeIdentity writes what makes the machine this node to STATE: the node certificate and the
// OS CAs chalkd serves with, the provenance record, the config slot of the image whose store has
// the root hash, with the identity, and the layers' payloads.
func (i *Installer) writeIdentity(req Request, rootHash []byte) error {
	if err := i.change("write the identity"); err != nil {
		return err
	}
	// chalkd/ holds the node's private key.
	chalkdDir := filepath.Join(i.StateDir, "chalkd")
	if err := os.MkdirAll(chalkdDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(chalkdDir, 0o700); err != nil {
		return err
	}
	nodeFile, err := req.nodeFile()
	if err != nil {
		return err
	}
	files := []struct {
		path string
		data []byte
		perm fs.FileMode
	}{
		// One file, so the chain and the key are only ever replaced together.
		{"chalkd/node.pem", nodeFile, 0o600},
		{"chalkd/ca.crt", req.CA, 0o644},
	}
	for _, f := range files {
		if err := atomicfile.WriteFile(filepath.Join(i.StateDir, f.path), f.data, f.perm); err != nil {
			return err
		}
	}
	if len(req.Images) > 0 {
		if err := atomicfile.WriteFile(filepath.Join(i.StateDir, upgrade.ImagesRecord), req.Images, 0o644); err != nil {
			return err
		}
	}
	// The identity is in the slot, so the boot applies it with the documents it came from.
	if err := slot.Write(slot.Dir(filepath.Join(i.StateDir, "config"), rootHash), req.Config); err != nil {
		return fmt.Errorf("store the config: %w", err)
	}
	for _, write := range req.Layers {
		if err := write(i.StateDir); err != nil {
			return err
		}
	}
	return nil
}

// RemoveState deletes STATE from the disk the node booted from, which must not be installed, so
// its next boot creates STATE again, sealed to the TPM as the firmware measures the boot then. A
// change of the Secure Boot keys changes PCR 7, so the TPM would no longer unseal the STATE the
// node booted with. A disk without STATE is left as it is.
func (i *Installer) RemoveState(ctx context.Context) error {
	if installed, err := Installed(i.StateDir); err != nil {
		return err
	} else if installed {
		return errors.New("the node is installed")
	}
	boot, err := i.Host.ResolvePath(i.BootDisk)
	if err != nil {
		return fmt.Errorf("find the disk the node booted from: %w", err)
	}
	table, err := i.readTable(ctx, boot.Device)
	if err != nil {
		return err
	}
	state, err := table.find(stateLabel, storage.PartitionType(stateLabel))
	if errors.Is(err, errNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if err := i.closeState(ctx); err != nil {
		return err
	}
	if _, err := i.sfdisk(boot.Device).Exec(ctx, "--delete", boot.Device, strconv.Itoa(state.Number)); err != nil {
		return fmt.Errorf("delete STATE: %w", err)
	}
	return nil
}
