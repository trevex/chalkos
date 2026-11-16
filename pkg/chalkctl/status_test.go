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

// adviceStatus is the status of a node that runs config, with an image active in the inactive
// slot, known-good or not, and a trial of trial from previous when trial is set.
func adviceStatus(config string, knownGood bool, trial, previous string) *nodev1.StatusResponse {
	s := &nodev1.StatusResponse{ConfigVersion: config, Boot: &nodev1.BootStatus{
		Entry: "chalkos_4+1-2.efi", Staged: &nodev1.BootImage{RootHash: []byte{3}}, StagedState: nodev1.SlotState_SLOT_STATE_ACTIVE, StagedKnownGood: knownGood,
	}}
	if trial != "" {
		s.Trial = &nodev1.ConfigTrial{Version: trial, PreviousVersion: previous}
	}
	return s
}

// trialUnreadable is the status of a node whose trial record cannot be read.
func trialUnreadable(config string) *nodev1.StatusResponse {
	s := adviceStatus(config, false, "", "")
	s.TrialError = "parse chalkd/trial: unexpected end of JSON input"
	return s
}

const (
	unreadableAdvice = "the node takes no delivery until chalkctl config rollback w1 removes the record of a config's trial that cannot be read"
	upgradeAdvice    = "chalkctl upgrade --nodes w1 delivers it with the image new"
)

// TestStagedAdvice names the delivery that gives the staged slot the inventory's config, and no
// delivery the running image or another staged image may refuse unsaid.
func TestStagedAdvice(t *testing.T) {
	runs := imageMatch{name: "new", runs: true}
	runsOther := imageMatch{name: "new", runs: true, other: "030303030303"}
	stages := imageMatch{name: "new", stages: true}
	elsewhere := imageMatch{name: "new"}
	knownGood := "the rollback image keeps the config it last ran while the boot of chalkos_4+1-2.efi is not found healthy"
	other := " if the image 030303030303 in the inactive slot, which is not the inventory's, accepts it too"
	for name, tc := range map[string]struct {
		s    *nodev1.StatusResponse
		img  imageMatch
		want string
	}{
		"running differs":              {adviceStatus("old", false, "", ""), runs, "chalkctl config apply w1 delivers it"},
		"running is the same":          {adviceStatus("want", false, "", ""), runs, "chalkctl config apply --staged w1 delivers it"},
		"known-good":                   {adviceStatus("want", true, "", ""), runs, knownGood},
		"known-good, running differs":  {adviceStatus("old", true, "", ""), runs, knownGood},
		"on trial":                     {adviceStatus("want", false, "want", "old"), runs, "the confirmation of its trial writes it: chalkctl config confirm w1"},
		"another on trial":             {adviceStatus("other", false, "other", "old"), runs, "the node takes no delivery until the trial of config other ends"},
		"trial rolled back":            {adviceStatus("old", false, "other", "old"), runs, "chalkctl config apply w1 delivers it"},
		"unreadable trial":             {trialUnreadable("old"), runs, unreadableAdvice},
		"inventory's image staged":     {adviceStatus("old", false, "", ""), stages, "chalkctl config apply --staged w1 delivers it"},
		"inventory's image in neither": {adviceStatus("old", false, "", ""), elsewhere, upgradeAdvice},
		"neither, running is the same": {adviceStatus("want", false, "", ""), elsewhere, upgradeAdvice},
		"neither, known-good":          {adviceStatus("old", true, "", ""), elsewhere, knownGood},
		"neither, another on trial":    {adviceStatus("other", false, "other", "old"), elsewhere, "the node takes no delivery until the trial of config other ends"},
		"another image staged":         {adviceStatus("old", false, "", ""), runsOther, "chalkctl config apply w1 delivers it" + other},
		"another image staged, same":   {adviceStatus("want", false, "", ""), runsOther, "chalkctl config apply --staged w1 delivers it" + other},
	} {
		if got := stagedAdvice("w1", "want", tc.img, tc.s); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// TestRunningAdvice names config apply for the running slot only when the node runs the
// inventory's image, which config apply checks the config against; otherwise the config comes
// with that image. A trial that holds deliveries back is named first.
func TestRunningAdvice(t *testing.T) {
	for name, tc := range map[string]struct {
		img  imageMatch
		s    *nodev1.StatusResponse
		want string
	}{
		"inventory's image runs":       {imageMatch{name: "new", runs: true}, adviceStatus("old", false, "", ""), "chalkctl config apply w1 delivers it"},
		"another image staged":         {imageMatch{name: "new", runs: true, other: "030303030303"}, adviceStatus("old", false, "", ""), "chalkctl config apply w1 delivers it if the image 030303030303 in the inactive slot, which is not the inventory's, accepts it too"},
		"inventory's image staged":     {imageMatch{name: "new", stages: true}, adviceStatus("old", false, "", ""), "it comes with the image new in the inactive slot"},
		"inventory's image known-good": {imageMatch{name: "new", knownGood: true}, adviceStatus("old", true, "", ""), "the node boots the image new next with the config it last ran; once it runs it, chalkctl config apply w1 delivers it"},
		"another image":                {imageMatch{name: "new"}, adviceStatus("old", false, "", ""), upgradeAdvice},
		"on trial":                     {imageMatch{name: "new", runs: true}, adviceStatus("old", false, "old", "older"), "the node takes no delivery until the trial of config old ends"},
		"on trial, another image":      {imageMatch{name: "new"}, adviceStatus("old", false, "old", "older"), "the node takes no delivery until the trial of config old ends"},
		"trial rolled back":            {imageMatch{name: "new", runs: true}, adviceStatus("old", false, "other", "old"), "chalkctl config apply w1 delivers it"},
		"trial rolled back, another":   {imageMatch{name: "new"}, adviceStatus("old", false, "other", "old"), upgradeAdvice},
		"unreadable trial":             {imageMatch{name: "new", runs: true}, trialUnreadable("old"), unreadableAdvice},
	} {
		if got := runningAdvice("w1", "want", tc.img, tc.s); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
