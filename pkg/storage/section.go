// Package storage reads a node's storage section and applies it on the node: it resolves and
// pins disks, writes repart definitions, and classifies changes to a recorded section.
package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const (
	// SystemDisk names the disk holding the system region and VAR: the disk the node booted from.
	SystemDisk = "system"
	// VarVolume names the volume mounted at /var.
	VarVolume = "var"
	// EncryptionTPM2 seals the volume's LUKS2 key to the TPM; EncryptionNone leaves it unencrypted.
	EncryptionTPM2 = "tpm2"
	EncryptionNone = "none"
	// FallbackNone means a volume whose TPM2 unsealing fails can only be reset.
	FallbackNone = "none"
)

// Section is the storage section of a node identity, as rendered by the cluster definition and
// recorded on the node in /state/storage/storage.json.
type Section struct {
	Disks   map[string]Disk   `json:"disks"`
	Volumes map[string]Volume `json:"volumes"`
	// Fallback is the keyslot asked for when TPM2 unsealing fails: recovery-key, password or none.
	Fallback string `json:"fallback"`
}

// Disk is a disk the node's volumes live on, with the repart definitions to apply to it.
type Disk struct {
	Ref Ref `json:"ref"`
	// Seed is the UUID repart derives partition UUIDs from.
	Seed string `json:"seed"`
	// Repart maps definition file names to repart.d contents.
	Repart map[string]string `json:"repart"`
}

// Volume is one partition and how the node uses it. Empty strings stand for JSON null.
type Volume struct {
	Disk       string `json:"disk"`
	Label      string `json:"label"`
	Format     string `json:"format"`
	MountPoint string `json:"mountPoint"`
	Encryption string `json:"encryption"`
	Size       string `json:"size"`
}

// Ref references a disk by a /dev path or by a selector.
type Ref struct {
	Path     string
	Selector Selector
}

// Selector matches disks by their properties; empty fields match any disk.
type Selector struct {
	// Model is a glob such as "Samsung SSD 990*".
	Model  string `json:"model,omitempty"`
	Serial string `json:"serial,omitempty"`
	WWN    string `json:"wwn,omitempty"`
	// Size is a comparison such as ">= 1T".
	Size string `json:"size,omitempty"`
	// Type is nvme, ssd or hdd.
	Type string `json:"type,omitempty"`
	// unknown lists the keys a newer chalkos wrote that this one does not know, separated by ", ".
	unknown string
}

// UnknownKeys returns the selector keys this version of chalkos does not know, in sorted order.
func (r Ref) UnknownKeys() []string {
	if r.Selector.unknown == "" {
		return nil
	}
	return strings.Split(r.Selector.unknown, ", ")
}

func (r *Ref) UnmarshalJSON(data []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte(`"`)) {
		return json.Unmarshal(data, &r.Path)
	}
	// Unknown keys are recorded rather than refused, so an older image still reads state a
	// newer one wrote; a selector with unknown keys never matches a disk.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &r.Selector); err != nil {
		return err
	}
	var unknown []string
	for key := range keys {
		if !slices.Contains(selectorKeys, strings.ToLower(key)) {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	r.Selector.unknown = strings.Join(unknown, ", ")
	return nil
}

// selectorKeys are the JSON keys of Selector; encoding/json matches them ignoring case.
var selectorKeys = []string{"model", "serial", "wwn", "size", "type"}

func (r Ref) MarshalJSON() ([]byte, error) {
	if r.Path != "" {
		return json.Marshal(r.Path)
	}
	return json.Marshal(r.Selector)
}

func (r Ref) String() string {
	if r.Path != "" {
		return r.Path
	}
	var parts []string
	for _, f := range []struct{ key, value string }{
		{"model", r.Selector.Model}, {"serial", r.Selector.Serial}, {"wwn", r.Selector.WWN},
		{"size", r.Selector.Size}, {"type", r.Selector.Type},
	} {
		if f.value != "" {
			parts = append(parts, fmt.Sprintf("%s %q", f.key, f.value))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// ReadSection reads a recorded storage section. A missing file is returned as an error that
// matches fs.ErrNotExist. Unknown fields are ignored: after a rollback, an older image boots
// with the section a newer one recorded.
func ReadSection(path string) (Section, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Section{}, err
	}
	var s Section
	if err := json.Unmarshal(data, &s); err != nil {
		return Section{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// WriteDefinitions writes each disk's repart definitions to dir/<disk>/ and removes definition
// files the section no longer contains.
func WriteDefinitions(dir string, s Section) error {
	for name, disk := range s.Disks {
		diskDir := filepath.Join(dir, name)
		if err := os.MkdirAll(diskDir, 0o755); err != nil {
			return err
		}
		old, err := filepath.Glob(filepath.Join(diskDir, "*.conf"))
		if err != nil {
			return err
		}
		for _, path := range old {
			if _, keep := disk.Repart[filepath.Base(path)]; !keep {
				if err := os.Remove(path); err != nil {
					return err
				}
			}
		}
		for file, text := range disk.Repart {
			if err := os.WriteFile(filepath.Join(diskDir, file), []byte(text), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// PartitionTypes returns the lower-case GPT type UUIDs the disk's definitions set with Type=.
func (d Disk) PartitionTypes() map[string]bool {
	types := map[string]bool{}
	for _, text := range d.Repart {
		for _, line := range strings.Split(text, "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(key) == "Type" {
				types[strings.ToLower(strings.TrimSpace(value))] = true
			}
		}
	}
	return types
}

// VolumeOfDefinition returns the volume a definition file belongs to: "60-data.conf" is "data".
func VolumeOfDefinition(file string) string {
	_, name, _ := strings.Cut(strings.TrimSuffix(file, ".conf"), "-")
	return name
}

// DiskNames returns the section's disk names in a stable order, the system disk first.
func (s Section) DiskNames() []string {
	names := make([]string, 0, len(s.Disks))
	for name := range s.Disks {
		if name != SystemDisk {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if _, ok := s.Disks[SystemDisk]; ok {
		names = append([]string{SystemDisk}, names...)
	}
	return names
}
