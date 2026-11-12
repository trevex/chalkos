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

// TestInstallerInstallsOntoLVMDisk boots the test cluster's signed installer under Secure Boot
// next to a disk that holds an LVM volume group, which the installer leaves inactive, and installs
// a node onto that disk from its image, signed beforehand as chalkbuild signs it. The first
// install wipes the disk with --wipe-disk, streams the image's parts from the host and is
// cancelled while slot A is written. The second, of config with a Greeting that shouts, is refused by the
// image's validator once the image's store is on the disk. The install run again without
// --wipe-disk continues on the disk, with the image pulled from a registry on the host with the
// credentials of a Registry in the node's config. The
// node then boots from the disk installed: slot A holding the image under labels of its root
// hash, its UKI as chalkos_1.efi, slot B empty, VAR mounted, STATE and VAR with two keyslots,
// chalkd over mTLS.
func TestInstallerInstallsOntoLVMDisk(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_LVM_DISK", "CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS_ENROLLED", "CHALKLAB_SB_KEYS", "CHALKLAB_INSTALLER_DIR", "CHALKLAB_IMAGE_DIR"}, chalkdEnv...)...)
	root := rootHash(t, "CHALKLAB_IMAGE_DIR")
	start := time.Now()
	dir := vmDir(t)
	reg := startImageRegistry(t, filepath.Join(dir, "registry"), "pull-s3cret")
	installer := prepareDisk(t, dir, diskOpts{imageEnv: "CHALKLAB_INSTALLER_DIR", sign: true})
	targetPath := filepath.Join(dir, "target.qcow2")
	if err := lab.CreateOverlay(context.Background(), os.Getenv("CHALKLAB_LVM_DISK"), targetPath, "16G"); err != nil {
		t.Fatal(err)
	}
	n := startNode(t, lab.VMConfig{
		Dir:           dir,
		FirmwareVars:  os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"),
		Disks:         []lab.VMDisk{installer, {Path: targetPath, Serial: "chalk-target"}},
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
	// An install streamed from the image's directory wipes the target, whose volume group stayed
	// inactive, and is cancelled while chalkd writes slot A of the laid-out target; it continues
	// without --wipe-disk from the registry.
	interrupted := cancelledInstall(t, n, "", append(slices.Clone(install), "--image", image, "--wipe-disk"))
	// The store data, hash tree, UKI and boot loader, not the whole raw image.
	m := regexp.MustCompile(`sending (\d+) bytes`).FindStringSubmatch(interrupted)
	if m == nil {
		t.Fatal("chalkctl did not say how many bytes it sends")
	}
	if sent, _ := strconv.ParseInt(m[1], 10, 64); sent >= 400<<20 {
		t.Errorf("chalkctl sent %d bytes, want less than 400 MiB", sent)
	}
	// A Greeting that shouts passes chalkctl's checks; the image's validator refuses it once the
	// image's store is on the disk, and the target boots nothing. The refused install took the disk
	// up where the cancelled one left it, not laid out anew, and pulled the store from the registry.
	out, err := chalkctl(t, n, "", "install", "chalklab-target", "--fingerprint", fingerprint, "--inventory", shoutingTarget(t, inv))
	if err == nil || !strings.Contains(out, "Greeting/hello: spec.message: the message shouts; write it in lower case; the target disk boots nothing") {
		t.Fatalf("an install of a Greeting that shouts: %v", err)
	}
	continued := func(which string) {
		t.Helper()
		logged, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := n.vm.Console.WaitFor(logged, regexp.MustCompile(`continuing the install on `)); err != nil {
			t.Fatalf("the %s install did not continue the one before it: %v", which, err)
		}
	}
	continued("refused")
	if _, ok := reg.served.Load(layers[ociimage.StoreType]); !ok {
		t.Errorf("the installer did not pull the %s layer from the registry", ociimage.StoreType)
	}
	// The install of the fixed config continues on the disk too. Slot A holds the store already,
	// so it pulls the layers after it and not the store.
	reg.served.Clear()
	pulled := time.Now()
	out, err = chalkctl(t, n, "", install...)
	if err != nil || !strings.Contains(out, "which the installer pulls from "+registryGuest+"/chalkos/test@sha256:") {
		t.Fatalf("install: %v", err)
	}
	t.Logf("pulled and installed in %v", time.Since(pulled).Round(time.Second))
	continued("final")
	if _, ok := reg.served.Load(layers[ociimage.BootLoaderType]); !ok {
		t.Errorf("the installer did not pull the %s layer from the registry", ociimage.BootLoaderType)
	}
	if _, ok := reg.served.Load(layers[ociimage.StoreType]); ok {
		t.Errorf("the installer pulled the %s layer, which slot A holds already", ociimage.StoreType)
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

// shoutingTarget writes, beside the inventory inv, one whose chalklab-target also gets the
// Greeting that shouts, which the image's validator refuses and chalkctl's checks pass, and its
// token, and returns its path.
func shoutingTarget(t *testing.T, inv string) string {
	t.Helper()
	data, err := os.ReadFile(inv)
	if err != nil {
		t.Fatal(err)
	}
	files := "nodes/chalklab-target.yaml, nodes/chalklab-target-registry.yaml"
	shout := replaceOnce(t, string(data), files+"]", files+", nodes/chalklab/greeting-token.yaml, nodes/chalklab/greeting-shout.yaml]")
	path := filepath.Join(filepath.Dir(inv), "install-shout.yaml")
	if err := os.WriteFile(path, []byte(shout), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
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

// TestInstallerISOBoots signs a copy of the generic installer as chalkbuild sign does and boots
// its ISO as a CD-ROM on firmware in Setup Mode, where chalkd serves any client in maintenance
// mode. chalkctl secureboot enroll writes the test keys, which signed the copy, without --force,
// so chalkd finds the boot loader and UKI the firmware started from the ISO's ESP, and checks
// the option ROM of the NIC that the TPM event log lists, whose digest --option-roms enrols. The
// installer boots again with Secure Boot on, and chalkctl lists the disks of the machine.
func TestInstallerISOBoots(t *testing.T) {
	requireEnv(t, "CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_SB_KEYS", "CHALKLAB_GENERIC_INSTALLER_DIR", "CHALKLAB_CHALKCTL")
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
		FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS"),
		CDROM:        isos[0],
		Disks:        []lab.VMDisk{{Path: blank, Serial: "chalk-blank"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	m, err := n.vm.Console.WaitFor(ctx, regexp.MustCompile(`maintenance mode, accepting any client; certificate fingerprint ([0-9a-f]{64})`))
	if err != nil {
		t.Fatal(err)
	}
	if info := waitForChalkd(t, n, time.Minute); info.SecureBoot != nodev1.SecureBoot_SECURE_BOOT_SETUP_MODE {
		t.Errorf("Secure Boot = %v, want Setup Mode", info.SecureBoot)
	}

	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, os.Getenv("CHALKLAB_CHALKCTL"), args...).CombinedOutput()
		t.Logf("chalkctl %s:\n%s", strings.Join(args, " "), out)
		return string(out), err
	}
	// OVMF loads the option ROM of the virtio NIC, which the test keys do not sign: --option-roms
	// enrols the digests the TPM event log lists, and --db-hash one more.
	hash := strings.Repeat("0123456789abcdef", 4)
	out, err := run("secureboot", "enroll", "--keys", os.Getenv("CHALKLAB_SB_KEYS"), "--address", n.addr, "--fingerprint", m[1], "--option-roms", "--db-hash", hash)
	if err != nil {
		t.Fatalf("secureboot enroll: %v", err)
	}
	// The firmware measured the NIC's option ROM, and db holds its digest and the given one after
	// the enrolment.
	rom := regexp.MustCompile(`option ROMs the firmware measured:\n  sha256:([0-9a-f]{64}) PciRoot\(`).FindStringSubmatch(out)
	if rom == nil {
		t.Fatal("secureboot enroll listed no option ROM")
	}
	_, dbAfter, _ := strings.Cut(out, "db after:\n")
	dbAfter, _, _ = strings.Cut(dbAfter, "KEK before:")
	for _, want := range []string{"  CN=chalkos test db\n", "  sha256:" + rom[1] + "\n", "  sha256:" + hash + "\n"} {
		if !strings.Contains(dbAfter, want) {
			t.Errorf("db after the enrolment holds no %q", want)
		}
	}
	now := regexp.MustCompile(`fingerprint is now ([0-9a-f]{64})`).FindStringSubmatch(out)
	if now == nil {
		t.Fatal("secureboot enroll printed no new fingerprint")
	}

	err = poll(2*time.Minute, 2*time.Second, func() error {
		out, err = run("disks", "--address", n.addr, "--fingerprint", now[1])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`/dev/vda\s+4G\s+hdd\s+.*chalk-blank`).MatchString(out) {
		t.Error("the blank disk is not listed")
	}
	if info := waitForChalkd(t, n, time.Minute); info.SecureBoot != nodev1.SecureBoot_SECURE_BOOT_ENABLED {
		t.Errorf("Secure Boot = %v, want enabled", info.SecureBoot)
	}
}
