package chalkd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/imagecheck"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/storage"
)

// recording runs the checker as run does and keeps the input of each run.
type recording struct {
	run    imagecheck.Runner
	inputs []*checkv1.Input
	// answer, when set, replaces the result.
	answer func(*checkv1.Result)
}

func (r *recording) Run(ctx context.Context, img imagecheck.Image, in *checkv1.Input, f imagecheck.Facts) (*checkv1.Result, error) {
	r.inputs = append(r.inputs, in)
	res, err := r.run.Run(ctx, img, in, f)
	if err == nil && r.answer != nil {
		r.answer(res)
	}
	return res, err
}

// withOutputs gives the image of st the outputs defs, as its system's outputs.json declares them.
func withOutputs(t *testing.T, st dirStore, defs map[string]outputs.Definition) {
	t.Helper()
	data, err := json.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(string(st), "nix/store/x/etc/chalkos/outputs.json"), string(data))
}

// An upgrade's config is checked by the image's renderers, which the running image lacks: the
// image refuses a Demo its renderer fails on, and its slot is not written.
func TestStageConfigRunsTheImagesRenderers(t *testing.T) {
	s, _ := configServer(t)
	st := imageWith(t, s, fixtureSchemas(t), "")
	demo := demoOutput(t, s, `grep -q second && exit 5; cat > "$OUT/demo.json"`, "")
	demo.Dir = "/run/chalkos/outputs/demo"
	withOutputs(t, st, map[string]outputs.Definition{"demo": demo})
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	_, err := s.stageConfig(context.Background(), st, "/nix/store/x", "metal", newImage, next, "")
	if err == nil || err.Error() != "output demo: its renderer failed (exit 5); see the node's journal" {
		t.Errorf("err = %v, want the image's renderer's refusal", err)
	}
	if _, found := staged(t, s); found {
		t.Error("wrote the image's slot")
	}
	if _, err := s.stageConfig(context.Background(), st, "/nix/store/x", "metal", newImage, baseConfig, ""); err != nil {
		t.Errorf("a Demo the renderer takes: %v", err)
	}
}

// The checker gets the config with its secret values hashed, the delivered and the running
// config alike, and the slot keeps the delivered values.
func TestImageCheckGetsNoSecretValue(t *testing.T) {
	s, _ := configServer(t)
	rec := &recording{run: s.CheckImage}
	s.CheckImage = rec
	if _, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, strings.Replace(baseConfig, "s3cret", "n3w", 1), ""); err != nil {
		t.Fatal(err)
	}
	if len(rec.inputs) != 1 || len(rec.inputs[0].GetDelivered()) != 3 || len(rec.inputs[0].GetOld()) != 3 {
		t.Fatalf("inputs = %v", rec.inputs)
	}
	for _, d := range append(rec.inputs[0].GetDelivered(), rec.inputs[0].GetOld()...) {
		if strings.Contains(d, "s3cret") || strings.Contains(d, "n3w") {
			t.Errorf("the checker got a secret value: %s", d)
		}
	}
	got, _ := staged(t, s)
	for _, d := range got.Documents {
		if d.IsSecret() && d.Object["spec"].(map[string]any)["data"].(map[string]any)["token"] != "n3w" {
			t.Errorf("the slot keeps %v, want the delivered value", d.Object)
		}
	}
}

// The checker compares the config with the running slot's documents as they were delivered,
// without the running image's defaults, such as the Demo's mode: the image fills in its own.
func TestImageCheckComparesWithTheRunningConfigAsDelivered(t *testing.T) {
	s, _ := configServer(t)
	rec := &recording{run: s.CheckImage}
	s.CheckImage = rec
	if _, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, "", ""); err != nil {
		t.Fatal(err)
	}
	_, running, _, _ := s.runningSlot()
	for _, d := range running.Documents {
		if d.Kind == "Demo" && d.Object["spec"].(map[string]any)["mode"] != "safe" {
			t.Fatalf("the running slot's Demo %v lacks the default mode", d.Object)
		}
	}
	if len(rec.inputs) != 1 || len(rec.inputs[0].GetOld()) != 3 {
		t.Fatalf("inputs = %v", rec.inputs)
	}
	for _, d := range rec.inputs[0].GetOld() {
		if strings.Contains(d, `"mode"`) {
			t.Errorf("the checker got the old document %s, with the running image's default mode", d)
		}
	}
}

