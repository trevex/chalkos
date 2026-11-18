package chalkd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/internal/testwait"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
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
			testwait.Bubble(t, func(t *testing.T) {
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

// TestDeliveryGoesOnWithoutTheActivationRecord: a record chalkd cannot write fails no delivery,
// and a delivery that fails before its slot still acts on chalkd's loops.
func TestDeliveryGoesOnWithoutTheActivationRecord(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s, r := configServer(t)
	addKubernetes(t, s, k8s.KindWorker, true)
	writeContract(t, s, contract.EffectsFile, `{"named": {"when": "config[\"demo.example.org/v1alpha1\"].Demo.one.spec.name != oldConfig[\"demo.example.org/v1alpha1\"].Demo.one.spec.name", "chalkd": "kubernetes:reload"},
	  "demo": {"outputs": ["demo"], "restart": ["demo.service"]}}`)
	var acted []string
	s.actedOnSelf = func(name string) { acted = append(acted, name) }
	// A directory that is not empty takes no record in its place.
	write(t, filepath.Join(s.activationPath(), "in-the-way"), "")
	slots := filepath.Dir(slotOf(t, s))
	if err := os.Chmod(slots, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slots, 0o755) })
	if _, err := sendConfig(s, nextConfig); err == nil || !strings.Contains(err.Error(), "store the config") {
		t.Fatalf("err = %v, want the slot's failure", err)
	}
	if !slices.Equal(acted, []string{"kubernetes:reload"}) {
		t.Fatalf("the failed delivery acted on itself %v, want kubernetes:reload", acted)
	}
	if err := os.Chmod(slots, 0o755); err != nil {
		t.Fatal(err)
	}
	acted = nil
	r.ResetCalls()
	if _, err := sendConfig(s, nextConfig); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(acted, []string{"kubernetes:reload"}) || !slices.Contains(r.Calls, "systemctl try-restart demo.service") {
		t.Errorf("acted on itself %v, commands %v", acted, r.Calls)
	}
}

// TestActivationStatusOfADeliveryThatFinishesDuringTheRead: a delivery that finishes between the
// read of its running record and that of the live attempt is read again, not reported interrupted.
func TestActivationStatusOfADeliveryThatFinishesDuringTheRead(t *testing.T) {
	var activating atomic.Int64
	activating.Store(41)
	reads := 0
	read := func() (activationRecord, error) {
		reads++
		if reads == 1 {
			// The delivery finishes once its running record was read.
			activating.Store(0)
			return activationRecord{Attempt: 41, State: activationRunning}, nil
		}
		return activationRecord{Attempt: 41, State: activationComplete}, nil
	}
	r, live, err := consistentActivation(&activating, read)
	if err != nil || r.State != activationComplete || live != 0 || reads != 2 {
		t.Fatalf("record = %+v, live = %d, %v after %d reads", r, live, err, reads)
	}
}
