// Package gpt reads and changes GUID partition tables with sfdisk, under the disk's lock, and
// names the ESP's partition type.
package gpt

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// TypeESP is the GPT partition type of the ESP. The types of a store slot's partitions depend on
// the architecture, as package arch lists them.
const TypeESP = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"

// Partition is a partition of a disk as sfdisk --json lists it.
type Partition struct {
	Node string `json:"node"`
	// Start and Size count the disk's sectors.
	Start  int64  `json:"start"`
	Size   int64  `json:"size"`
	Type   string `json:"type"`
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Number int    `json:"-"`
	// SectorSize is the disk's sector size in bytes.
	SectorSize int64 `json:"-"`
}

// Bytes is the partition's size in bytes.
func (p Partition) Bytes() int64 { return p.Size * p.SectorSize }

// ParseTable reads a disk's GPT from what sfdisk --json printed for it.
//   - Partition numbers come from the device names sfdisk derives, /dev/vda3 or /dev/nvme0n1p3.
//   - Sizes count the sectors of the table's sector size: 4096 bytes on a 4Kn disk, 512 when
//     sfdisk names none.
//   - Types and UUIDs are in lower case.
func ParseTable(disk string, sfdiskJSON []byte) ([]Partition, error) {
	var dump struct {
		Table struct {
			Label      string      `json:"label"`
			SectorSize int64       `json:"sectorsize"`
			Partitions []Partition `json:"partitions"`
		} `json:"partitiontable"`
	}
	if err := json.Unmarshal(sfdiskJSON, &dump); err != nil {
		return nil, fmt.Errorf("read the partition table of %s: %w", disk, err)
	}
	t := dump.Table
	if t.Label != "gpt" {
		return nil, fmt.Errorf("%s has a %q partition table, not a GPT", disk, t.Label)
	}
	if t.SectorSize == 0 {
		t.SectorSize = 512
	}
	parts := t.Partitions
	for i, p := range parts {
		number, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(p.Node, disk), "p"))
		if err != nil {
			return nil, fmt.Errorf("partition %s of %s: no partition number", p.Node, disk)
		}
		parts[i].Number = number
		parts[i].SectorSize = t.SectorSize
		parts[i].Type = strings.ToLower(p.Type)
		parts[i].UUID = strings.ToLower(p.UUID)
	}
	return parts, nil
}

// FormatUUID formats 16 bytes as a UUID.
func FormatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewUUID returns a random version 4 UUID, for a partition that no other disk may match.
func NewUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return FormatUUID(b)
}
