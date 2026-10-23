package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/types"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/ociimage"
)

// consoleFingerprint waits for chalkd to print its certificate's fingerprint on the console.
func consoleFingerprint(t *testing.T, vm *lab.VM) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	m, err := vm.Console.WaitFor(ctx, fingerprintRE)
	if err != nil {
		t.Fatal(err)
	}
	return m[1]
}

// TestInstallerInstallsOntoBlankDisk boots the test cluster's signed installer under Secure Boot
// next to a blank disk and installs a node onto that disk from its image, signed beforehand as
// chalkbuild signs it. The first install streams the image's parts from the host and is cancelled
// while slot A is written. The install run again without --wipe-disk continues it, with the image
// pulled from a registry on the host with the credentials of a Registry in the node's config. The
// node then boots from the disk installed: slot A holding the image under labels of its root
// hash, its UKI as chalkos_1.efi, slot B empty, VAR mounted, STATE and VAR with two keyslots,
// chalkd over mTLS.
func TestInstallerInstallsOntoBlankDisk(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS_ENROLLED", "CHALKLAB_SB_KEYS", "CHALKLAB_INSTALLER_DIR", "CHALKLAB_IMAGE_DIR"}, chalkdEnv...)...)
	root := rootHash(t, "CHALKLAB_IMAGE_DIR")
	start := time.Now()
	dir := vmDir(t)
	reg := startImageRegistry(t, filepath.Join(dir, "registry"), "pull-s3cret")
	installer := prepareDisk(t, dir, diskOpts{imageEnv: "CHALKLAB_INSTALLER_DIR", sign: true})
	targetPath := filepath.Join(dir, "target.qcow2")
	if err := lab.CreateDisk(context.Background(), targetPath, "16G"); err != nil {
		t.Fatal(err)
	}
	n := startNode(t, lab.VMConfig{
		Dir:           dir,
		FirmwareVars:  os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"),
		Disks:         []lab.Disk{installer, {Path: targetPath, Serial: "chalk-target"}},
		GuestForwards: []lab.GuestForward{{Guest: registryGuest, Host: reg.host}},
	})

	fingerprint := consoleFingerprint(t, n.vm)
	if info := waitForChalkd(t, n, 5*time.Minute); !info.Installer || info.ImageId != "chalkos-installer" {
		t.Fatalf("info = %+v, want the installer", info)
	}
	image := signedImage(t, dir, "CHALKLAB_IMAGE_DIR")
	inv, layers := publish(t, reg, image, ociInventory{
		base: "base", out: "install-oci.yaml", directory: "images/test-metal", repo: "chalkos/test",
		files: "nodes/chalklab-target.yaml", registry: "nodes/chalklab-target-registry.yaml",
	})
	install := []string{"install", "chalklab-target", "--fingerprint", fingerprint, "--inventory", inv}
	// An install streamed from the image's directory and cancelled while chalkd writes slot A of
	// the laid-out target continues without --wipe-disk from the registry.
	interrupted := cancelledInstall(t, n, "", append(slices.Clone(install), "--image", image))
	// The store data, hash tree, UKI and boot loader, not the whole raw image.
	m := regexp.MustCompile(`sending (\d+) bytes`).FindStringSubmatch(interrupted)
	if m == nil {
		t.Fatal("chalkctl did not say how many bytes it sends")
	}
	if sent, _ := strconv.ParseInt(m[1], 10, 64); sent >= 400<<20 {
		t.Errorf("chalkctl sent %d bytes, want less than 400 MiB", sent)
	}
	pulled := time.Now()
	out, err := chalkctl(t, n, "", install...)
	if err != nil || !strings.Contains(out, "which the installer pulls from "+registryGuest+"/chalkos/test@sha256:") {
		t.Fatalf("install: %v", err)
	}
	t.Logf("pulled and installed in %v", time.Since(pulled).Round(time.Second))
	for _, mt := range []types.MediaType{ociimage.StoreType, ociimage.BootLoaderType} {
		if _, ok := reg.served.Load(layers[mt]); !ok {
			t.Errorf("the installer did not pull the %s layer from the registry", mt)
		}
	}
	// The second run took the disk up where the first left it, not laid out anew.
	logged, cancelLogged := context.WithTimeout(context.Background(), time.Minute)
	defer cancelLogged()
	if _, err := n.vm.Console.WaitFor(logged, regexp.MustCompile(`continuing the install on `)); err != nil {
		t.Fatalf("the second install did not continue the first: %v", err)
	}

	// The boot entry the installer added makes the VM boot the target.
	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"secureboot":      "1",
		"store_label":     "store_" + root[:16],
		"store_partition": "3",
		"slot_b_empty":    "2",
		"state_fstype":    "ext4",
		"var_fstype":      "ext4",
		"var_disk":        "vdb",
		"var_tpm2":        "1",
		"state_tpm2":      "1",
		"state_keyslots":  "2",
		"var_keyslots":    "2",
		"state_boots":     "1",
		"var_boots":       "1",
	})
	waitForNode(t, n, "chalklab-target")
	if out, err := chalkctl(t, n, "", "status", "chalklab-target", "--inventory", inv); err != nil || !strings.Contains(out, "(the inventory's)") ||
		!strings.Contains(out, "image "+root[:12]+", booted from chalkos_1.efi\n") {
		t.Errorf("status: %v", err)
	}
	t.Logf("the test took %v", time.Since(start).Round(time.Second))
}

