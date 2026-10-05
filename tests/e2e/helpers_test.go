// Package e2e boots chalkos images in chalklab VMs. Tests skip unless the CHALKLAB_*
// variables point at firmware, keys, and images (the dev shell and Nix checks set them).
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"chalkos/internal/imagesign"
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

type diskOpts struct {
	sign   bool
	mutate func(t *testing.T, raw string, parts []imagesign.Partition)
}

// prepareDisk copies the test image into dir, optionally modifies and signs the copy, and
// returns a 16 GiB qcow2 overlay so first-boot repart has room for slot B, STATE, and VAR.
func prepareDisk(t *testing.T, dir string, o diskOpts) string {
	t.Helper()
	ctx := context.Background()
	imageDir := os.Getenv("CHALKLAB_IMAGE_DIR")
	raws, err := filepath.Glob(filepath.Join(imageDir, "*.raw"))
	if err != nil || len(raws) != 1 {
		t.Fatalf("want exactly one .raw in %s, got %v (%v)", imageDir, raws, err)
	}
	parts, err := imagesign.ReadPartitions(filepath.Join(imageDir, "repart-output.json"))
	if err != nil {
		t.Fatal(err)
	}

	raw := filepath.Join(dir, "image.raw")
	if err := lab.CopySparse(ctx, raws[0], raw); err != nil {
		t.Fatal(err)
	}
	if o.mutate != nil {
		o.mutate(t, raw, parts)
	}
	if o.sign {
		esp, err := imagesign.FindPartition(parts, "esp")
		if err != nil {
			t.Fatal(err)
		}
		keys := os.Getenv("CHALKLAB_SB_KEYS")
		if err := imagesign.SignImage(ctx, raw, esp, filepath.Join(keys, "db.key"), filepath.Join(keys, "db.crt")); err != nil {
			t.Fatal(err)
		}
	}

	disk := filepath.Join(dir, "disk.qcow2")
	if err := lab.CreateOverlay(ctx, raw, disk, "16G"); err != nil {
		t.Fatal(err)
	}
	return disk
}

var factRE = regexp.MustCompile(`CHALKTEST ([a-z0-9_]+)=(\S*)`)

// readFacts collects the probe's facts up to its final "done" fact.
func readFacts(t *testing.T, vm *lab.VM, timeout time.Duration) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	facts := map[string]string{}
	for {
		m, err := vm.Console.WaitFor(ctx, factRE)
		if err != nil {
			t.Fatalf("reading probe facts (so far %v): %v", facts, err)
		}
		if m[1] == "done" {
			return facts
		}
		facts[m[1]] = m[2]
	}
}

func assertFacts(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("fact %s = %q, want %q (all facts: %v)", k, got[k], v, got)
		}
	}
}
