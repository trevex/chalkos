package storage

import "encoding/json"

// Status records what chalkos-storage did at boot, for chalkd to report; it is written to
// /run/chalkos/storage-status.json.
type Status struct {
	// Installed is false when STATE holds no storage section and /var is on tmpfs.
	Installed bool                  `json:"installed"`
	Disks     map[string]DiskStatus `json:"disks"`
}

// DiskStatus is one disk's outcome; Error is empty when its volumes were created and opened.
type DiskStatus struct {
	Device string `json:"device,omitempty"`
	Error  string `json:"error,omitempty"`
}

// WriteStatus replaces the status file atomically.
func WriteStatus(path string, s Status) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}
