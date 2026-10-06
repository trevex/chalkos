// Package storage reads a node's storage section and applies it on the node: it resolves and
// pins disks, writes repart definitions, and classifies changes to a recorded section.
package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	// Encryption is the node's encryption policy, which STATE follows: tpm2 or none.
	Encryption string `json:"encryption"`
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

// UnmarshalJSON reads a reference written as a /dev path string or as a selector object.
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

// MarshalJSON writes the reference as UnmarshalJSON reads it: the path as a string, or else the
// selector as an object. Unknown selector keys are not written back.
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
// files the section no longer contains. It refuses an invalid section, whose names could lead
// outside dir.
func WriteDefinitions(dir string, s Section) error {
	if err := s.Validate(); err != nil {
		return err
	}
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

// volumeOfDefinition returns the volume a definition file belongs to: "60-data.conf" is "data".
func volumeOfDefinition(file string) string {
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

var (
	// validName is the cluster definition's rule for volume names, which also name disks and
	// label partitions: at most 32 characters, starting and ending with a letter or digit.
	validName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	// validDefinitionFile is a repart.d file name: no path, no hidden file.
	validDefinitionFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.conf$`)
	// validMountPoint keeps mount points free of characters that change a unit file's meaning.
	validMountPoint = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
)

// reservedNames are the labels of the system region and VAR, and the system disk's name. A
// volume with one of the labels would share its link below /dev/disk/chalk-boot with a
// partition of the image; a volume named like the system disk would replace its entry when the
// section is rendered.
var reservedNames = []string{"esp", "store", "store-verity", "state", VarVolume, SystemDisk}

// forbiddenMountPoints hold the root, the store, STATE and VAR; nothing else is mounted on them
// or below /nix and /state.
var forbiddenMountPoints = []string{"/", "/nix", "/state", "/var"}

// Validate checks a section the node did not render itself, as the cluster definition checks
// it: names and labels that are safe in paths and unit names, no reserved labels, definition
// files that stay in their directory, and mount points that are unique and safe in unit files.
func (s Section) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, name := range sortedKeys(s.Disks) {
		if !validName.MatchString(name) {
			add("disk name %q must match %s", name, validName)
			continue
		}
		for _, file := range sortedKeys(s.Disks[name].Repart) {
			if !validDefinitionFile.MatchString(file) {
				add("disk %s: definition file %q must be a plain file name ending in .conf", name, file)
			}
		}
	}
	mounts := map[string]string{}
	for _, name := range sortedKeys(s.Volumes) {
		v := s.Volumes[name]
		if !validName.MatchString(name) {
			add("volume name %q must match %s", name, validName)
			continue
		}
		if v.Label != name {
			add("volume %s has the label %q; a volume's label is its name", name, v.Label)
		}
		if name == VarVolume {
			if v.Disk != SystemDisk || v.MountPoint != "/var" {
				add("VAR must be on the system disk and mounted at /var")
			}
		} else if slices.Contains(reservedNames, name) {
			add("volume name %s is reserved", name)
		}
		if _, ok := s.Disks[v.Disk]; !ok {
			add("volume %s is on the unknown disk %q", name, v.Disk)
		}
		if v.MountPoint == "" {
			continue
		}
		if other, ok := mounts[v.MountPoint]; ok {
			add("volumes %s and %s have the same mount point %s", other, name, v.MountPoint)
		}
		mounts[v.MountPoint] = name
		if name != VarVolume && !safeMountPoint(v.MountPoint) {
			add("volume %s: mount point %q must be an absolute path of A-Z, a-z, 0-9, ., _ and -, other than /, /nix, /state and /var, and not below /nix or /state", name, v.MountPoint)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid storage section: %s", strings.Join(problems, "; "))
	}
	return nil
}

func safeMountPoint(p string) bool {
	if !validMountPoint.MatchString(p) || slices.Contains(forbiddenMountPoints, p) ||
		strings.HasPrefix(p, "/nix/") || strings.HasPrefix(p, "/state/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
