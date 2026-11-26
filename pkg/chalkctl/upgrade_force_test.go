package chalkctl

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

// A repair image reaches a selected unhealthy node through the core API, even while all layer
// status calls fail and other control planes are unreachable. Only the new boot must be healthy.
func TestUpgradeForceRecoversWithoutCluster(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "cp-metal", "0.2.0")
	l.kubernetesFails = map[string]bool{"cp1": true}
	l.sick = map[string]string{"0.1.0": "bootstrap cannot pull the control-plane images"}
	l.addrs["cp2"], l.addrs["cp3"] = "127.0.0.1:1", "127.0.0.1:1"
	l.cordoned["cp1"] = true
	if err := l.upgrade(img, "--nodes", "cp1"); err == nil || !strings.Contains(err.Error(), "API server does not answer") {
		t.Fatalf("ordinary upgrade = %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Fatalf("ordinary upgrade changed nodes: %q", got)
	}
	if err := l.upgrade(img, "--nodes", "cp1", "--recover"); err != nil {
		t.Fatalf("forced upgrade: %v\n%s", err, l.ta.stdout)
	}
	want := []string{"cp1 upgrade", "cp1 activate", "cp1 reboot"}
	if got := l.takeEvents(); !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if l.images()["cp1"] != "0.2.0" || len(l.healthChecked["cp1"]) == 0 {
		t.Error("the repair image was not booted and health-checked")
	}
	if !l.cordoned["cp1"] {
		t.Error("the forced upgrade uncordoned the node")
	}
	if !strings.Contains(l.ta.stdout.String(), "recover: skipping cluster version checks and layer coordination") {
		t.Errorf("output lacks the force notice:\n%s", l.ta.stdout)
	}
	// Repeating recovery does not reinstall, reboot or release an already healthy target.
	if err := l.upgrade(img, "--nodes", "cp1", "--recover"); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("repeat recovery changed nodes: %q", got)
	}
}

// A force run is targeted explicitly and leaves the nodes alone on a usage error.
func TestUpgradeForceNeedsNodes(t *testing.T) {
	l := newUpgradeLab(t, 1)
	err := l.upgrade(testUpgradeImage(t, "cp-metal", "0.2.0"), "--recover")
	if err == nil || !strings.Contains(err.Error(), "--recover requires --nodes") {
		t.Fatalf("upgrade = %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("events %q", got)
	}
}

// Staging and activation need neither the cluster nor a reboot, and a repeated force run can
// continue the same image through those steps to a healthy boot.
func TestUpgradeForceSteps(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.kubernetesFails = map[string]bool{"cp1": true}
	img := testUpgradeImage(t, "cp-metal", "0.2.0")
	if err := l.upgrade(img, "--nodes", "cp1", "--recover", "--diff"); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Fatalf("force preview changed nodes: %q", got)
	}

	for _, tc := range []struct {
		step   string
		events []string
	}{
		{"stage", []string{"cp1 upgrade"}},
		{"activate", []string{"cp1 activate"}},
		{"reboot", []string{"cp1 reboot"}},
	} {
		if err := l.upgrade(img, "--nodes", "cp1", "--recover", "--until", tc.step); err != nil {
			t.Fatalf("%s: %v", tc.step, err)
		}
		if got := l.takeEvents(); !slices.Equal(got, tc.events) {
			t.Errorf("%s: events %q, want %q", tc.step, got, tc.events)
		}
	}
}

// Forcing orchestration does not turn an image or config rejection into an activation or reboot.
func TestUpgradeForceKeepsValidation(t *testing.T) {
	for _, what := range []string{"config", "transfer", "activation"} {
		t.Run(what, func(t *testing.T) {
			l := newUpgradeLab(t, 1)
			img := testUpgradeImage(t, "cp-metal", "0.2.0")
			var want string
			var events []string
			switch what {
			case "config":
				for _, f := range []string{"facts/kubernetes.json", "schemas/kubernetes.chalkos.dev_v1alpha1_Node.json"} {
					if err := os.Remove(filepath.Join(img, f)); err != nil {
						t.Fatal(err)
					}
				}
				want = `does not accept kind "Node"`
			case "transfer":
				l.refuseUpgrade = "cp1"
				want = "receive the store"
				events = []string{"cp1 upgrade"}
			case "activation":
				l.refuseActivate = "cp1"
				want = "Secure Boot would refuse the UKI"
				events = []string{"cp1 upgrade", "cp1 activate"}
			}
			err := l.upgrade(img, "--nodes", "cp1", "--recover")
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("upgrade = %v, want %q", err, want)
			}
			if got := l.takeEvents(); !slices.Equal(got, events) {
				t.Errorf("events %q, want %q", got, events)
			}
		})
	}
}

