package storage

import (
	"encoding/json"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/jsonfile"
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

// ReadPins reads disks.json; a missing file yields empty pins.
func ReadPins(path string) (Pins, error) {
	var p Pins
	if err := jsonfile.Read(path, &p); err != nil {
		return Pins{}, err
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
