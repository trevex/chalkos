package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
)

// vetoes is a validator's program that refuses a Demo named second, and writes the names it read
// to standard error, which goes to the journal alone.
const vetoes = `docs=$(cat)
echo "read $docs" >&2
case $docs in
*'"name":"second"'*) echo '{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name", "message": "the name is vetoed"}]}' ;;
esac`

// writeValidators declares the image's validators at the test server's path, each with the shell
// commands given as its program, reading the Demo documents.
func writeValidators(t *testing.T, s *Server, programs map[string]string) {
	t.Helper()
	defs := map[string]outputs.Validator{}
	for name, commands := range programs {
		defs[name] = outputs.Validator{Inputs: []string{"Demo"}, Program: shellScript(t, commands)}
	}
	data, err := json.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	writeContract(t, s, contract.ValidatorsFile, string(data))
}

// A validator that refuses or fails refuses a delivery after the rules and before the renderers,
// before it changes anything: the refusal reads as a renderer's, and what the program wrote to
// standard error stays in the journal. A dry run refuses it alike.
func TestApplyConfigRefusesByAValidator(t *testing.T) {
	for name, tc := range map[string]struct{ program, want string }{
		"a refusal": {vetoes, "the config is refused: Demo/one: spec.name: the name is vetoed"},
		"a failure": {`echo "read $(cat)" >&2; exit 2`, "validator veto: its program failed (exit 2); see the node's journal"},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := configServer(t)
			writeValidators(t, s, map[string]string{"veto": tc.program})
			// The renderer would fail too; the validator refuses first.
			writeOutputs(t, s, map[string]outputs.Definition{"demo": demoOutput(t, s, `exit 3`, "")})
			var logged bytes.Buffer
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			next := strings.Replace(baseConfig, "name: first", "name: second", 1)
			_, dryErr := dryRun(s, next)
			_, err := sendConfig(s, next)
			for _, err := range []error{dryErr, err} {
				if codeOf(err) != connect.CodeFailedPrecondition || !strings.HasSuffix(err.Error(), tc.want) || strings.Contains(err.Error(), "read ") {
					t.Errorf("err = %v, want %q", err, tc.want)
				}
			}
			if tc.program != vetoes && !strings.Contains(logged.String(), "validator veto: its program") {
				t.Errorf("the journal lacks the failure: %s", logged.String())
			}
			if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) || len(r.Calls) != 0 {
				t.Errorf("stored %s, ran %v", v, r.Calls)
			}
			if st, err := outputs.ReadStatus(s.Loader.Render.Root); err != nil || st["demo"].Error != "" {
				t.Errorf("the renderers ran: status = %+v, %v", st, err)
			}
			if _, err := os.Stat(filepath.Join(s.Loader.Render.Root, ".validating")); !os.IsNotExist(err) {
				t.Errorf("the validators' directory stays: %v", err)
			}
		})
	}
}

// A validator that passes leaves the delivery to the renderers and the effects; one that a
// delivery without config would fail does not run, since the node keeps its documents.
func TestApplyConfigPassesAValidator(t *testing.T) {
	s, _ := configServer(t)
	writeValidators(t, s, map[string]string{"veto": vetoes})
	resp, err := sendConfig(s, strings.Replace(baseConfig, "name: first", "name: third", 1))
	if err != nil || len(resp.RestartedUnits) != 1 {
		t.Fatalf("the delivery: %v, %v", resp, err)
	}
	writeValidators(t, s, map[string]string{"veto": `exit 2`})
	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Trust: []byte(s.Certificate.OSCA())})); err != nil {
		t.Errorf("a delivery without config ran the validators: %v", err)
	}
}

// A delivery refused by a validator reads as the image's checker refuses the same config on the
// same image, at the stage of the validators.
func TestApplyConfigRefusesByAValidatorAsTheChecker(t *testing.T) {
	s, _ := configServer(t)
	writeValidators(t, s, map[string]string{"veto": vetoes})
	c := s.Loader.Contract
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	docs, err := config.Parse(strings.NewReader(next))
	if err != nil {
		t.Fatal(err)
	}
	img := check.Image{Schemas: c.Schemas, Target: config.Target{Platform: "metal"}, Rules: mustRules(t, c), Validators: c.Validators, Render: s.Loader.Render}
	_, checked := check.Run(context.Background(), img, check.Input{Node: "n1", Delivered: config.Redact(docs), Facts: func(_, _ config.Redacted) rules.Facts { return nil }})
	r, ok := errors.AsType[*check.Refusal](checked)
	if !ok || r.Stage != check.StageValidators {
		t.Fatalf("check.Run: %v", checked)
	}
	_, delivered := sendConfig(s, next)
	if ce, ok := rpcErr(delivered); !ok || ce.Message() != r.Message {
		t.Errorf("the delivery is refused with %v, check.Run with %q", delivered, r.Message)
	}
}

// An install in place runs the running image's validators before it installs anything.
func TestInstallInPlaceRunsTheValidators(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	writeSchemas(t, s)
	writeValidators(t, s, map[string]string{"veto": `exit 2`})
	installed := false
	s.InPlace = func(context.Context, install.Request, []byte) error { installed = true; return nil }
	h := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	h.Config = baseConfig
	err := sendInstall(t, s, h, nil)
	if installed || connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "validator veto: its program failed (exit 2)") {
		t.Errorf("install: %v, installed %v", err, installed)
	}
}