// Recovery succeeds only once the replacement boot is healthy; a fallback remains a failure.
func TestUpgradeForceStopsAtFallback(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.unhealthy["0.2.0"] = true
	err := l.upgrade(testUpgradeImage(t, "cp-metal", "0.2.0"), "--nodes", "cp1", "--recover")
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("upgrade = %v", err)
	}
	want := []string{"cp1 upgrade", "cp1 activate", "cp1 reboot"}
	if got := l.takeEvents(); !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
}

// Recovery can replace versions that a live cluster's rollout guards would refuse, while the
// replacement config still has to suit the image it will boot.
func TestUpgradeForceSkipsRolloutGuards(t *testing.T) {
	for _, what := range []string{"image version", "config version"} {
		t.Run(what, func(t *testing.T) {
			l := newUpgradeLab(t, 3)
			var img, want string
			if what == "image version" {
				img = testImageWithKubelet(t, "cp-metal", "0.2.0", "1.38.0")
				l.rollTo(t, "1.38.0")
				want = "would be newer than the API server"
			} else {
				img = testUpgradeImage(t, "cp-metal", "0.2.0")
				l.rollTo(t, "1.37.2")
				want = "which an upgrade does not deliver"
			}
			l.runAPIServer("1.37.1")
			if err := l.upgrade(img, "--nodes", "cp1", "--until", "stage"); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("ordinary upgrade = %v, want %q", err, want)
			}
			if got := l.takeEvents(); len(got) != 0 {
				t.Fatalf("ordinary upgrade changed nodes: %q", got)
			}
			if err := l.upgrade(img, "--nodes", "cp1", "--until", "stage", "--recover"); err != nil {
				t.Fatal(err)
			}
			if got, want := l.takeEvents(), []string{"cp1 upgrade"}; !slices.Equal(got, want) {
				t.Errorf("events %q, want %q", got, want)
			}
		})
	}
}

// Force does not grant a reader upgrade permission or let an operator deliver admin-only config.
func TestUpgradeForceKeepsRoles(t *testing.T) {
	for _, role := range []pki.Role{pki.RoleReader, pki.RoleOperator} {
		t.Run(string(role), func(t *testing.T) {
			l := newUpgradeLab(t, 1)
			config := l.ta.writeConfig(t, "recovery", role, time.Hour, time.Now())
			l.ta.withoutSecrets(t)
			err := l.upgrade(testUpgradeImage(t, "cp-metal", "0.2.0"), "--nodes", "cp1", "--recover", "--config", config)
			if role == pki.RoleReader {
				if err == nil || !strings.Contains(err.Error(), "upgrade needs the operator role") {
					t.Fatalf("upgrade = %v", err)
				}
				if got := l.takeEvents(); len(got) != 0 {
					t.Errorf("events %q", got)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if header := l.nodes["cp1"].header; header == nil || header.GetConfig() != "" {
					t.Errorf("operator sent config: %v", header)
				}
			}
		})
	}
}

// A new boot that settles unhealthy fails recovery even when its entry is already blessed.
func TestUpgradeForceChecksBootHealth(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.sick["0.2.0"] = "broken.service failed"
	err := l.upgrade(testUpgradeImage(t, "cp-metal", "0.2.0"), "--nodes", "cp1", "--recover")
	if err == nil || !strings.Contains(err.Error(), "broken.service failed") {
		t.Fatalf("upgrade = %v", err)
	}
	if len(l.healthChecked["cp1"]) == 0 {
		t.Error("the replacement boot was not health-checked")
	}
}

// A previous image can be booted through the same API-only recovery path.
func TestUpgradeForceRollback(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "cp-metal", "0.2.0")
	if err := l.upgrade(img, "--nodes", "cp1", "--recover"); err != nil {
		t.Fatal(err)
	}
	l.takeEvents()
	l.kubernetesFails = map[string]bool{"cp1": true}
	if err := l.run("upgrade", "--nodes", "cp1", "--rollback", "--recover"); err != nil {
		t.Fatal(err)
	}
	if got, want := l.takeEvents(), []string{"cp1 rollback", "cp1 reboot"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if l.images()["cp1"] != "0.1.0" {
		t.Error("the previous image was not booted")
	}
}
