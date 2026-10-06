package e2e

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"
)

// TestFirmwareBoots checks the harness itself: OVMF with SMM and a TPM starts, and its
// boot manager output reaches the serial console.
func TestFirmwareBoots(t *testing.T) {
	requireEnv(t, "CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS")
	vm := startVM(t, vmDir(t), os.Getenv("CHALKLAB_OVMF_VARS"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := vm.Console.WaitFor(ctx, regexp.MustCompile(`BdsDxe|UEFI Interactive Shell`)); err != nil {
		t.Fatal(err)
	}
}
