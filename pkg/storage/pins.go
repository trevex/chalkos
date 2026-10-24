package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/trevex/chalkos/pkg/atomicfile"
)

// Pins is /state/storage/disks.json: for each disk name, the physical disk its reference
// resolved to and the partitions repart created there. Pinned disks are never resolved again.
type Pins struct {
	Disks map[string]Pin `json:"disks"`
}

// Pin is one disk name's physical disk.
type Pin struct {
	// Ref is the reference the disk was resolved from.
	Ref      Ref      `json:"ref"`
	Identity Identity `json:"identity"`
	// Partitions maps volume names to the PARTUUIDs repart reported.
	Partitions map[string]string `json:"partitions"`
}

// ReadPins reads disks.json; a missing file yields empty pins. Unknown fields are ignored, so an
// older image boots with pins a newer one wrote.
func ReadPins(path string) (Pins, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Pins{Disks: map[string]Pin{}}, nil
	}
	if err != nil {
		return Pins{}, err
	}
	var p Pins
	if err := json.Unmarshal(data, &p); err != nil {
		return Pins{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if p.Disks == nil {
		p.Disks = map[string]Pin{}
	}
	return p, nil
}

// WritePins replaces disks.json atomically, so a power loss leaves the old or the new pins.
func WritePins(path string, p Pins) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(data, '\n'), 0o600)
}
