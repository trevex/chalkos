package chalkctl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/config/slot"
)

// timeSync is a TimeSync document of the server given, to make n1's config another version.
func timeSync(host string) string {
	return "apiVersion: time.chalkos.dev/v1alpha1\nkind: TimeSync\nmetadata: {name: default}\nspec: {servers: [{host: " + host + "}]}\n"
}

// rewriteTrial changes the fields of the trial record of a node installedNode made, as chalkd
// writes them, such as one a rollback at the deadline left.
func rewriteTrial(t *testing.T, s *chalkd.Server, fields map[string]any) {
	t.Helper()
	path := filepath.Join(s.Paths.StateDir, "chalkd", "trial")
	r := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range fields {
		r[k] = v
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
}

// TestApplyConfigOnTrial applies a config on trial and confirms it over a new connection once the
// node is healthy; a trial left open with --no-confirm shows in status, holds back other
// deliveries, ends with --confirm or with --rollback.
func TestApplyConfigOnTrial(t *testing.T) {
	ta := newTestApp(t)
	ta.pollInterval = 20 * time.Millisecond
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	run := func(args ...string) error {
		ta.stdout.Reset()
		return ta.run(context.Background(), ta.args(args, addr))
	}
	contains := func(lines ...string) {
		t.Helper()
		for _, line := range lines {
			if !strings.Contains(ta.stdout.String(), line) {
				t.Errorf("output lacks %q:\n%s", line, ta.stdout)
			}
		}
	}
	if err := run("apply-config", "n1"); err != nil {
		t.Fatal(err)
	}
	first := ta.configVersion(t)
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp.lan"))
	second := ta.configVersion(t)
	if err := run("apply-config", "n1", "--try", "1m"); err != nil {
		t.Fatalf("%v\n%s", err, ta.stdout)
	}
	contains("n1 runs config "+second+" on trial; it rolls back to config "+first+" by itself unless confirmed within 1m0s",
		"n1: waiting for a new connection and the node's health to confirm config "+second,
		"n1 keeps config "+second+": the trial is confirmed")
	if err := run("apply-config", "n1", "--try", "1m"); err == nil || !strings.Contains(err.Error(), "n1 runs config "+second+" already; --try needs a config that changes it") {
		t.Errorf("--try with the running config: %v", err)
	}

	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp2.lan"))
	third := ta.configVersion(t)
	if err := run("apply-config", "n1", "--try", "1m", "--no-confirm"); err != nil {
		t.Fatal(err)
	}
	contains("chalkctl apply-config --confirm n1 keeps it; chalkctl apply-config --rollback n1 rolls it back now")
	if err := run("status", "n1"); err != nil {
		t.Fatal(err)
	}
	contains("config " + third + " on trial until ")
	contains(": it rolls back to config " + second + " unless apply-config --confirm confirms it")
	if err := run("apply-config", "n1"); err == nil || !strings.Contains(err.Error(), "n1 runs config "+third+" on trial until ") || !strings.Contains(err.Error(), "confirm it with chalkctl apply-config --confirm n1, or roll it back with chalkctl apply-config --rollback n1, first") {
		t.Errorf("a delivery during the trial: %v", err)
	}
	if err := run("apply-config", "n1", "--diff"); err != nil {
		t.Errorf("--diff during the trial: %v", err)
	}
	if err := run("apply-config", "n1", "--confirm"); err != nil {
		t.Fatal(err)
	}
	contains("n1 keeps config " + third + ": the trial is confirmed")
	if err := run("apply-config", "n1", "--confirm"); err == nil || !strings.Contains(err.Error(), "n1 has no open trial to confirm; it runs config "+third) {
		t.Errorf("--confirm without a trial: %v", err)
	}

	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp3.lan"))
	fourth := ta.configVersion(t)
	if err := run("apply-config", "n1", "--try", "1m", "--no-confirm"); err != nil {
		t.Fatal(err)
	}
	if err := run("apply-config", "n1", "--rollback"); err != nil {
		t.Fatal(err)
	}
	contains("n1 runs config "+third+" again; config "+fourth+" is its previous one now", "n1: the trial of config "+fourth+" ended")
	if v, _ := slot.Version(slotOf(t, s)); v != third {
		t.Errorf("the node runs %s, want %s", v, third)
	}
	if err := run("apply-config", "n1"); err != nil {
		t.Errorf("a delivery after the trial ended: %v", err)
	}
}

// TestApplyConfigOnTrialUnconfirmed: a node not found healthy within its trial rolls the config
// back by itself at the deadline; chalkctl says so and fails with the reason.
func TestApplyConfigOnTrialUnconfirmed(t *testing.T) {
	defer func(m time.Duration) { minTrial = m }(minTrial)
	minTrial = time.Second
	ta := newTestApp(t)
	ta.pollInterval = 50 * time.Millisecond
	s, _ := installedNode(t, ta, []byte(`{}`))
	s.Healthy = func(context.Context, []string) error { return errors.New("units failed: demo.service") }
	addr := ta.startNode(t, s)
	run := func(args ...string) error {
		ta.stdout.Reset()
		return ta.run(context.Background(), ta.args(args, addr))
	}
	if err := run("apply-config", "n1"); err != nil {
		t.Fatal(err)
	}
	first := ta.configVersion(t)
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp.lan"))
	second := ta.configVersion(t)
	err := run("apply-config", "n1", "--try", "1s")
	if err == nil || !strings.Contains(err.Error(), "n1: config "+second+" was not confirmed within its trial: ") || !strings.Contains(err.Error(), "the node is not healthy: units failed: demo.service") {
		t.Fatalf("an unconfirmed trial = %v\n%s", err, ta.stdout)
	}
	if !strings.Contains(ta.stdout.String(), "n1 rolled back to config "+first+" at the trial's deadline") {
		t.Errorf("output lacks the rollback:\n%s", ta.stdout)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != first {
		t.Errorf("the node runs %s, want %s", v, first)
	}
}

// TestConfirmAfterAFailedRollback confirms a trial past its deadline whose rollback failed and is
// tried again, which the node still lets a confirmation end, within a window of chalkctl's own;
// once a failed rollback changed the node, the node refuses, and chalkctl says nothing rolled back.
func TestConfirmAfterAFailedRollback(t *testing.T) {
	ta := newTestApp(t)
	ta.pollInterval = 20 * time.Millisecond
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	run := func(args ...string) error {
		ta.stdout.Reset()
		return ta.run(context.Background(), ta.args(args, addr))
	}
	if err := run("apply-config", "n1"); err != nil {
		t.Fatal(err)
	}
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp.lan"))
	second := ta.configVersion(t)
	if err := run("apply-config", "n1", "--try", "1m", "--no-confirm"); err != nil {
		t.Fatal(err)
	}
	// The armed deadline is of the record as written, so it leaves the rewritten one alone.
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	rewriteTrial(t, s, map[string]any{"deadline": past, "error": "output demo failed", "attempts": 1})
	if err := run("status", "n1"); err != nil || !strings.Contains(ta.stdout.String(), "; the rollback at the deadline failed and is tried again each minute: output demo failed") {
		t.Errorf("status = %v:\n%s", err, ta.stdout)
	}
	if err := run("apply-config", "n1", "--confirm"); err != nil {
		t.Fatalf("--confirm after a failed rollback = %v\n%s", err, ta.stdout)
	}
	if want := "n1 keeps config " + second + ": the trial is confirmed"; !strings.Contains(ta.stdout.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, ta.stdout)
	}

	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp2.lan"))
	if err := run("apply-config", "n1", "--try", "1m", "--no-confirm"); err != nil {
		t.Fatal(err)
	}
	rewriteTrial(t, s, map[string]any{"deadline": past, "error": "restart demo.service failed", "attempts": 1, "changed": true})
	if err := run("status", "n1"); err != nil || !strings.Contains(ta.stdout.String(), "; the rollback at the deadline failed after it changed the node and is tried again each minute: restart demo.service failed; apply-config --confirm no longer keeps the config, and apply-config --rollback finishes the rollback now") {
		t.Errorf("status = %v:\n%s", err, ta.stdout)
	}
	err := run("apply-config", "n1", "--confirm")
	if err == nil || !strings.Contains(err.Error(), "chalkctl apply-config --rollback n1 finishes it now") {
		t.Errorf("--confirm after a rollback that changed the node = %v", err)
	}
	if strings.Contains(ta.stdout.String(), "rolled back") {
		t.Errorf("chalkctl reports a rollback:\n%s", ta.stdout)
	}
}

// TestTrialThatEnded: a trial whose slot runs the config from before it, as a rollback at the
// deadline that failed after it wrote the slot leaves it, holds back no delivery, and --rollback
// delivers the running config again, which the node keeps.
func TestTrialThatEnded(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	run := func(args ...string) error {
		ta.stdout.Reset()
		return ta.run(context.Background(), ta.args(args, addr))
	}
	if err := run("apply-config", "n1"); err != nil {
		t.Fatal(err)
	}
	first := ta.configVersion(t)
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp.lan"))
	second := ta.configVersion(t)
	if err := run("apply-config", "n1"); err != nil {
		t.Fatal(err)
	}
	// The slot runs second with first as its previous config: the trial of first ended.
	ended := map[string]any{"slot": filepath.Base(slotOf(t, s)), "version": first, "previous": second, "deadline": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), "error": "restart demo.service failed", "changed": true}
	rewriteTrial(t, s, ended)
	if err := run("apply-config", "n1", "--rollback"); err != nil {
		t.Fatal(err)
	}
	if want := "n1 runs config " + second + " again; config " + first + " is its previous one now"; !strings.Contains(ta.stdout.String(), want) {
		t.Errorf("stdout lacks %q:\n%s", want, ta.stdout)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != second {
		t.Errorf("the node runs %s, want %s", v, second)
	}

	rewriteTrial(t, s, ended)
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp2.lan"))
	if err := run("apply-config", "n1"); err != nil {
		t.Errorf("a delivery past an ended trial: %v", err)
	}
}

// TestApplyConfigOnTrialPartlyFailed: a delivery on trial that failed after it wrote the slot
// leaves the trial open, and chalkctl says how to end it.
func TestApplyConfigOnTrialPartlyFailed(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	run := func(args ...string) error {
		ta.stdout.Reset()
		return ta.run(context.Background(), ta.args(args, addr))
	}
	if err := run("apply-config", "n1"); err != nil {
		t.Fatal(err)
	}
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp.lan"))
	second := ta.configVersion(t)
	r.setFail("chalkos-chrony-dhcp.service")
	err := run("apply-config", "n1", "--try", "1m")
	if err == nil || !strings.Contains(err.Error(), "config "+second+" stays on trial until ") || !strings.Contains(err.Error(), "chalkctl apply-config --confirm n1 keeps it; chalkctl apply-config --rollback n1 rolls it back now") {
		t.Fatalf("a delivery on trial that failed = %v\n%s", err, ta.stdout)
	}
	if strings.Contains(ta.stdout.String(), "waiting") {
		t.Errorf("chalkctl waited to confirm:\n%s", ta.stdout)
	}
	r.setFail("")
	if err := run("apply-config", "n1", "--rollback"); err != nil {
		t.Fatal(err)
	}
}

// The flags of a trial refuse what they do not go with before any node is reached.
func TestApplyConfigTrialUsage(t *testing.T) {
	ta := newTestApp(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--no-confirm"}, "--no-confirm goes with --try"},
		{[]string{"--try", "5s"}, "--try takes from 10s to 24h0m0s, not 5s"},
		{[]string{"--try", "25h"}, "--try takes from 10s to 24h0m0s, not 25h0m0s"},
		{[]string{"--try", "1m", "--staged"}, "--try applies config to the running image; --staged goes without it"},
		{[]string{"--try", "1m", "--diff"}, "--diff changes nothing, so it tries nothing; drop --try"},
		{[]string{"--try", "1m", "--kubernetes-share"}, "--try delivers config alone, which a rollback undoes"},
		{[]string{"--rollback", "--try", "1m"}, "--rollback goes without --try, --no-confirm, --ignore-unit and --confirm"},
		{[]string{"--rollback", "--no-confirm"}, "--rollback goes without --try, --no-confirm, --ignore-unit and --confirm"},
		{[]string{"--rollback", "--ignore-unit", "late.service"}, "--rollback goes without --try, --no-confirm, --ignore-unit and --confirm"},
		{[]string{"--ignore-unit", "late.service"}, "--ignore-unit goes with --try"},
		{[]string{"--confirm", "--ignore-unit", "late.service"}, "--confirm confirms the node's open trial"},
		{[]string{"--confirm", "--rollback"}, "--confirm confirms the node's open trial and takes no other apply-config flag but the node's"},
		{[]string{"--confirm", "--try", "1m"}, "--confirm confirms the node's open trial"},
	} {
		err := ta.run(context.Background(), ta.args(append([]string{"apply-config", "n1"}, tc.args...), "127.0.0.1:1"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestTrialLine(t *testing.T) {
	tr := &nodev1.ConfigTrial{Version: "v2", PreviousVersion: "v1", Deadline: timestamppb.New(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))}
	if got, want := trialLine(tr), "config v2 on trial until 2026-10-29T12:00:00Z: it rolls back to config v1 unless apply-config --confirm confirms it"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	tr.IgnoreUnits = []string{"late.service", "sync.timer"}
	if got := trialLine(tr); !strings.HasSuffix(got, " confirms it; its health check leaves out late.service, sync.timer") {
		t.Errorf("with ignored units: %q", got)
	}
	tr.Error = "output demo failed"
	if got := trialLine(tr); !strings.HasSuffix(got, "; the rollback at the deadline failed and is tried again each minute: output demo failed") {
		t.Errorf("with an error: %q", got)
	}
	tr.RollbackChanged = true
	if got := trialLine(tr); !strings.HasSuffix(got, "; the rollback at the deadline failed after it changed the node and is tried again each minute: output demo failed; apply-config --confirm no longer keeps the config, and apply-config --rollback finishes the rollback now") {
		t.Errorf("with a rollback that changed the node: %q", got)
	}
}

func TestFailedUnitsWarning(t *testing.T) {
	if got := failedUnitsWarning([]string{"late.service"}, []string{"late.service"}); got != "" {
		t.Errorf("all left out: %q", got)
	}
	want := "units failed already: a.service, c.service; the trial rolls back at its deadline unless they recover, the image ignores them or --ignore-unit leaves them out"
	if got := failedUnitsWarning([]string{"a.service", "b.service", "c.service"}, []string{"b.service"}); got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

// TestApplyConfigOnTrialIgnoresUnits warns of a unit that failed before a trial, unless
// --ignore-unit leaves it out; the units left out reach the node's health check, which passes
// without them, and show in status.
func TestApplyConfigOnTrialIgnoresUnits(t *testing.T) {
	ta := newTestApp(t)
	ta.pollInterval = 20 * time.Millisecond
	s, r := installedNode(t, ta, []byte(`{}`))
	r.failedUnits = "late.service loaded failed failed Late\n"
	s.Healthy = func(_ context.Context, ignore []string) error {
		if !slices.Contains(ignore, "late.service") {
			return errors.New("units failed: late.service")
		}
		return nil
	}
	addr := ta.startNode(t, s)
	run := func(args ...string) error {
		ta.stdout.Reset()
		ta.stderr.Reset()
		return ta.run(context.Background(), ta.args(args, addr))
	}
	warning := "chalkctl: warning: n1: units failed already: late.service; the trial rolls back at its deadline unless they recover, the image ignores them or --ignore-unit leaves them out"
	if err := run("apply-config", "n1"); err != nil {
		t.Fatal(err)
	}
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp.lan"))
	second := ta.configVersion(t)
	if err := run("apply-config", "n1", "--try", "1m", "--ignore-unit", "late.service", "--no-confirm"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ta.stderr.String(), "warning") {
		t.Errorf("a unit left out was warned of:\n%s", ta.stderr)
	}
	if err := run("status", "n1"); err != nil || !strings.Contains(ta.stdout.String(), "; its health check leaves out late.service") {
		t.Errorf("status = %v:\n%s", err, ta.stdout)
	}
	if err := run("apply-config", "n1", "--confirm"); err != nil || !strings.Contains(ta.stdout.String(), "n1 keeps config "+second+": the trial is confirmed") {
		t.Fatalf("--confirm = %v:\n%s", err, ta.stdout)
	}
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp2.lan"))
	if err := run("apply-config", "n1", "--try", "1m", "--no-confirm"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stderr.String(), warning) {
		t.Errorf("stderr lacks %q:\n%s", warning, ta.stderr)
	}
	// The trial's health check cannot pass: it ends with a rollback on request.
	if err := run("apply-config", "n1", "--rollback"); err != nil {
		t.Fatal(err)
	}
}
