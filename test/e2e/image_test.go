package e2e

import (
	"os"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/lab"
)

// TestImageBootsWithoutSecureBoot boots the unsigned image with Secure Boot off and installs it
// in place, then checks the store comes from dm-verity, the root is tmpfs, /etc is read-only,
// first boot added an empty slot B and a TPM-sealed STATE, and the install a TPM-sealed VAR.
func TestImageBootsWithoutSecureBoot(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_IMAGE_DIR"}, chalkdEnv...)...)
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{})
	n := startNode(t, lab.VMConfig{Dir: dir, FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS"), Disks: []lab.Disk{disk}})

	// Maintenance mode: STATE exists, VAR does not.
	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"state_fstype": "ext4",
		"state_tpm2":   "1",
		"state_boots":  "1",
	})
	installInPlace(t, n, "chalklab")

	facts := readFacts(t, n.vm, 5*time.Minute)
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
		"state_boots":  "2",
		"var_boots":    "1",
	})
}
