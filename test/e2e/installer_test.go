package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/lab"
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
// next to a blank disk, installs a node onto that disk from the role image's parts, which chalkctl
// signs and sends from the host, cancels that install while slot A is written and runs it again
// without --wipe-disk, and checks the node boots from the disk installed: slot A holding the image,
// slot B empty, VAR mounted, STATE and VAR with two keyslots, chalkd over mTLS.
func TestInstallerInstallsOntoBlankDisk(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS_ENROLLED", "CHALKLAB_SB_KEYS", "CHALKLAB_INSTALLER_DIR", "CHALKLAB_IMAGE_DIR"}, chalkdEnv...)...)
	dir := vmDir(t)
	installer := prepareDisk(t, dir, diskOpts{imageEnv: "CHALKLAB_INSTALLER_DIR", sign: true})
	targetPath := filepath.Join(dir, "target.qcow2")
	if err := lab.CreateDisk(context.Background(), targetPath, "16G"); err != nil {
		t.Fatal(err)
	}
	n := startNode(t, lab.VMConfig{
		Dir:          dir,
		FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"),
		Disks:        []lab.Disk{installer, {Path: targetPath, Serial: "chalk-target"}},
	})

	fingerprint := consoleFingerprint(t, n.vm)
	if info := waitForChalkd(t, n, 5*time.Minute); !info.Installer || info.ImageId != "chalkos-installer" {
		t.Fatalf("info = %+v, want the installer", info)
	}
	keys := os.Getenv("CHALKLAB_SB_KEYS")
	install := []string{"install", "chalklab-target", "--fingerprint", fingerprint, "--image", os.Getenv("CHALKLAB_IMAGE_DIR"),
		"--sign-key", filepath.Join(keys, "db.key"), "--sign-cert", filepath.Join(keys, "db.crt")}
	// An install cancelled while chalkd writes slot A of the laid-out target continues without
	// --wipe-disk.
	interrupted := cancelledInstall(t, n, install)
	out, err := chalkctl(t, n, "base", install...)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	// The second run took the disk up where the first left it, not laid out anew.
	logged, cancelLogged := context.WithTimeout(context.Background(), time.Minute)
	defer cancelLogged()
	if _, err := n.vm.Console.WaitFor(logged, regexp.MustCompile(`continuing the install on `)); err != nil {
		t.Fatalf("the second install did not continue the first: %v", err)
	}
	for _, out := range []string{interrupted, out} {
		// The store data, hash tree, UKI and boot loader, not the whole raw image.
		m := regexp.MustCompile(`sending (\d+) bytes`).FindStringSubmatch(out)
		if m == nil {
			t.Fatal("chalkctl did not say how many bytes it sends")
		}
		if sent, _ := strconv.ParseInt(m[1], 10, 64); sent >= 400<<20 {
			t.Errorf("chalkctl sent %d bytes, want less than 400 MiB", sent)
		}
	}

	// The boot entry the installer added makes the VM boot the target.
	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"secureboot":      "1",
		"store_label":     "store_0.1.0",
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
	if out, err := chalkctl(t, n, "base", "status", "chalklab-target"); err != nil || !strings.Contains(out, "(the cluster definition's)") {
		t.Errorf("status: %v", err)
	}
}

// cancelledInstall runs chalkctl with the arguments until chalkd retires slot A of the target, before
// it writes the store there, then kills it, and waits for chalkd to give the install up while it
// receives the store. It returns what chalkctl printed.
func cancelledInstall(t *testing.T, n *node, args []string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := chalkctlContext(ctx, t, n, "base", args...)
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

// TestInstallerISOBoots signs the generic installer ISO as chalkctl sign signs disk images,
// boots it as a CD-ROM with Secure Boot on, and checks chalkd serves any client in maintenance
// mode: chalkctl lists the disks of the machine.
func TestInstallerISOBoots(t *testing.T) {
	requireEnv(t, "CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS_ENROLLED", "CHALKLAB_SB_KEYS", "CHALKLAB_GENERIC_INSTALLER_DIR", "CHALKLAB_CHALKCTL")
	isos, err := filepath.Glob(filepath.Join(os.Getenv("CHALKLAB_GENERIC_INSTALLER_DIR"), "*.iso"))
	if err != nil || len(isos) != 1 {
		t.Fatalf("want one ISO, found %v (%v)", isos, err)
	}
	dir := vmDir(t)
	iso := filepath.Join(dir, "installer.iso")
	if err := lab.CopySparse(context.Background(), isos[0], iso); err != nil {
		t.Fatal(err)
	}
	keys := os.Getenv("CHALKLAB_SB_KEYS")
	sign := exec.Command(os.Getenv("CHALKLAB_CHALKCTL"), "sign", "--image", iso, "--repart-json", isos[0]+".json",
		"--key", filepath.Join(keys, "db.key"), "--cert", filepath.Join(keys, "db.crt"))
	if out, err := sign.CombinedOutput(); err != nil {
		t.Fatalf("sign the ISO: %v\n%s", err, out)
	}
	blank := filepath.Join(dir, "blank.qcow2")
	if err := lab.CreateDisk(context.Background(), blank, "4G"); err != nil {
		t.Fatal(err)
	}
	n := startNode(t, lab.VMConfig{
		Dir:          dir,
		FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"),
		CDROM:        iso,
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
		out, err = exec.Command(os.Getenv("CHALKLAB_CHALKCTL"), "disks", "--endpoint", n.addr, "--fingerprint", m[1]).CombinedOutput()
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
