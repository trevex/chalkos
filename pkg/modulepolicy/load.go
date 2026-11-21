package modulepolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// State is what the initrd's loader did with the image's policy.
type State string

const (
	// StateLoaded: the kernel took the signed policy and activated it.
	StateLoaded State = "loaded"
	// StateUnsigned: the image was never signed, so its policy is text the kernel refuses.
	StateUnsigned State = "unsigned"
	// StateFailed: the policy is missing or the kernel refused it, as without its key in db.
	StateFailed State = "failed"
)

// Record is what the initrd's loader leaves for chalkd in /run/chalkos/module-policy.json.
type Record struct {
	State State `json:"state"`
	// Error says why the policy did not load.
	Error string `json:"error,omitempty"`
	// Version is the loaded policy's policy_version, as the kernel reports it.
	Version string `json:"version,omitempty"`
	// Digest is the SHA-256 of the signed policy the kernel took, in lower-case hex.
	Digest string `json:"digest,omitempty"`
}

// Paths are where Load reads and writes: the policy file, securityfs and the record.
type Paths struct {
	Policy, SecurityFS, Record string
}

// InitrdPaths are Load's paths in the initrd.
func InitrdPaths() Paths {
	return Paths{
		Policy:     "/" + PolicyFile,
		SecurityFS: "/sys/kernel/security",
		Record:     filepath.Join("/run", RecordFile),
	}
}

// Load loads and activates the image's signed policy through securityfs, as IPE takes it: the
// whole policy in one write to new_policy, then 1 to the policy's active. It never fails the boot:
// it records what happened, and returns the record too. The record is written last, so chalkd
// never finds one of a policy the kernel did not take.
func Load(p Paths) Record {
	r := load(p)
	if b, err := json.Marshal(r); err == nil {
		if err := os.MkdirAll(filepath.Dir(p.Record), 0o755); err == nil {
			// A record that cannot be written leaves chalkd none, which it reports as failed.
			_ = os.WriteFile(p.Record, b, 0o644)
		}
	}
	return r
}

func load(p Paths) Record {
	slot, err := os.ReadFile(p.Policy)
	if err != nil {
		return Record{State: StateFailed, Error: fmt.Sprintf("read the image's module policy: %v", err)}
	}
	signed, blob, err := Trim(slot)
	if err != nil {
		return Record{State: StateFailed, Error: err.Error()}
	}
	if !signed {
		return Record{State: StateUnsigned, Error: "the image is not signed"}
	}
	ipe := filepath.Join(p.SecurityFS, "ipe")
	if err := writeOnce(filepath.Join(ipe, "new_policy"), blob); err != nil {
		return Record{State: StateFailed, Error: fmt.Sprintf("the kernel refused the module policy: %v", err)}
	}
	dir := filepath.Join(ipe, "policies", Name)
	if err := writeOnce(filepath.Join(dir, "active"), []byte("1")); err != nil {
		return Record{State: StateFailed, Error: fmt.Sprintf("activate the module policy: %v", err)}
	}
	version, err := os.ReadFile(filepath.Join(dir, "version"))
	if err != nil {
		return Record{State: StateFailed, Error: fmt.Sprintf("read the module policy's version: %v", err)}
	}
	sum := sha256.Sum256(blob)
	return Record{State: StateLoaded, Version: strings.TrimSpace(string(version)), Digest: hex.EncodeToString(sum[:])}
}

// writeOnce writes data to an existing securityfs file in one write(2), as IPE parses each write
// as a whole.
func writeOnce(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != len(data) {
		err = fmt.Errorf("wrote %d of %d bytes", n, len(data))
	}
	return err
}
