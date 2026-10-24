// Package e2e boots chalkos images in chalklab VMs. The dev shell sets the firmware and key
// variables; tests that boot an image or install a node skip unless these are exported too:
//
//	export CHALKLAB_IMAGE_DIR=$(nix build .#test-image --no-link --print-out-paths)
//	export CHALKLAB_STORAGE_IMAGE_DIR=$(nix build .#test-storage-image --no-link --print-out-paths)
//	export CHALKLAB_UPGRADE_IMAGE_DIR=$(nix build .#test-upgrade-image --no-link --print-out-paths)
//	export CHALKLAB_UNHEALTHY_IMAGE_DIR=$(nix build .#test-unhealthy-image --no-link --print-out-paths)
//	export CHALKLAB_CHALKCTL=$(nix build .#chalkctl --no-link --print-out-paths)/bin/chalkctl
//	export CHALKLAB_CHALKLAB=$(nix build .#chalklab --no-link --print-out-paths)/bin/chalklab
//	export CHALKLAB_SECRETS=$(nix build .#test-secrets --no-link --print-out-paths)/secrets.json
//	export CHALKLAB_FIXTURES=$PWD/test/fixtures/clusters
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/imagesign"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/schema"
)

// TestMain denies io_uring to the QEMU the tests start, as chalklab does, and copies the fixtures.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests runs the tests with a copy of the fixtures, which it removes once they ran.
func runTests(m *testing.M) int {
	if err := lab.DenyIOURing(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dir, err := copyFixtures()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if dir != "" {
		defer os.RemoveAll(dir)
	}
	fixtures = dir
	return m.Run()
}

// fixtures is a copy of CHALKLAB_FIXTURES, the e2e clusters' inventories and config files, with
// the image directories the environment names linked where their inventories point; empty without
// CHALKLAB_FIXTURES.
var fixtures string

// fixtureImagesFile names, by where each of the fixtures' images points, the variable that names
// the image directory to link there. The fixtures check reads it too.
const fixtureImagesFile = "images.json"

// copyFixtures copies the fixtures to a new directory, which chalkctl may read beside the image
// directories it links there. It removes the copy when it fails.
func copyFixtures() (dir string, err error) {
	src := os.Getenv("CHALKLAB_FIXTURES")
	if src == "" {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(src, fixtureImagesFile))
	if err != nil {
		return "", err
	}
	var images map[string]string
	if err := json.Unmarshal(data, &images); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(src, fixtureImagesFile), err)
	}
	if dir, err = os.MkdirTemp("", "chalkos-fixtures-"); err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
			dir = ""
		}
	}()
	if err := copyTree(src, dir); err != nil {
		return "", err
	}
	for link, env := range images {
		target := os.Getenv(env)
		if target == "" {
			continue
		}
		path := filepath.Join(dir, link)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.Symlink(target, path); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// copyTree copies the directories, regular files and links below src into dst, which exists. It
// leaves out what lies below an images directory, such as image directories linked there to
// validate the fixtures by hand: the environment names the images.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir() && d.Name() == "images":
			return fs.SkipDir
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			if slices.Contains(strings.Split(rel, string(filepath.Separator)), "images") {
				return nil
			}
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case !d.Type().IsRegular():
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// inventories are the fixtures' inventories by the names the tests give them: the main cluster,
// the same with an additive and with a destructive change of chalklab's storage, the same with
// chalklab on the upgrade image with config only that image accepts, and the HA cluster.
var inventories = map[string]string{
	"base":        "chalklab/chalkos.yaml",
	"added":       "chalklab/added.yaml",
	"destructive": "chalklab/destructive.yaml",
	"upgrade":     "chalklab/upgrade.yaml",
	"ha":          "chalklab-ha/chalkos.yaml",
}

// inventoryPath is the copied inventory of the name.
func inventoryPath(name string) string {
	return filepath.Join(fixtures, inventories[name])
}

// rootHash is the root hash of the store of the image directory the variable names, in hex, as
// its metadata.json gives it: an image is its root hash. chalkctl names an image by its first 12
// hex digits, and a slot's labels by its first 16.
func rootHash(t *testing.T, env string) string {
	t.Helper()
	md, err := schema.ReadMetadata(os.Getenv(env))
	if err != nil {
		t.Fatal(err)
	}
	return md.RootHash
}

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

// outliving is a subtest's T whose cleanups run when owner ends, so a VM a phase of a test starts
// runs on through the phases after it.
type outliving struct {
	*testing.T
	owner *testing.T
}

func (o outliving) Cleanup(f func()) { o.owner.Cleanup(f) }

func startVM(t *testing.T, dir, vars string, disks ...lab.VMDisk) *lab.VM {
	t.Helper()
	return bootVM(t, lab.VMConfig{Dir: dir, FirmwareVars: vars, Disks: disks})
}

// bootVM starts a VM with the test firmware, a TPM, and the resources an image needs: 2 GiB of
// memory unless the configuration asks for another amount.
func bootVM(t testing.TB, c lab.VMConfig) *lab.VM {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c.Name = "node"
	c.FirmwareCode = os.Getenv("CHALKLAB_OVMF_CODE")
	if c.MemoryMB == 0 {
		c.MemoryMB = 2048
	}
	c.CPUs = 2
	c.TPM = true
	vm, err := lab.StartVM(ctx, c)
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

// prepareDisk returns a 16 GiB qcow2 overlay of the test image in dir, so first-boot repart has
// room for slot B, STATE, and VAR. An image to modify or sign is copied into dir first; otherwise
// the overlay is backed by the image itself, which QEMU only reads.
func prepareDisk(t *testing.T, dir string, o diskOpts) lab.VMDisk {
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

	raw := raws[0]
	if o.mutate != nil || o.sign {
		raw = filepath.Join(dir, "image.raw")
		if err := lab.CopySparse(ctx, raws[0], raw); err != nil {
			t.Fatal(err)
		}
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
	return lab.VMDisk{Path: disk}
}

// signedImage signs a copy of the image directory the variable names in dir with the test's db
// key, as chalkbuild sign does, and returns the copy's path.
func signedImage(t *testing.T, dir, env string) string {
	t.Helper()
	keys := os.Getenv("CHALKLAB_SB_KEYS")
	out := filepath.Join(dir, strings.ToLower(env))
	if _, err := imagesign.SignDirectory(context.Background(), os.Getenv(env), out, filepath.Join(keys, "db.key"), filepath.Join(keys, "db.crt")); err != nil {
		t.Fatal(err)
	}
	return out
}

// prepareDataDisk creates the empty second disk the storage image selects by its serial.
func prepareDataDisk(t *testing.T, dir string) lab.VMDisk {
	t.Helper()
	path := filepath.Join(dir, "data.qcow2")
	if err := lab.CreateDisk(context.Background(), path, "2G"); err != nil {
		t.Fatal(err)
	}
	return lab.VMDisk{Path: path, Serial: "chalk-data"}
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

// poll calls check every interval until it succeeds or timeout has passed, and returns check's
// last error.
func poll(timeout, interval time.Duration, check func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(interval)
	}
}

// waitFor polls check every 5 seconds and fails the test when it has not succeeded within
// timeout.
func waitFor(t *testing.T, timeout time.Duration, what string, check func() error) {
	t.Helper()
	if err := poll(timeout, 5*time.Second, check); err != nil {
		t.Fatalf("waiting for %s: %v", what, err)
	}
}
