package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/lab"
)

// TestUpgrade installs the signed test image under Secure Boot, upgrades it to 0.2.0, which boots
// from slot B with STATE and VAR unlocked by the TPM and is found healthy, and then to 0.3.0,
// which never becomes healthy: after its one try the node falls back to 0.2.0 and says so.
func TestUpgrade(t *testing.T) {
	requireEnv(t, append(secureBootEnv, append(chalkdEnv, "CHALKLAB_UPGRADE_IMAGE_DIR", "CHALKLAB_UNHEALTHY_IMAGE_DIR")...)...)
	start := time.Now()
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{sign: true})
	n := startNode(t, lab.VMConfig{Dir: dir, FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"), Disks: []lab.Disk{disk}})
	readFacts(t, n.vm, 5*time.Minute)
	installInPlace(t, n, "chalklab")
	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"secureboot":      "1",
		"store_label":     "store_0.1.0",
		"store_partition": "3",
		"state_boots":     "2",
		"var_boots":       "1",
	})
	waitForNode(t, n, "chalklab")
	t.Logf("installed after %v", time.Since(start).Round(time.Second))

	keys := os.Getenv("CHALKLAB_SB_KEYS")
	upgrade := func(env string) (string, error) {
		return chalkctl(t, nil, "base", "upgrade", "--image", os.Getenv(env), "--nodes", "chalklab", "--endpoint", "chalklab="+n.addr,
			"--sign-key", filepath.Join(keys, "db.key"), "--sign-cert", filepath.Join(keys, "db.crt"), "--timeout", "10m")
	}
	upgraded := time.Now()
	if out, err := upgrade("CHALKLAB_UPGRADE_IMAGE_DIR"); err != nil || !strings.Contains(out, "upgraded 1 nodes to 0.2.0") {
		t.Fatalf("upgrade to 0.2.0: %v", err)
	}
	t.Logf("upgraded to 0.2.0 in %v", time.Since(upgraded).Round(time.Second))
	// Slot B, under Secure Boot, with the TPM unsealing STATE and VAR as before.
	assertFacts(t, readFacts(t, n.vm, time.Minute), map[string]string{
		"secureboot":      "1",
		"usr_verity":      "verified",
		"store_label":     "store_0.2.0",
		"store_partition": "5",
		"slot_b_empty":    "0",
		"state_tpm2":      "1",
		"var_tpm2":        "1",
		"state_boots":     "3",
		"var_boots":       "2",
	})
	if out, err := chalkctl(t, n, "base", "status", "chalklab"); err != nil || !strings.Contains(out, "image 0.2.0, booted from chalkos_0.2.0.efi\n") {
		t.Errorf("status after the upgrade: %v", err)
	}

	failed := time.Now()
	out, err := upgrade("CHALKLAB_UNHEALTHY_IMAGE_DIR")
	if err == nil || !strings.Contains(out, "chalklab: upgrade to 0.3.0 failed: rolled back to 0.2.0") || !strings.Contains(out, "units failed: chalktest-broken.service") {
		t.Fatalf("upgrade to 0.3.0: %v", err)
	}
	t.Logf("fell back from 0.3.0 in %v", time.Since(failed).Round(time.Second))
	// One boot of 0.3.0 from slot A, then 0.2.0 again.
	assertFacts(t, readFacts(t, n.vm, time.Minute), map[string]string{"store_label": "store_0.3.0", "store_partition": "3", "state_boots": "4"})
	assertFacts(t, readFacts(t, n.vm, time.Minute), map[string]string{"store_label": "store_0.2.0", "store_partition": "5", "state_boots": "5", "var_boots": "4"})
	out, err = chalkctl(t, n, "base", "status", "chalklab")
	if err != nil || !strings.Contains(out, "image 0.2.0, booted from chalkos_0.2.0.efi\n") || !strings.Contains(out, "upgrade to 0.3.0 failed: rolled back to 0.2.0") ||
		!strings.Contains(out, "units failed: chalktest-broken.service") {
		t.Errorf("status after the rollback: %v", err)
	}
	t.Logf("the test took %v", time.Since(start).Round(time.Second))
}
