package e2e

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/pki"
)

// TestInstallInPlace installs the test image where it runs and checks the installed node: VAR
// is mounted, STATE and VAR have a TPM2 and a fallback keyslot, chalkd requires client
// certificates of the OS CA and honours their roles, and identities apply additive storage
// changes live and refuse destructive ones.
func TestInstallInPlace(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_IMAGE_DIR"}, chalkdEnv...)...)
	dir := vmDir(t)
	system := prepareDisk(t, dir, diskOpts{})
	extraPath := filepath.Join(dir, "extra.qcow2")
	if err := lab.CreateDisk(context.Background(), extraPath, "1G"); err != nil {
		t.Fatal(err)
	}
	extra := lab.Disk{Path: extraPath, Serial: "chalk-extra"}
	n := startNode(t, lab.VMConfig{Dir: dir, FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS"), Disks: []lab.Disk{system, extra}})
	readFacts(t, n.vm, 5*time.Minute)

	// The image carries the OS CA, so even in maintenance mode a client needs its certificate.
	anonymous, err := client.Dial(n.addr, client.Options{Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := info(anonymous); err == nil {
		t.Error("a client without a certificate was served in maintenance mode")
	}
	installInPlace(t, n, "chalklab")

	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"var_fstype":     "ext4",
		"state_keyslots": "2",
		"var_keyslots":   "2",
	})
	waitForNode(t, n, "chalklab")

	out, err := chalkctl(t, n, "base", "status", "chalklab")
	if err != nil || !strings.Contains(out, "(the cluster definition's)") || !regexp.MustCompile(`\s/var\s+mounted`).MatchString(out) {
		t.Errorf("status: %v", err)
	}
	if err := info(anonymous); err == nil {
		t.Error("an installed node served a client without a certificate")
	}
	reader := dialNode(t, n, "chalklab", clientCertificate(t, pki.RoleReader))
	if err := info(reader); err != nil {
		t.Errorf("a reader could not call Info: %v", err)
	}
	_, err = reader.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{Identity: "{}"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("ApplyIdentity as a reader: %v, want permission denied", err)
	}

	if out, err := chalkctl(t, n, "destructive", "apply-identity", "chalklab"); err == nil || !strings.Contains(out, "chalkctl storage reset chalklab var") {
		t.Errorf("a destructive identity was not refused with the reset command: %v", err)
	}
	if out, err := chalkctl(t, n, "added", "apply-identity", "chalklab"); err != nil || !strings.Contains(out, "volume extra: new volume on new disk extra") {
		t.Fatalf("apply-identity: %v", err)
	}
	out, err = chalkctl(t, n, "added", "status", "chalklab")
	if err != nil || !strings.Contains(out, "(the cluster definition's)") || !regexp.MustCompile(`\s/srv/extra\s+mounted`).MatchString(out) {
		t.Errorf("the new volume is not mounted: %v", err)
	}

	// After a reboot the new volume opens like the others, with its fallback keyslot.
	if _, err := chalkctl(t, n, "added", "reboot", "chalklab"); err != nil {
		t.Fatal(err)
	}
	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"var_boots":      "2",
		"extra_fstype":   "ext4",
		"extra_tpm2":     "1",
		"extra_keyslots": "2",
		"extra_disk":     "vdb",
		"extra_boots":    "1",
	})
}
