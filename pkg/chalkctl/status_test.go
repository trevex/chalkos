package chalkctl

import (
	"context"
	"strings"
	"testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// TestStatusWithABrokenConfig reports the rest of the status when chalkctl cannot read the node's
// config from the inventory.
func TestStatusWithABrokenConfig(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	ta.writeNode(t, n1SystemDisk, n1SystemDisk, n1Network)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	out := ta.stdout.String()
	if !strings.Contains(out, "config none (cannot read the config the inventory gives it: ") || !strings.Contains(out, "platform metal") || !strings.Contains(out, "var") {
		t.Errorf("status = %q", out)
	}
}

// TestStagedAdvice advises the delivery that gives the staged image's slot the inventory's config:
// a plain config apply, which writes both slots, while the running image's differs too;
// config apply --staged while the running image runs the inventory's config already; and none for
// the image chalkd names known-good, which keeps the config it last ran, whatever else the boot
// tells. During a trial, which holds every delivery back, the confirmation writes the slot when
// the inventory's config is on trial, and the trial's end comes first otherwise.
func TestStagedAdvice(t *testing.T) {
	staged := &nodev1.BootImage{RootHash: []byte{3}}
	status := func(config string, knownGood bool, trial string) *nodev1.StatusResponse {
		s := &nodev1.StatusResponse{ConfigVersion: config, Boot: &nodev1.BootStatus{
			Entry: "chalkos_4+1-2.efi", Staged: staged, StagedState: nodev1.SlotState_SLOT_STATE_ACTIVE, StagedKnownGood: knownGood,
		}}
		if trial != "" {
			s.Trial = &nodev1.ConfigTrial{Version: trial, PreviousVersion: "old"}
		}
		return s
	}
	knownGood := "the rollback image keeps the config it last ran while the boot of chalkos_4+1-2.efi is not found healthy"
	for name, tc := range map[string]struct {
		s    *nodev1.StatusResponse
		want string
	}{
		"running differs":             {status("old", false, ""), "chalkctl config apply w1 delivers it"},
		"running is the same":         {status("want", false, ""), "chalkctl config apply --staged w1 delivers it"},
		"known-good":                  {status("want", true, ""), knownGood},
		"known-good, running differs": {status("old", true, ""), knownGood},
		"on trial":                    {status("want", false, "want"), "the confirmation of its trial writes it: chalkctl config confirm w1"},
		"another on trial":            {status("other", false, "other"), "the node takes no delivery until the trial of config other ends"},
	} {
		if got := stagedAdvice("w1", "want", tc.s); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
