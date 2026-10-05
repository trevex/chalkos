// Package e2e boots chalkos images in chalklab VMs. Tests skip unless the CHALKLAB_*
// variables point at firmware, keys, and images (the dev shell and Nix checks set them).
package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"chalkos/internal/lab"
)

func requireEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if os.Getenv(name) == "" {
			t.Skipf("%s not set", name)
		}
	}
}

// vmDir returns a short directory path because Unix socket paths are limited to 108 bytes.
// The directory is kept when the test fails so its console log can be inspected.
func vmDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("kept VM directory %s", dir)
			return
		}
		os.RemoveAll(dir)
	})
	return dir
}

func startVM(t *testing.T, dir, vars string, disks ...string) *lab.VM {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	vm, err := lab.StartVM(ctx, lab.VMConfig{
		Name:         "node",
		Dir:          dir,
		FirmwareCode: os.Getenv("CHALKLAB_OVMF_CODE"),
		FirmwareVars: vars,
		Disks:        disks,
		MemoryMB:     2048,
		CPUs:         2,
		TPM:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vm.Stop() })
	return vm
}
