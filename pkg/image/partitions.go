// Package image reads the layout of chalkos disk images.
package image

import (
	"encoding/json"
	"fmt"
	"os"
)

// Partition is one entry of the repart-output.json systemd-repart writes next to an image.
type Partition struct {
	Type    string `json:"type"`
	Label   string `json:"label"`
	Offset  int64  `json:"offset"`
	RawSize int64  `json:"raw_size"`
}

// ReadPartitions parses a repart-output.json file.
func ReadPartitions(path string) ([]Partition, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parts []Partition
	if err := json.Unmarshal(b, &parts); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return parts, nil
}

// FindPartition returns the first partition with the given repart type, such as "esp".
func FindPartition(parts []Partition, typ string) (Partition, error) {
	for _, p := range parts {
		if p.Type == typ {
			return p, nil
		}
	}
	return Partition{}, fmt.Errorf("no partition of type %q", typ)
}
