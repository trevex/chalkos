package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// TestInstallerInstallsOntoBlankDisk boots the test cluster's installer next to a blank disk,
// installs a node onto that disk with the role image streamed from the host, and checks the
// node boots from it installed: VAR mounted, STATE and VAR with two keyslots, chalkd over mTLS.
func TestInstallerInstallsOntoBlankDisk(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_INSTALLER_DIR", "CHALKLAB_IMAGE_DIR"}, chalkdEnv...)...)
	dir := vmDir(t)
	installer := prepareDisk(t, dir, diskOpts{imageEnv: "CHALKLAB_INSTALLER_DIR"})
	targetPath := filepath.Join(dir, "target.qcow2")
	if err := lab.CreateDisk(context.Background(), targetPath, "16G"); err != nil {
		t.Fatal(err)
	}
	n := startNode(t, lab.VMConfig{
		Dir:          dir,
		FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS"),
		Disks:        []lab.Disk{installer, {Path: targetPath, Serial: "chalk-target"}},
	})

	fingerprint := consoleFingerprint(t, n.vm)
	if info := waitForChalkd(t, n, 5*time.Minute); !info.Installer || info.ImageId != "chalkos-installer" {
		t.Fatalf("info = %+v, want the installer", info)
	}
	if _, err := chalkctl(t, n, "base", "install", "chalklab-target", "--fingerprint", fingerprint, "--image", os.Getenv("CHALKLAB_IMAGE_DIR")); err != nil {
		t.Fatalf("install: %v", err)
	}

	// The boot entry the installer added makes the VM boot the target.
	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"var_fstype":     "ext4",
		"var_disk":       "vdb",
		"var_tpm2":       "1",
		"state_tpm2":     "1",
		"state_keyslots": "2",
		"var_keyslots":   "2",
		"state_boots":    "1",
		"var_boots":      "1",
	})
	waitForNode(t, n, "chalklab-target")
	if out, err := chalkctl(t, n, "base", "status", "chalklab-target"); err != nil || !strings.Contains(out, "(the cluster definition's)") {
		t.Errorf("status: %v", err)
	}
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
