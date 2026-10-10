package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var errNotFound = errors.New("no such partition")

// partition is one entry of a GPT as sfdisk --json prints it.
type partition struct {
	Node string `json:"node"`
	// Size is in the disk's sectors, as sfdisk counts; Bytes in bytes.
	Size   int64  `json:"size"`
	Type   string `json:"type"`
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Number int    `json:"-"`
	Bytes  int64  `json:"-"`
}

type partitionTable struct {
	device     string
	Label      string      `json:"label"`
	SectorSize int64       `json:"sectorsize"`
	Partitions []partition `json:"partitions"`
}

// readTable reads a disk's GPT. Partition numbers come from the device names sfdisk derives:
// /dev/vda3 or /dev/nvme0n1p3.
func (i *Installer) readTable(ctx context.Context, dev string) (partitionTable, error) {
	out, err := i.Run.Run(ctx, "sfdisk", "--json", dev)
	if err != nil {
		return partitionTable{}, fmt.Errorf("read the partition table of %s: %w", dev, err)
	}
	var dump struct {
		Table partitionTable `json:"partitiontable"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		return partitionTable{}, fmt.Errorf("read the partition table of %s: %w", dev, err)
	}
	t := dump.Table
	if t.Label != "gpt" {
		return partitionTable{}, fmt.Errorf("%s has a %q partition table, not a GPT", dev, t.Label)
	}
	t.device = dev
	if t.SectorSize == 0 {
		t.SectorSize = 512
	}
	for n, p := range t.Partitions {
		number, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(p.Node, dev), "p"))
		if err != nil {
			return partitionTable{}, fmt.Errorf("partition %s of %s: no partition number", p.Node, dev)
		}
		t.Partitions[n].Number = number
		t.Partitions[n].Bytes = p.Size * t.SectorSize
		t.Partitions[n].Type = strings.ToLower(p.Type)
		t.Partitions[n].UUID = strings.ToLower(p.UUID)
	}
	return t, nil
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