// cancelledInstall runs chalkctl with the inventory given, as chalkctl takes it, and the arguments
// until chalkd retires slot A of the target, before it writes the store there, then kills it, and
// waits for chalkd to give the install up while it receives the store. It returns what chalkctl
// printed.
func cancelledInstall(t *testing.T, n *node, inventory string, args []string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := chalkctlContext(ctx, t, n, inventory, args...)
		done <- result{out, err}
	}()
	wait, stop := context.WithTimeout(context.Background(), 10*time.Minute)
	defer stop()
	if _, err := n.vm.Console.WaitFor(wait, regexp.MustCompile(`retired the slot of partitions 2 and 3`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	r := <-done
	if r.err == nil {
		t.Fatal("the install completed before it was cancelled")
	}
	m, err := n.vm.Console.WaitFor(wait, regexp.MustCompile(`install failed: (.*)`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m[1], "receive the store: ") {
		t.Fatalf("the install failed with %q, want it to fail while it receives the store", m[1])
	}
	return r.out
}

// TestInstallerISOBoots signs a copy of the generic installer as chalkbuild sign does, boots its
// ISO as a CD-ROM with Secure Boot on, and checks chalkd serves any client in maintenance mode:
// chalkctl lists the disks of the machine.
func TestInstallerISOBoots(t *testing.T) {
	requireEnv(t, "CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS_ENROLLED", "CHALKLAB_SB_KEYS", "CHALKLAB_GENERIC_INSTALLER_DIR", "CHALKLAB_CHALKCTL")
	dir := vmDir(t)
	installer := signedImage(t, dir, "CHALKLAB_GENERIC_INSTALLER_DIR")
	isos, err := filepath.Glob(filepath.Join(installer, "*.iso"))
	if err != nil || len(isos) != 1 {
		t.Fatalf("want one ISO, found %v (%v)", isos, err)
	}
	blank := filepath.Join(dir, "blank.qcow2")
	if err := lab.CreateDisk(context.Background(), blank, "4G"); err != nil {
		t.Fatal(err)
	}
	n := startNode(t, lab.VMConfig{
		Dir:          dir,
		FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"),
		CDROM:        isos[0],
		Disks:        []lab.Disk{{Path: blank, Serial: "chalk-blank"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	m, err := n.vm.Console.WaitFor(ctx, regexp.MustCompile(`maintenance mode, accepting any client; certificate fingerprint ([0-9a-f]{64})`))
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	deadline := time.Now().Add(2 * time.Minute)
	for {
		out, err = exec.Command(os.Getenv("CHALKLAB_CHALKCTL"), "disks", "--address", n.addr, "--fingerprint", m[1]).CombinedOutput()
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("chalkctl disks:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`/dev/vda\s+4G\s+hdd\s+.*chalk-blank`).Match(out) {
		t.Error("the blank disk is not listed")
	}
	if info := waitForChalkd(t, n, time.Minute); info.SecureBoot != nodev1.SecureBoot_SECURE_BOOT_ENABLED {
		t.Errorf("Secure Boot = %v, want enabled", info.SecureBoot)
	}
}
