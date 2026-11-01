package chalkd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/pki"
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

// A dry run leaves the baseline an earlier delivery that failed part way recorded as it was,
// whether it plans the delivery or refuses it, so the next delivery compares with what that
// delivery may have changed.
func TestDryRunLeavesTheBaseline(t *testing.T) {
	s, _ := configServer(t)
	earlier := `{"documents":[],"facts":{}}`
	write(t, s.Paths.EffectsBaseline, earlier)
	if _, err := dryRun(s, nextConfig); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, s.Paths.EffectsBaseline); got != earlier {
		t.Errorf("baseline after a dry run = %s, want the earlier one %s", got, earlier)
	}
	invalid := baseConfig + "---\napiVersion: demo.example.org/v1alpha1\nkind: Demo\nmetadata: {name: one}\nspec: {name: twice}\n"
	if _, err := dryRun(s, invalid); err == nil {
		t.Fatal("an invalid set passed the dry run")
	}
	if got := readFile(t, s.Paths.EffectsBaseline); got != earlier {
		t.Errorf("baseline after a refused dry run = %s, want the earlier one %s", got, earlier)
	}
}

// A dry run of a config that adds a volume answers with the storage change and makes none: the
// volume is neither created nor recorded.
func TestDryRunAddsNoVolume(t *testing.T) {
	s, r := configServer(t)
	r.Rules = volumeRules(s)
	recorded := filepath.Join(s.Paths.StateDir, "storage", "storage.json")
	before := readFile(t, recorded)
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), FallbackSecret: secret, DryRun: true}))
	if err != nil {
		t.Fatal(err)
	}
	if c := resp.Msg.Changes; len(c) != 1 || c[0].Volume != "extra" {
		t.Errorf("changes = %v, want the new volume", c)
	}
	for _, c := range r.Calls {
		for _, changes := range []string{"systemd-repart --dry-run=no", "systemd-cryptenroll", "systemctl", "mkfs", "cryptsetup luksFormat"} {
			if strings.HasPrefix(c, changes) {
				t.Errorf("the dry run ran %s", c)
			}
		}
	}
	if got := readFile(t, recorded); got != before {
		t.Errorf("storage.json after a dry run = %s, want %s", got, before)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
		t.Errorf("stored %s", v)
	}
}

// A dry run that carries a node certificate or OS CAs checks them and keeps the certificate the
// node serves and the OS CAs it trusts, in memory and on STATE.
func TestDryRunKeepsTheCertificateAndTrust(t *testing.T) {
	s, _ := configServer(t)
	osCA, err := testOSCA()
	if err != nil {
		t.Fatal(err)
	}
	next, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, admin := adminOf(t, osCA)
	renewed, err := pki.IssueNode(kubernetestest.NodeCA(t), pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Paths.StateDir, "chalkd")
	fingerprint, trusted := s.Certificate.Fingerprint(), s.Certificate.OSCA()
	certFile, caFile := readFile(t, filepath.Join(dir, NodeCertificateFile)), readFile(t, filepath.Join(dir, CAFile))
	for name, req := range map[string]*nodev1.ApplyConfigRequest{
		"a node certificate": {NodeCertificate: []byte(renewed.Certificate), NodeKey: []byte(renewed.Key), DryRun: true},
		"OS CAs":             {Trust: []byte(pki.Bundle(osCA.Certificate, next.Certificate)), DryRun: true},
	} {
		if _, err := s.ApplyConfig(asCaller(admin), connect.NewRequest(req)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if s.Certificate.Fingerprint() != fingerprint || s.Certificate.OSCA() != trusted {
			t.Errorf("%s: the dry run changed the certificate the node serves or the OS CAs it trusts", name)
		}
		if readFile(t, filepath.Join(dir, NodeCertificateFile)) != certFile || readFile(t, filepath.Join(dir, CAFile)) != caFile {
			t.Errorf("%s: the dry run changed the certificate or the OS CAs on STATE", name)
		}
	}
}

// A dry run is refused while a config is on trial, as any delivery is, and leaves the trial open.
func TestDryRunWaitsForATrial(t *testing.T) {
	s, r := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	r.Calls = nil
	_, err := dryRun(s, baseConfig)
	want := "config " + rec.Version + " is on trial until " + rec.Deadline.Format(time.RFC3339) + ": confirm it with chalkctl apply-config --confirm n1, or roll it back with chalkctl apply-config --rollback n1, before another delivery"
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want %q", err, want)
	}
	if !trialOpenOn(t, s) || len(r.Calls) != 0 {
		t.Errorf("the refusal ended the trial or ran %v", r.Calls)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Version {
		t.Errorf("the node runs %s, want the config on trial", v)
	}
}
