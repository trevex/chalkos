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

// TestStagedAdvice names the delivery that gives the staged slot the inventory's config, and no
// delivery the running image checks when that image is not the inventory's.
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
	runs := inventoryImage{name: "new", runs: true}
	stages := inventoryImage{name: "new", stages: true}
	elsewhere := inventoryImage{name: "new"}
	knownGood := "the rollback image keeps the config it last ran while the boot of chalkos_4+1-2.efi is not found healthy"
	for name, tc := range map[string]struct {
		s    *nodev1.StatusResponse
		img  inventoryImage
		want string
	}{
		"running differs":              {status("old", false, ""), runs, "chalkctl config apply w1 delivers it"},
		"running is the same":          {status("want", false, ""), runs, "chalkctl config apply --staged w1 delivers it"},
		"known-good":                   {status("want", true, ""), runs, knownGood},
		"known-good, running differs":  {status("old", true, ""), runs, knownGood},
		"on trial":                     {status("want", false, "want"), runs, "the confirmation of its trial writes it: chalkctl config confirm w1"},
		"another on trial":             {status("other", false, "other"), runs, "the node takes no delivery until the trial of config other ends"},
		"inventory's image staged":     {status("old", false, ""), stages, "chalkctl config apply --staged w1 delivers it"},
		"inventory's image in neither": {status("old", false, ""), elsewhere, "chalkctl upgrade --nodes w1 delivers it with the image new"},
		"neither, running is the same": {status("want", false, ""), elsewhere, "chalkctl upgrade --nodes w1 delivers it with the image new"},
		"neither, known-good":          {status("old", true, ""), elsewhere, knownGood},
		"neither, another on trial":    {status("other", false, "other"), elsewhere, "the node takes no delivery until the trial of config other ends"},
	} {
		if got := stagedAdvice("w1", "want", tc.img, tc.s); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// TestRunningAdvice names config apply for the running slot only when the node runs the
// inventory's image, which config apply checks the config against; otherwise the config comes
// with that image.
func TestRunningAdvice(t *testing.T) {
	for name, tc := range map[string]struct {
		img   inventoryImage
		trial string
		want  string
	}{
		"inventory's image runs":   {inventoryImage{name: "new", runs: true}, "", "chalkctl config apply w1 delivers it"},
		"inventory's image staged": {inventoryImage{name: "new", stages: true}, "", "it comes with the image new in the inactive slot"},
		"another image":            {inventoryImage{name: "new"}, "", "chalkctl upgrade --nodes w1 delivers it with the image new"},
		"on trial":                 {inventoryImage{name: "new", runs: true}, "old", "the node takes no delivery until the trial of config old ends"},
	} {
		s := &nodev1.StatusResponse{ConfigVersion: "old"}
		if tc.trial != "" {
			s.Trial = &nodev1.ConfigTrial{Version: tc.trial, PreviousVersion: "older"}
		}
		if got := runningAdvice("w1", "want", tc.img, s); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
