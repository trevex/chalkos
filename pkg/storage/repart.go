package storage

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

// ParsePartitions reads the JSON table `systemd-repart --json=short` prints and returns the
// PARTUUID of each volume, by the definition file the partition was matched to.
func ParsePartitions(out []byte) (map[string]string, error) {
	var rows []struct {
		File string `json:"file"`
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse repart output: %w", err)
	}
	parts := map[string]string{}
	for _, r := range rows {
		if r.File != "" && r.UUID != "" {
			parts[volumeOfDefinition(filepath.Base(r.File))] = r.UUID
		}
	}
	return parts, nil
}
