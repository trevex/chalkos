package chalkctl

import (
	"bytes"
	"context"
	"slices"
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

// status names each output with the start of its hash and why its last render failed, or that
// none rendered it yet.
func TestOutputLine(t *testing.T) {
	for _, tc := range []struct {
		o    *nodev1.OutputStatus
		want string
	}{
		{&nodev1.OutputStatus{Name: "bird-conf", Hash: strings.Repeat("ab", 32)}, "bird-conf\tabababababab\trendered"},
		{&nodev1.OutputStatus{Name: "bird-conf", Hash: strings.Repeat("ab", 32), Error: "its check refused the rendered files (exit 1); see the node's journal"}, "bird-conf\tabababababab\tfailed: its check refused the rendered files (exit 1); see the node's journal"},
		{&nodev1.OutputStatus{Name: "bird-conf", Error: "its renderer failed (exit 2); see the node's journal"}, "bird-conf\tnone\tfailed: its renderer failed (exit 2); see the node's journal"},
		{&nodev1.OutputStatus{Name: "bird-conf"}, "bird-conf\tnone\tnot rendered yet"},
	} {
		if got := outputLine(tc.o); got != tc.want {
			t.Errorf("outputLine(%v) = %q, want %q", tc.o, got, tc.want)
		}
	}
}

// TestBootLines names each image by its root hash, with the tag and revision the node knows.
func TestBootLines(t *testing.T) {
	running := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x81, 0xbd}, 16), Generation: 7, Revision: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Tag: "v1.4.0"}
	next := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x3f, 0xa9}, 16), Generation: 8, Tag: "v1.5.0"}
	for _, tc := range []struct {
		boot *nodev1.BootStatus
		want []string
	}{
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true}, []string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7+2-1.efi"}, []string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7+2-1.efi, not found healthy yet"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Staged: next, StagedState: nodev1.SlotState_SLOT_STATE_ACTIVE},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) active; it boots next"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Staged: next, StagedState: nodev1.SlotState_SLOT_STATE_STAGED},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) staged; node activate makes it boot next"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Staged: next, Rollback: next},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "rollback to 3fa93fa93fa9 (v1.5.0) staged; it boots next"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Previous: &nodev1.BootImage{RootHash: next.RootHash, Generation: 6, Tag: "v1.5.0"}},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "previous image 3fa93fa93fa9 (v1.5.0) (upgrade --rollback boots it)"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Failed: next, Journal: []string{"a", "b"}},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) failed: rolled back to 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6)", "  a", "  b"}},
		{&nodev1.BootStatus{Running: &nodev1.BootImage{RootHash: next.RootHash}, Error: "no UKI on the ESP boots the running store"}, []string{"image 3fa93fa93fa9; its boot is unknown: no UKI on the ESP boots the running store"}},
		{nil, nil},
	} {
		if got := bootLines(tc.boot); !slices.Equal(got, tc.want) {
			t.Errorf("bootLines(%v) = %q, want %q", tc.boot, got, tc.want)
		}
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
