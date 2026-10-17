package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/lab"
)

// TestUpgrade installs the signed test image under Secure Boot, which boots its UKI as
// chalkos_1.efi, upgrades it to the upgrade image, which boots from slot B as chalkos_2.efi with
// STATE and VAR unlocked by the TPM and is found healthy, and then to the unhealthy image,
// installed as chalkos_3, which never becomes healthy: after its one try the node falls back to
// chalkos_2.efi and says so. Each image is named by its root hash.
func TestUpgrade(t *testing.T) {
	requireEnv(t, append(secureBootEnv, append(chalkdEnv, "CHALKLAB_UPGRADE_IMAGE_DIR", "CHALKLAB_UNHEALTHY_IMAGE_DIR")...)...)
	base, next, bad := rootHash(t, "CHALKLAB_IMAGE_DIR"), rootHash(t, "CHALKLAB_UPGRADE_IMAGE_DIR"), rootHash(t, "CHALKLAB_UNHEALTHY_IMAGE_DIR")
	start := time.Now()
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{sign: true})
	n := startNode(t, lab.VMConfig{Dir: dir, FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"), Disks: []lab.Disk{disk}})
	readFacts(t, n.vm, 5*time.Minute)
	installInPlace(t, n, "chalklab")
	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"secureboot":      "1",
		"store_label":     "store_" + base[:16],
		"store_partition": "3",
		"state_boots":     "2",
		"var_boots":       "1",
	})
	waitForNode(t, n, "chalklab")
	if out, err := chalkctl(t, n, "base", "status", "chalklab"); err != nil || !strings.Contains(out, "image "+base[:12]+", booted from chalkos_1.efi\n") {
		t.Errorf("status after the install: %v", err)
	}
	t.Logf("installed after %v", time.Since(start).Round(time.Second))

	keys := os.Getenv("CHALKLAB_SB_KEYS")
	upgrade := func(env string) (string, error) {
		return chalkctl(t, nil, "base", "upgrade", "--image", os.Getenv(env), "--nodes", "chalklab", "--address", "chalklab="+n.addr,
			"--sign-key", filepath.Join(keys, "db.key"), "--sign-cert", filepath.Join(keys, "db.crt"), "--timeout", "10m")
	}
	upgraded := time.Now()
	if out, err := upgrade("CHALKLAB_UPGRADE_IMAGE_DIR"); err != nil || !strings.Contains(out, "chalklab: installed "+next[:12]+", which boots next as chalkos_2+3.efi") ||
		!strings.Contains(out, "upgraded 1 node to "+next[:12]) {
		t.Fatalf("upgrade to %s: %v", next[:12], err)
	}
	t.Logf("upgraded to %s in %v", next[:12], time.Since(upgraded).Round(time.Second))
	// Slot B, under Secure Boot, with the TPM unsealing STATE and VAR as before.
	assertFacts(t, readFacts(t, n.vm, time.Minute), map[string]string{
		"secureboot":      "1",
		"usr_verity":      "verified",
		"store_label":     "store_" + next[:16],
		"store_partition": "5",
		"slot_b_empty":    "0",
		"state_tpm2":      "1",
		"var_tpm2":        "1",
		"state_boots":     "3",
		"var_boots":       "2",
	})
	if out, err := chalkctl(t, n, "base", "status", "chalklab"); err != nil || !strings.Contains(out, "image "+next[:12]+", booted from chalkos_2.efi\n") {
		t.Errorf("status after the upgrade: %v", err)
	}

	failed := time.Now()
	out, err := upgrade("CHALKLAB_UNHEALTHY_IMAGE_DIR")
	if err == nil || !strings.Contains(out, "chalklab: installed "+bad[:12]+", which boots next as chalkos_3+1.efi") ||
		!strings.Contains(out, "chalklab: upgrade to "+bad[:12]+" failed: rolled back to "+next[:12]) || !strings.Contains(out, "units failed: chalktest-broken.service") {
		t.Fatalf("upgrade to %s: %v", bad[:12], err)
	}
	t.Logf("fell back from %s in %v", bad[:12], time.Since(failed).Round(time.Second))
	// One boot of the unhealthy image from slot A, then the upgrade image again.
	assertFacts(t, readFacts(t, n.vm, time.Minute), map[string]string{"store_label": "store_" + bad[:16], "store_partition": "3", "state_boots": "4"})
	assertFacts(t, readFacts(t, n.vm, time.Minute), map[string]string{"store_label": "store_" + next[:16], "store_partition": "5", "state_boots": "5", "var_boots": "4"})
	out, err = chalkctl(t, n, "base", "status", "chalklab")
	if err != nil || !strings.Contains(out, "image "+next[:12]+", booted from chalkos_2.efi\n") || !strings.Contains(out, "upgrade to "+bad[:12]+" failed: rolled back to "+next[:12]) ||
		!strings.Contains(out, "units failed: chalktest-broken.service") {
		t.Errorf("status after the rollback: %v", err)
	}
	t.Logf("the test took %v", time.Since(start).Round(time.Second))
}
