package chalkctl

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

// pendingSince is when the lab's nodes stored their pending config.
var pendingSince = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// apply-config --staged on a node without a staged image has it hold the inventory's config
// pending, compared with the running config and sent without a fallback secret; status shows it,
// another --staged replaces it, a delivery to the running image replaces it too, as its dry run
// says first, and --staged --discard drops it, needing no inventory.
func TestApplyConfigStagedHoldsPending(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.useInventory(t, map[string]string{"w": testUpgradeImage(t, "w", "0.2.0")})
	n := l.nodes["w1"]
	if err := l.applyConfig("w1", "--staged"); err != nil {
		t.Fatal(err)
	}
	want := versionOf(l.inventoryConfig("w1"))
	if n.pending != l.inventoryConfig("w1") || n.config != "" || n.staged != "" {
		t.Errorf("w1 holds %q pending and runs %q", n.pending, n.config)
	}
	if out := l.ta.stdout.String(); out != "w1: config 4 added, 0 removed, 0 changed\nw1 holds config "+want+" pending, unchecked; the next upgrade checks it against its image\n" {
		t.Errorf("stdout = %q", out)
	}
	// The diff of a second delivery compares with the pending config.
	if err := l.applyConfig("w1", "--staged"); err != nil || !strings.HasPrefix(l.ta.stdout.String(), "w1: config unchanged\n") {
		t.Errorf("a second delivery = %v, stdout %q", err, l.ta.stdout)
	}
	l.ta.stdout.Reset()
	if err := l.ta.run(context.Background(), []string{"status", "w1", "--inventory", l.inventory, "--address", l.addrs["w1"]}); err != nil {
		t.Fatal(err)
	}
	if line := "pending config " + want + " (unchecked, since 2026-11-01T12:00:00Z; the next upgrade checks it against its image)\n"; !strings.Contains(l.ta.stdout.String(), line) {
		t.Errorf("status lacks %q:\n%s", line, l.ta.stdout)
	}
	n.pending = "{}"
	if err := l.applyConfig("w1", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if line := "w1: pending config → " + want + ", unchecked; the next upgrade checks it against its image\n"; !strings.HasSuffix(l.ta.stdout.String(), line) || n.pending != "{}" {
		t.Errorf("stdout = %q, want it to end with %q", l.ta.stdout, line)
	}
	if err := l.applyConfig("w1"); err != nil {
		t.Fatal(err)
	}
	if out := l.ta.stdout.String(); !strings.HasSuffix(out, "w1 runs config "+want+"\nw1 holds config "+want+" pending, unchecked, in place of its pending config; the next upgrade checks it against its image\n") || n.pending != l.inventoryConfig("w1") {
		t.Errorf("stdout = %q, pending %q", out, n.pending)
	}
	if err := l.applyConfig("w1", "--discard"); err == nil || !strings.Contains(err.Error(), "pass --staged") {
		t.Errorf("--discard without --staged = %v", err)
	}
	l.ta.stdout.Reset()
	if err := l.ta.run(context.Background(), []string{"apply-config", "w1", "--staged", "--discard", "--address", l.addrs["w1"]}); err != nil {
		t.Fatal(err)
	}
	if out := l.ta.stdout.String(); out != "w1 dropped its pending config "+want+"\n" || n.pending != "" {
		t.Errorf("stdout = %q, pending %q", out, n.pending)
	}
	if err := l.applyConfig("w1", "--staged", "--discard"); err != nil || l.ta.stdout.String() != "w1 holds no pending config\n" {
		t.Errorf("a second discard = %v, stdout %q", err, l.ta.stdout)
	}
}

// apply-config --staged --dry-run on a node without a staged image says the config would be held
// pending and keeps nothing.
func TestApplyConfigStagedDryRunHoldsNothing(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.useInventory(t, map[string]string{"w": testUpgradeImage(t, "w", "0.2.0")})
	if err := l.applyConfig("w1", "--staged", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	want := "w1: no image is staged; config " + versionOf(l.inventoryConfig("w1")) + " would be held pending, unchecked, for the next upgrade, which checks it against its image\n"
	if out := l.ta.stdout.String(); !strings.HasSuffix(out, want) {
		t.Errorf("stdout = %q, want it to end with %q", out, want)
	}
	if p := l.nodes["w1"].pending; p != "" {
		t.Errorf("w1 holds %q pending", p)
	}
}

// An upgrade says, once it staged the image, what it did with a node's pending config: an
// operator's, which carries no config, gave it to the image; an admin's replaced it with the
// inventory's.
func TestUpgradeNamesThePendingConfig(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	l.useInventory(t, map[string]string{"w": img})
	n := l.nodes["w1"]
	n.pending = "{}"
	config := l.ta.writeConfig(t, "ops", pki.RoleOperator, time.Hour, time.Now())
	if err := l.upgrade(img, "--nodes", "w1", "--until", "activate", "--config", config); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if line := ", staged as .staged-chalkos_2.efi\nw1: the image took the pending config " + versionOf("{}") + ", which it checked first\n"; !strings.Contains(l.ta.stdout.String(), line) {
		t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
	}
	if n.stagedConfig != "{}" || n.pending != "" {
		t.Errorf("w1 stages %q and holds %q pending", n.stagedConfig, n.pending)
	}
	n.staged, n.stagedRoot, n.stagedConfig, n.pending = "", nil, "", "{}"
	l.ta.stdout.Reset()
	if err := l.upgrade(img, "--nodes", "w1", "--until", "activate"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if line := "w1: the inventory's config replaced the pending config " + versionOf("{}") + "\n"; !strings.Contains(l.ta.stdout.String(), line) {
		t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
	}
	if n.stagedConfig != l.inventoryConfig("w1") || n.pending != "" {
		t.Errorf("w1 stages %q and holds %q pending", n.stagedConfig, n.pending)
	}
}

// An upgrade after which the node still holds its pending config says it keeps it, as for the
// known-good image a rollback left with the config it last ran.
func TestUpgradeNotesAPendingConfigKept(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	l.useInventory(t, map[string]string{"w": img})
	n := l.nodes["w1"]
	n.pending, n.keepPending = "{}", true
	config := l.ta.writeConfig(t, "ops", pki.RoleOperator, time.Hour, time.Now())
	if err := l.upgrade(img, "--nodes", "w1", "--until", "activate", "--config", config); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if line := "w1: keeps the pending config " + versionOf("{}") + " for a later upgrade\n"; !strings.Contains(l.ta.stdout.String(), line) || strings.Contains(l.ta.stdout.String(), "took the pending config") {
		t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
	}
}
