package e2e

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"
)

var secureBootEnv = []string{
	"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS_ENROLLED", "CHALKLAB_SB_KEYS", "CHALKLAB_IMAGE_DIR",
}

// TestSecureBootBootsSignedImage boots the signed image with test keys enrolled, then resets
// the VM and checks the TPM unseals STATE and VAR again with their data intact.
func TestSecureBootBootsSignedImage(t *testing.T) {
	requireEnv(t, secureBootEnv...)
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{sign: true})
	vm := startVM(t, dir, os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"), disk)

	first := readFacts(t, vm, 5*time.Minute)
	assertFacts(t, first, map[string]string{
		"secureboot":   "1",
		"usr_verity":   "verified",
		"root_fstype":  "tmpfs",
		"slot_b_empty": "2",
		"state_tpm2":   "1",
		"var_tpm2":     "1",
		"state_boots":  "1",
		"var_boots":    "1",
	})

	if err := vm.Reset(); err != nil {
		t.Fatal(err)
	}
	second := readFacts(t, vm, 5*time.Minute)
	assertFacts(t, second, map[string]string{
		"secureboot":  "1",
		"state_boots": "2",
		"var_boots":   "2",
	})
}

// TestSecureBootRejectsUnsignedImage checks the firmware refuses an unsigned systemd-boot.
func TestSecureBootRejectsUnsignedImage(t *testing.T) {
	requireEnv(t, secureBootEnv...)
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{})
	vm := startVM(t, dir, os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"), disk)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := vm.Console.WaitFor(ctx, regexp.MustCompile(`Access Denied`)); err != nil {
		t.Fatal(err)
	}
}