// An image without a checker, as one built before chalkd check, cannot check its config and is
// refused; so is a checker whose documents carry other secret values than were delivered.
func TestImageCheckRefuses(t *testing.T) {
	s, _ := configServer(t)
	old := imageWith(t, s, fixtureSchemas(t), "")
	if err := os.Remove(filepath.Join(string(old), "nix/store/x/sw/bin/chalkd")); err != nil {
		t.Fatal(err)
	}
	_, err := s.stageConfig(context.Background(), old, "/nix/store/x", "metal", newImage, baseConfig, "")
	if err == nil || err.Error() != fmt.Sprintf("the image %x predates chalkd check; it cannot check its config", newImage[:6]) {
		t.Errorf("an image without a checker: %v", err)
	}
	s.CheckImage = &recording{run: s.CheckImage, answer: func(res *checkv1.Result) {
		for i, d := range res.Documents {
			res.Documents[i] = strings.Replace(d, `"token":"`, `"token":"0`, 1)
		}
	}}
	_, err = s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, baseConfig, "")
	if err == nil || !strings.Contains(err.Error(), `the image's checker gave back other documents than it got: Secret "demo" holds other secret values than were delivered`) {
		t.Errorf("other secret values: %v", err)
	}
	if _, found := staged(t, s); found {
		t.Error("wrote the image's slot")
	}
}

// The checker's facts are those the image declares, which chalkd loads, with an override in place
// of what the node holds.
func TestStagedFacts(t *testing.T) {
	s, _ := configServer(t)
	st := imageWith(t, s, nil, "")
	probe := filepath.Join(t.TempDir(), "probe.json")
	write(t, probe, `"image"`)
	write(t, filepath.Join(string(st), "nix/store/x/etc/chalkos/facts.json"), `{"probe": {"file": "`+probe+`"}, "recorded": {"file": "/nonexistent"}}`)
	section := storage.Section{Fallback: "recovery-key"}
	f, err := s.stagedFacts(context.Background(), st, "/nix/store/x", imageCheck{recorded: &section})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := f.Fact("probe", nil); err != nil || v != "image" {
		t.Errorf("probe = %v, %v", v, err)
	}
	if v, err := f.Fact("recorded", nil); err != nil || v.(map[string]any)["fallback"] != "recovery-key" {
		t.Errorf("recorded = %v, %v; want the override", v, err)
	}
	if v, err := f.Fact("share", nil); err != nil || v != nil {
		t.Errorf("share = %v, %v; want null, as the image declares none", v, err)
	}
}

// A dry run for the staged image has the image check the config and plans the versions and the
// storage changes, and changes nothing: neither the machine nor the staged slot.
func TestApplyConfigStagedDryRun(t *testing.T) {
	s, r := stagedServer(t, fixtureSchemas(t))
	r.Rules = volumeRules(s)
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), Staged: true, DryRun: true, FallbackSecret: secret}))
	if err != nil {
		t.Fatal(err)
	}
	if p := resp.Msg.GetPlan(); p.GetVersion() != hashOf(t, extraConfig("ext4")) || p.GetReplaces() != hashOf(t, baseConfig) || len(p.GetOutputs()) != 0 || len(p.GetEffects()) != 0 {
		t.Errorf("plan = %v", p)
	}
	if c := resp.Msg.GetChanges(); len(c) != 1 || c[0].GetVolume() != "extra" {
		t.Errorf("changes = %v, want the new volume", c)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
		t.Errorf("staged version = %q, want it unchanged", v)
	}
	if changed := withoutChecks(r.Calls); len(changed) != 0 {
		t.Errorf("a dry run ran %v", changed)
	}
	// A refusal is a delivery's.
	_, err = s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: strings.Replace(baseConfig, "{disk: {path: /dev/vda}}", "{disk: {path: /dev/vda}, var: {size: 1G}}", 1), Staged: true, DryRun: true}))
	if codeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "only a reinstall of") {
		t.Errorf("err = %v", err)
	}
}

// The warnings of the staged image's check reach the answer to config apply --staged, a dry run's
// too.
func TestApplyConfigStagedWarns(t *testing.T) {
	s, _ := stagedServer(t, fixtureSchemas(t))
	s.CheckImage = &recording{run: s.CheckImage, answer: func(res *checkv1.Result) { res.Warnings = []string{"effect demo: cannot evaluate"} }}
	for _, dryRun := range []bool{true, false} {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true, DryRun: dryRun}))
		if err != nil || !slices.Equal(resp.Msg.GetWarnings(), []string{"effect demo: cannot evaluate"}) {
			t.Errorf("dry run %v: warnings = %v, %v", dryRun, resp.Msg.GetWarnings(), err)
		}
	}
}
