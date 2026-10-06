package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	return writeFileAtomic(path, append(data, '\n'))
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
