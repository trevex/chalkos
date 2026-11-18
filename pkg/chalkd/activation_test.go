package chalkd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

func TestApplyReportsStoredAndActivatedVersionsAndResumes(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		retryBoot  string
		retryState string
		change     bool
		restart    bool
	}{
		{name: "same boot and plan", retryBoot: "boot-one", retryState: "active"},
		{name: "reboot", retryBoot: "boot-two", retryState: "active", restart: true},
		{name: "unit no longer active", retryBoot: "boot-one", retryState: "failed", restart: true},
		{name: "new target config", retryBoot: "boot-one", retryState: "active", change: true, restart: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, r := configServer(t)
			write(t, s.Paths.BootID, "boot-one\n")
			if _, err := sendConfig(s, baseConfig); err != nil {
				t.Fatal(err)
			}
			writeEffects(t, s, `{
			  "network": {"outputs": ["demo"], "restart": ["network.service"]},
			  "demo": {"outputs": ["demo"], "after": ["network"], "restart": ["demo.service"],
			    "waitFor": {"description": "the route", "argv": ["/nix/store/demo/bin/check-route"], "timeoutSeconds": 1}}
			}`)
			r.Rules = append([]tooltest.Rule{
				{Prefix: "systemctl show --property=ActiveState --value network.service", Out: "active\n"},
				{Prefix: "/nix/store/demo/bin/check-route", Err: &tool.Error{Code: 1}, Times: 1},
			}, r.Rules...)
			next := strings.Replace(baseConfig, "name: first", "name: second", 1)
			r.ResetCalls()
			if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "probe for the route") {
				t.Fatalf("apply = %v", err)
			}
			if !slices.Contains(r.Calls, "systemctl try-restart network.service") {
				t.Fatalf("first attempt: %v", r.Calls)
			}
			resp, err := s.Status(t.Context(), connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			a := resp.Msg.ConfigActivation
			target, completed := hashOf(t, next), hashOf(t, baseConfig)
			if resp.Msg.ConfigVersion != target || a.GetCompletedVersion() != completed ||
				a.GetTargetVersion() != target || a.GetState() != activationFailed || a.GetFailedStep() != "effect batch 2 (demo)" {
				t.Fatalf("stored = %q, activation = %v", resp.Msg.ConfigVersion, a)
			}
			record, err := s.readActivation()
			if err != nil || record.CompletedBatches != 1 {
				t.Fatalf("checkpoint = %+v, %v", record, err)
			}
			if info, err := os.Stat(s.activationPath()); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("activation record mode = %v, %v", info, err)
			}

			write(t, s.Paths.BootID, scenario.retryBoot+"\n")
			r.Rules[0].Out = scenario.retryState + "\n"
			if scenario.change {
				next = strings.Replace(next, "name: second", "name: third", 1)
			}
			r.ResetCalls()
			if _, err := sendConfig(s, next); err != nil {
				t.Fatal(err)
			}
			ranNetwork := slices.Contains(r.Calls, "systemctl try-restart network.service") ||
				slices.Contains(r.Calls, "systemctl restart network.service")
			if ranNetwork != scenario.restart || !slices.Contains(r.Calls, "/nix/store/demo/bin/check-route") ||
				!slices.Contains(r.Calls, "systemctl try-restart demo.service") {
				t.Fatalf("retry commands = %v; want network restart %t", r.Calls, scenario.restart)
			}
			record, err = s.readActivation()
			if err != nil || record.Completed != hashOf(t, next) || record.State != activationComplete ||
				record.FailedStep != "" || record.CompletedBatches != 0 {
				t.Fatalf("completed record = %+v, %v", record, err)
			}
		})
	}
}

func TestActivationStatusAfterDaemonOrBootRestart(t *testing.T) {
	s, _ := configServer(t)
	dir := slotOf(t, s)
	r := activationRecord{
		Slot: filepath.Base(dir),
		Boot: "boot-one", Attempt: 41, Target: "v2", Completed: "v1", CompletedBoot: "boot-one",
		State: activationRunning, Step: "publish rendered outputs",
	}
	if err := s.writeActivation(r); err != nil {
		t.Fatal(err)
	}
	s.activating.Store(r.Attempt)
	if a := s.activationStatus(dir, "v2", "boot-one"); a.GetState() != activationRunning || a.GetStep() != r.Step {
		t.Fatalf("running activation = %v", a)
	}
	s.activating.Store(0)
	if a := s.activationStatus(dir, "v2", "boot-one"); a.GetState() != "interrupted" || a.GetFromEarlierBoot() {
		t.Fatalf("interrupted activation = %v", a)
	}
	if a := s.activationStatus(dir, "v2", "boot-two"); a.GetState() != "interrupted" ||
		!a.GetFromEarlierBoot() || !a.GetAttemptFromEarlierBoot() {
		t.Fatalf("prior boot activation = %v", a)
	}
	if a := s.activationStatus(dir+"-new-image", "v2", "boot-two"); a.GetState() != "unrecorded" || a.GetCompletedVersion() != "" {
		t.Fatalf("other image activation = %v", a)
	}
}
