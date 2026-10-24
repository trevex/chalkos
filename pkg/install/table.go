package install

import (
	"context"
	"errors"
	"fmt"

	"github.com/trevex/chalkos/pkg/storage/gpt"
)

var errNotFound = errors.New("no such partition")

type partitionTable struct {
	device     string
	Partitions []gpt.Partition
}

// sfdisk changes a disk's GPT with the disk's lock, as systemd-repart and udev take it.
func (i *Installer) sfdisk(dev string) gpt.Sfdisk {
	return gpt.Sfdisk{Run: i.Run, Disk: dev}
}

// readTable reads a disk's GPT.
func (i *Installer) readTable(ctx context.Context, dev string) (partitionTable, error) {
	parts, err := i.sfdisk(dev).ReadTable(ctx)
	if err != nil {
		return partitionTable{}, err
	}
	return partitionTable{device: dev, Partitions: parts}, nil
}

// find returns the one partition with the label and type.
func (t partitionTable) find(label, typ string) (gpt.Partition, error) {
	return t.only(fmt.Sprintf("label %s", label), func(p gpt.Partition) bool { return p.Name == label && p.Type == typ })
}

// findType returns the one partition of a type.
func (t partitionTable) findType(typ string) (gpt.Partition, error) {
	return t.only("type "+typ, func(p gpt.Partition) bool { return p.Type == typ })
}

func (t partitionTable) only(what string, match func(gpt.Partition) bool) (gpt.Partition, error) {
	var found []gpt.Partition
	for _, p := range t.Partitions {
		if match(p) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return gpt.Partition{}, fmt.Errorf("%s has no partition with %s: %w", t.device, what, errNotFound)
	case 1:
		return found[0], nil
	default:
		return gpt.Partition{}, fmt.Errorf("%s has %d partitions with %s, refusing to choose", t.device, len(found), what)
	}
}
