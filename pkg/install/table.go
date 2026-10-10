package install

import (
	"context"
	"errors"
	"fmt"

	"github.com/trevex/chalkos/pkg/upgrade"
)

var errNotFound = errors.New("no such partition")

// partition is one entry of a GPT as sfdisk --json prints it.
type partition = upgrade.Partition

type partitionTable struct {
	device     string
	Partitions []partition
}

// readTable reads a disk's GPT.
func (i *Installer) readTable(ctx context.Context, dev string) (partitionTable, error) {
	out, err := i.Run.Run(ctx, "sfdisk", "--json", dev)
	if err != nil {
		return partitionTable{}, fmt.Errorf("read the partition table of %s: %w", dev, err)
	}
	parts, err := upgrade.ParseTable(dev, out)
	if err != nil {
		return partitionTable{}, err
	}
	return partitionTable{device: dev, Partitions: parts}, nil
}

// find returns the one partition with the label and type.
func (t partitionTable) find(label, typ string) (partition, error) {
	return t.only(fmt.Sprintf("label %s", label), func(p partition) bool { return p.Name == label && p.Type == typ })
}

// findType returns the one partition of a type.
func (t partitionTable) findType(typ string) (partition, error) {
	return t.only("type "+typ, func(p partition) bool { return p.Type == typ })
}

func (t partitionTable) only(what string, match func(partition) bool) (partition, error) {
	var found []partition
	for _, p := range t.Partitions {
		if match(p) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return partition{}, fmt.Errorf("%s has no partition with %s: %w", t.device, what, errNotFound)
	case 1:
		return found[0], nil
	default:
		return partition{}, fmt.Errorf("%s has %d partitions with %s, refusing to choose", t.device, len(found), what)
	}
}
