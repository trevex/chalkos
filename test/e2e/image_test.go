package e2e

import (
	"os"
	"testing"
	"time"
)

// TestImageBootsWithoutSecureBoot boots the unsigned image with Secure Boot off and checks
// the store comes from dm-verity, the root is tmpfs, /etc is read-only, and first boot added
// an empty slot B and TPM-sealed STATE and VAR volumes.
func TestImageBootsWithoutSecureBoot(t *testing.T) {
	requireEnv(t, "CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_IMAGE_DIR")
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{})
	vm := startVM(t, dir, os.Getenv("CHALKLAB_OVMF_VARS"), disk)

	facts := readFacts(t, vm, 5*time.Minute)
	assertFacts(t, facts, map[string]string{
		"root_fstype":  "tmpfs",
		"usr_verity":   "verified",
		"etc_ro":       "1",
		"secureboot":   "0",
		"slot_b_empty": "2",
		"state_fstype": "ext4",
		"var_fstype":   "ext4",
		"state_tpm2":   "1",
		"var_tpm2":     "1",
		"state_boots":  "1",
		"var_boots":    "1",
	})
}
