package e2e

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/lab"
)

var secureBootEnv = []string{
	"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS_ENROLLED", "CHALKLAB_SB_KEYS", "CHALKLAB_IMAGE_DIR",
}

// TestSecureBootBootsSignedImage boots the signed image with test keys enrolled and installs
// it in place, then resets the VM and checks the TPM unseals STATE and VAR again with their data
// intact.
func TestSecureBootBootsSignedImage(t *testing.T) {
	requireEnv(t, append(secureBootEnv, chalkdEnv...)...)
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{sign: true})
	n := startNode(t, lab.VMConfig{Dir: dir, FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"), Disks: []lab.Disk{disk}})
	vm := n.vm

	assertFacts(t, readFacts(t, vm, 5*time.Minute), map[string]string{"secureboot": "1", "state_tpm2": "1"})
	installInPlace(t, n, "chalklab")
	first := readFacts(t, vm, 5*time.Minute)
	assertFacts(t, first, map[string]string{
		"secureboot":   "1",
		"usr_verity":   "verified",
		"root_fstype":  "tmpfs",
		"slot_b_empty": "2",
		"state_tpm2":   "1",
		"var_tpm2":     "1",
		"state_boots":  "2",
		"var_boots":    "1",
	})

	if err := vm.Reset(); err != nil {
		t.Fatal(err)
	}
	second := readFacts(t, vm, 5*time.Minute)
	assertFacts(t, second, map[string]string{
		"secureboot":  "1",
		"state_boots": "3",
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

// TestVerityRejectsTamperedStore corrupts the store in a correctly signed image and expects
// dm-verity to stop the boot.
func TestVerityRejectsTamperedStore(t *testing.T) {
	requireEnv(t, secureBootEnv...)
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{
		sign: true,
		mutate: func(t *testing.T, raw string, parts []image.Partition) {
			store, err := image.FindPartition(parts, "usr-x86-64")
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(raw, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			garbage := make([]byte, 8192)
			for i := range garbage {
				garbage[i] = 0xa5
			}
			if _, err := f.WriteAt(garbage, store.Offset); err != nil {
				t.Fatal(err)
			}
			t.Logf("corrupted %s at offset %d", filepath.Base(raw), store.Offset)
		},
	})
	vm := startVM(t, dir, os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"), disk)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	failure := regexp.MustCompile(`data block \d+ is corrupted|CHALKTEST done`)
	m, err := vm.Console.WaitFor(ctx, failure)
	if err != nil {
		t.Fatal(err)
	}
	if m[0] == "CHALKTEST done" {
		t.Fatal("tampered image booted to the probe")
	}
}
