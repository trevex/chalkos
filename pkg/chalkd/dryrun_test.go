package chalkd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/outputs"
)

// dryRun sends text as a dry run.
func dryRun(s *Server, text string) (*nodev1.ApplyConfigResponse, error) {
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: text, DryRun: true}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// A dry run plans the delivery, the outputs it renders and the effects they fire, and changes
// nothing: the slot, the outputs the node serves, the baseline and the outputs' status stay as
// they were, and no unit restarts.
func TestDryRunPlansAndChangesNothing(t *testing.T) {
	s, r := configServer(t)
	before := readFile(t, filepath.Join(s.Loader.Render.Root, "demo", "demo.json"))
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	resp, err := dryRun(s, next)
	if err != nil {
		t.Fatal(err)
	}
	want := &nodev1.Plan{
		Version: hashOf(t, next), Replaces: hashOf(t, baseConfig), Outputs: []string{"demo"},
		Effects: []*nodev1.PlannedEffect{{Name: "demo", FiredBy: []string{"output demo"}, Actions: []string{"restart demo.service"}}},
	}
	if !proto.Equal(resp.Plan, want) || len(resp.Changes) != 0 || len(resp.RestartedUnits) != 0 {
		t.Errorf("response = %v, want the plan %v", resp, want)
	}
	if len(r.Calls) != 0 {
		t.Errorf("ran %v", r.Calls)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
		t.Errorf("stored %s", v)
	}
	if got := readFile(t, filepath.Join(s.Loader.Render.Root, "demo", "demo.json")); got != before {
		t.Errorf("the output the node serves changed to %s", got)
	}
	if _, err := os.Stat(s.Paths.EffectsBaseline); !os.IsNotExist(err) {
		t.Errorf("a dry run left a baseline: %v", err)
	}
	if staged, _ := os.ReadDir(filepath.Join(s.Loader.Render.Root, ".staging")); len(staged) != 0 {
		t.Errorf("a dry run left staged outputs: %v", staged)
	}
	// The delivery the dry run planned takes the effects it planned.
	applied, err := sendConfig(s, next)
	if err != nil || !slices.Equal(applied.RestartedUnits, []string{"demo.service"}) {
		t.Errorf("the delivery restarted %v: %v", applied.GetRestartedUnits(), err)
	}
}

// A rotated Secret a Demo references fires the effect on the demo output, whose files stay the
// same, and the plan names the output.
func TestDryRunPlansARotatedSecret(t *testing.T) {
	s, _ := configServer(t)
	resp, err := dryRun(s, strings.Replace(baseConfig, "token: s3cret", "token: n3w", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.Plan.Outputs, []string{"demo"}) || len(resp.Plan.Effects) != 1 || resp.Plan.Effects[0].Name != "demo" {
		t.Errorf("plan = %v", resp.Plan)
	}
	if resp, err := dryRun(s, baseConfig); err != nil || len(resp.Plan.Outputs) != 0 || len(resp.Plan.Effects) != 0 {
		t.Errorf("the config the node runs plans %v: %v", resp.GetPlan(), err)
	}
}

// A dry run refuses what a delivery refuses, with its code and message, and records no output's
// failure in the status.
func TestDryRunRefusesAsADelivery(t *testing.T) {
	for name, tc := range map[string]struct {
		output bool
		next   string
	}{
		"an invalid set":        {false, baseConfig + "---\napiVersion: demo.example.org/v1alpha1\nkind: Demo\nmetadata: {name: one}\nspec: {name: twice}\n"},
		"a renderer that fails": {true, strings.Replace(baseConfig, "name: first", "name: second", 1)},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := configServer(t)
			if tc.output {
				writeOutputs(t, s, map[string]outputs.Definition{"demo": demoOutput(t, s, `grep -q first`, "")})
			}
			_, dryErr := dryRun(s, tc.next)
			if st, err := outputs.ReadStatus(s.Loader.Render.Root); err != nil || st["demo"].Error != "" {
				t.Errorf("status after the dry run = %+v, %v", st, err)
			}
			_, err := sendConfig(s, tc.next)
			if dryErr == nil || err == nil || dryErr.Error() != err.Error() || connect.CodeOf(dryErr) != connect.CodeOf(err) {
				t.Errorf("dry run: %v; delivery: %v", dryErr, err)
			}
		})
	}
}

// A dry run checks a delivery to the running image alone.
func TestDryRunRefusesTheStagedImage(t *testing.T) {
	s, _ := configServer(t)
	_, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true, DryRun: true}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "a dry run checks a delivery to the running image") {
		t.Errorf("err = %v", err)
	}
}
