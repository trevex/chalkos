package install

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
	"github.com/trevex/chalkos/pkg/storage/gpt"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/storeslot"
)

// classify tells whether the target is laid out anew: when it is empty or the request wipes it.
// A target that holds what an earlier attempt of this install left is continued, and anything
// else refused, naming what it holds.
func (i *Installer) classify(ctx context.Context, target storage.BlockDisk, l layout, req PartsRequest) (bool, error) {
	if req.WipeDisk {
		return true, nil
	}
	props, err := node.Probe(ctx, i.Run, target.Device)
	if err != nil {
		return false, err
	}
	if len(props) == 0 {
		return true, nil
	}
	if props["PTTYPE"] != "gpt" || props["TYPE"] != "" {
		return false, fmt.Errorf("the target disk %s carries data (%s); pass --wipe-disk to replace it", target, strings.TrimSpace(props["PTTYPE"]+" "+props["TYPE"]))
	}
	table, err := i.readTable(ctx, target.Device)
	if err != nil {
		return false, err
	}
	if len(table.Partitions) == 0 {
		return true, nil
	}
	if err := l.check(table, req.Section); err != nil {
		return false, fmt.Errorf("the target disk %s is not one an earlier install of this image left: %w; pass --wipe-disk to replace it", target, err)
	}
	pairs, err := storeslot.StoreSlots(target.String(), table.Partitions)
	if err != nil {
		return false, err
	}
	if a := pairs[0]; a.Label() != "" && !a.Holds(req.Image.RootHash) {
		return false, fmt.Errorf("slot A of the target disk %s holds the store %s of another image; pass --wipe-disk to replace it", target, a.Label())
	}
	if err := i.checkState(ctx, target, table, req.Section.Encryption); err != nil {
		return false, err
	}
	log.Printf("continuing the install on %s", target)
	return false, nil
}

// refuseBootDisk refuses the disk the installer runs from. Only a missing boot-disk link lets
// the install go ahead without knowing that disk; any other failure to resolve it refuses.
func (i *Installer) refuseBootDisk(target storage.BlockDisk) error {
	boot, err := i.Host.ResolvePath(i.BootDisk)
	if err != nil {
		link := filepath.Join(i.Host.DevRoot, strings.TrimPrefix(i.BootDisk, "/dev/"))
		if _, lerr := os.Lstat(link); errors.Is(lerr, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("find the disk the installer runs from: %w", err)
	}
	if boot.Name == target.Name {
		return fmt.Errorf("the target disk %s is the disk the installer runs from", target)
	}
	return nil
}

// describe names a partition found on the target.
func describe(p gpt.Partition) string {
	switch p.Type {
	case gpt.TypeESP:
		return "ESP"
	case gpt.TypeUsrX86, gpt.TypeUsrArm:
		return "chalkos store"
	case gpt.TypeUsrX86Verity, gpt.TypeUsrArmVerity:
		return "chalkos store verity"
	}
	if p.Type != storage.PartitionType(p.Name) {
		return fmt.Sprintf("type %s, label %q", p.Type, p.Name)
	}
	switch p.Name {
	case stateLabel:
		return "chalkos STATE"
	case "var":
		return "chalkos VAR"
	default:
		return "chalkos volume " + p.Name
	}
}
