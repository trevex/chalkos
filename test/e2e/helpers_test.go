// Package e2e boots chalkos images in chalklab VMs. The dev shell sets the firmware and key
// variables; tests that boot an image skip unless its directory is exported too, e.g.
// export CHALKLAB_IMAGE_DIR=$(nix build .#test-image --no-link --print-out-paths)
// export CHALKLAB_STORAGE_IMAGE_DIR=$(nix build .#test-storage-image --no-link --print-out-paths)
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/imagesign"
	"github.com/trevex/chalkos/pkg/lab"
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
// The directory is kept when the test fails, and the console tail is logged too because
// the directory is unreachable when the test runs in the Nix sandbox.
func vmDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("kept VM directory %s", dir)
			logConsoleTail(t, filepath.Join(dir, "console.log"), 200)
			return
		}
		os.RemoveAll(dir)
	})
	return dir
}

func logConsoleTail(t *testing.T, path string, n int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	t.Logf("last %d lines of %s:\n%s", len(lines), path, strings.Join(lines, "\n"))
}

func startVM(t *testing.T, dir, vars string, disks ...lab.Disk) *lab.VM {
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
	// imageEnv names the variable holding the image directory; empty means CHALKLAB_IMAGE_DIR.
	imageEnv string
	sign     bool
	mutate   func(t *testing.T, raw string, parts []image.Partition)
}

// prepareDisk copies the test image into dir, optionally modifies and signs the copy, and
// returns a 16 GiB qcow2 overlay so first-boot repart has room for slot B, STATE, and VAR.
func prepareDisk(t *testing.T, dir string, o diskOpts) lab.Disk {
	t.Helper()
	ctx := context.Background()
	if o.imageEnv == "" {
		o.imageEnv = "CHALKLAB_IMAGE_DIR"
	}
	imageDir := os.Getenv(o.imageEnv)
	raws, err := filepath.Glob(filepath.Join(imageDir, "*.raw"))
	if err != nil || len(raws) != 1 {
		t.Fatalf("want exactly one .raw in %s, got %v (%v)", imageDir, raws, err)
	}
	parts, err := image.ReadPartitions(filepath.Join(imageDir, "repart-output.json"))
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
		esp, err := image.FindPartition(parts, "esp")
		if err != nil {
			t.Fatal(err)
		}
		keys := os.Getenv("CHALKLAB_SB_KEYS")
		if err := imagesign.SignImage(ctx, raw, esp.Offset, filepath.Join(keys, "db.key"), filepath.Join(keys, "db.crt")); err != nil {
			t.Fatal(err)
		}
	}

	disk := filepath.Join(dir, "disk.qcow2")
	if err := lab.CreateOverlay(ctx, raw, disk, "16G"); err != nil {
		t.Fatal(err)
	}
	return lab.Disk{Path: disk}
}

// prepareDataDisk creates the empty second disk the storage image selects by its serial.
func prepareDataDisk(t *testing.T, dir string) lab.Disk {
	t.Helper()
	path := filepath.Join(dir, "data.qcow2")
	if err := lab.CreateDisk(context.Background(), path, "2G"); err != nil {
		t.Fatal(err)
	}
	return lab.Disk{Path: path, Serial: "chalk-data"}
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
