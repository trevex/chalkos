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
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config/slot"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/storage"
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
	mounted, err := isMounted(i.MountInfo, i.StateDir)
	if err != nil {
		return err
	}
	if mounted {
		_, err := i.Run.Run(ctx, "umount", i.StateDir)
		var te *tool.Error
		if err != nil && !(errors.As(err, &te) && strings.Contains(te.Stderr, "not mounted")) {
			return fmt.Errorf("unmount STATE: %w", err)
		}
	}
	if _, err := os.Stat(filepath.Join(i.Host.DevRoot, "mapper", stateMapperName)); err == nil {
		if _, err := i.Run.Run(ctx, "systemd-cryptsetup", "detach", stateMapperName); err != nil {
			return fmt.Errorf("close STATE: %w", err)
		}
	}
	return nil
}

// prepareState makes the disk's STATE match the node's encryption policy. A STATE that does
// not match is empty, as the node is not installed yet, so it is deleted and created again.
func (i *Installer) prepareState(ctx context.Context, disk storage.BlockDisk, defs map[string]string, policy string) error {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	want := node.ContentExt4
	if policy == storage.EncryptionTPM2 {
		want = node.ContentLUKS
	}
	if state, err := table.find(stateLabel, storage.PartitionType(stateLabel)); err == nil {
		found, err := node.Content(ctx, i.Run, state.Node)
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
	if _, err := i.Run.Run(ctx, "systemd-repart", "--dry-run=no", "--seed=random", "--definitions="+dir, "--tpm2-pcrs=7", disk.Device); err != nil {
		return fmt.Errorf("create the system region on %s: %w", disk.Device, err)
	}
	return nil
}

// checkState refuses a target whose STATE belongs to an installed node or to another machine:
// one that holds the installed marker, or that this machine's TPM does not open. A STATE that
// holds no file system yet is installed over.
func (i *Installer) checkState(ctx context.Context, target storage.BlockDisk, table partitionTable, policy string) error {
	state, err := table.find(stateLabel, storage.PartitionType(stateLabel))
	if err != nil {
		return err
	}
	found, err := node.Content(ctx, i.Run, state.Node)
	if err != nil {
		return err
	}
	switch found {
	case "":
		return nil
	case node.ContentLUKS, node.ContentExt4:
	default:
		return fmt.Errorf("STATE on the target disk %s holds %q; pass --wipe-disk to replace it", target, found)
	}
	if err := i.openStateReadOnly(ctx, state.Node, found == node.ContentLUKS); err != nil {
		return fmt.Errorf("STATE on the target disk %s does not open on this machine, so it belongs to another node: %w; pass --wipe-disk to replace it", target, err)
	}
	installed, err := Installed(i.StateDir)
	if cerr := i.closeState(ctx); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if installed {
		return fmt.Errorf("the target disk %s holds an installed node; pass --wipe-disk to replace it", target)
	}
	return nil
}

// openStateReadOnly opens STATE to look at it, without writing to the disk: LUKS attached
// read-only with the TPM alone, no file system check, and the file system mounted read-only
// without replaying its journal.
func (i *Installer) openStateReadOnly(ctx context.Context, dev string, luks bool) error {
	source := dev
	if luks {
		if _, err := i.Run.Run(ctx, "systemd-cryptsetup", "attach", stateMapperName, dev, "-", "tpm2-device=auto,headless=true,read-only"); err != nil {
			return err
		}
		source = "/dev/mapper/" + stateMapperName
	}
	if err := os.MkdirAll(i.StateDir, 0o755); err != nil {
		return err
	}
	_, err := i.Run.Run(ctx, "mount", "-t", "ext4", "-o", "ro,noload", source, i.StateDir)
	return err
}

// markInstalled writes the installed marker to STATE, last: until it is there, the node is not
// installed and the install runs again.
func (i *Installer) markInstalled() error {
	if err := i.change("mark the node installed"); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(i.StateDir, installedMarker), []byte(i.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// isMounted reports whether a file system is mounted at path, per a mountinfo file.
func isMounted(mountInfo, path string) (bool, error) {
	data, err := os.ReadFile(mountInfo)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && fields[4] == path {
			return true, nil
		}
	}
	return false, nil
}

// WriteShare records the node's Kubernetes share on STATE, in a directory only root can enter.
func WriteShare(stateDir string, share []byte) error {
	dir := filepath.Join(stateDir, "kubernetes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "share.json"), share, 0o600)
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
	if err := i.storage(disk).Open(ctx, stateMapperName, state.Node, i.StateDir, false, policy == storage.EncryptionTPM2); err != nil {
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
// the root hash, with the identity, and the Kubernetes share.
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
	if len(req.KubernetesShare) > 0 {
		// validate already parsed and checked the share; encode it again so STATE holds the
		// canonical form, never whatever bytes the request happened to carry.
		share, err := kpki.ParseShare(req.KubernetesShare)
		if err != nil {
			return err
		}
		canonical, err := share.Encode()
		if err != nil {
			return err
		}
		if err := WriteShare(i.StateDir, canonical); err != nil {
			return err
		}
	}
	return nil
}
