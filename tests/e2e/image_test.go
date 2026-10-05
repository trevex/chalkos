package e2e

import (
	"os"
	"testing"
	"time"
)

// TestImageBootsWithoutSecureBoot boots the unsigned image with Secure Boot off and checks
// the store comes from dm-verity and the root is tmpfs.
func TestImageBootsWithoutSecureBoot(t *testing.T) {
	requireEnv(t, "CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_IMAGE_DIR")
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{})
	vm := startVM(t, dir, os.Getenv("CHALKLAB_OVMF_VARS"), disk)

	facts := readFacts(t, vm, 5*time.Minute)
	assertFacts(t, facts, map[string]string{
		"root_fstype": "tmpfs",
		"usr_verity":  "verified",
		"secureboot":  "0",
	})
}
