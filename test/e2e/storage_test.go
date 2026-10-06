package e2e

import (
	"os"
	"testing"
	"time"
)

// TestStorageVolumes boots the storage image with a second disk and checks each volume of its
// layout, then resets the VM and checks every volume unlocks and mounts again with its data.
func TestStorageVolumes(t *testing.T) {
	requireEnv(t, "CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_STORAGE_IMAGE_DIR")
	dir := vmDir(t)
	system := prepareDisk(t, dir, diskOpts{imageEnv: "CHALKLAB_STORAGE_IMAGE_DIR"})
	data := prepareDataDisk(t, dir)
	vm := startVM(t, dir, os.Getenv("CHALKLAB_OVMF_VARS"), system, data)

	first := readFacts(t, vm, 5*time.Minute)
	assertFacts(t, first, map[string]string{
		// var.size limits VAR, which stays encrypted with TPM2.
		"var_fstype": "ext4",
		"var_tpm2":   "1",
		"var_size":   "2147483648",
		"var_disk":   "vda",
		"var_boots":  "1",
		// An encrypted, mounted volume on the second disk.
		"data_fstype": "xfs",
		"data_tpm2":   "1",
		"data_disk":   "vdb",
		"data_boots":  "1",
		// An unencrypted volume on the system disk.
		"plain_fstype": "ext4",
		"plain_tpm2":   "0",
		"plain_disk":   "vda",
		"plain_size":   "268435456",
		"plain_boots":  "1",
		// A raw block device: unlocked, not mounted.
		"raw_tpm2":    "1",
		"raw_block":   "1",
		"raw_mounted": "0",
	})

	if err := vm.Reset(); err != nil {
		t.Fatal(err)
	}
	second := readFacts(t, vm, 5*time.Minute)
	assertFacts(t, second, map[string]string{
		"state_boots": "2",
		"var_boots":   "2",
		"data_boots":  "2",
		"plain_boots": "2",
		"raw_block":   "1",
		"raw_mounted": "0",
	})
}
