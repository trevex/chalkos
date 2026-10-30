package chalkctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
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
	contains("n1: delivering config "+second+" on trial until ",
		"n1 runs config "+second+" on trial; it rolls back to config "+first+" by itself unless confirmed within 1m0s",
		"n1: waiting for a new connection and the node's health to confirm config "+second,
		"n1 keeps config "+second+": the trial is confirmed")
	// The confirmation came over a connection of its own, not the delivery's.
	applied, confirmed := ta.remotes("ApplyConfig"), ta.remotes("ConfirmConfig")
	if len(applied) != 2 || len(confirmed) == 0 || slices.Contains(applied, confirmed[len(confirmed)-1]) {
		t.Errorf("deliveries from %q, confirmations from %q", applied, confirmed)
	}
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

// holdAnswer has the node carry out each request for the procedure, then drop the connection
// without an answer, as a config that cuts chalkctl off does, or answer only after delay.
func holdAnswer(procedure string, delay time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/"+procedure) {
				next.ServeHTTP(w, r)
				return
			}
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, r.WithContext(context.WithoutCancel(r.Context())))
			if delay == 0 {
				panic(http.ErrAbortHandler)
			}
			time.Sleep(delay)
			maps.Copy(w.Header(), rec.Header())
			w.WriteHeader(rec.Code)
			w.Write(rec.Body.Bytes())
		})
	}
}

// refuse has the node refuse each request for the procedure with err.
func refuse(procedure string, err *connect.Error) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/"+procedure) {
				next.ServeHTTP(w, r)
				return
			}
			connect.NewErrorWriter().Write(w, r, err)
		})
	}
}

// trialNode serves n1, which runs the inventory's config, lets --try take a second, and has the
// inventory give n1 a second config. It returns the node, a run of chalkctl against it, and the
// versions of the first config and the second.
func trialNode(t *testing.T, ta *testApp) (s *chalkd.Server, run func(ctx context.Context, args ...string) error, first, second string) {
	t.Helper()
	m := minTrial
	t.Cleanup(func() { minTrial = m })
	minTrial = time.Second
	ta.pollInterval = 20 * time.Millisecond
	s, _ = installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	run = func(ctx context.Context, args ...string) error {
		ta.stdout.Reset()
		return ta.run(ctx, ta.args(args, addr))
	}
	if err := run(context.Background(), "apply-config", "n1"); err != nil {
		t.Fatal(err)
	}
	first = ta.configVersion(t)
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp.lan"))
	return s, run, first, ta.configVersion(t)
}

// TestApplyConfigOnTrialAnswerLost: a delivery on trial whose answer never comes, as when the
// config cuts chalkctl off, or comes only after the deadline, is not confirmed; chalkctl waits for
// the node to roll it back at the deadline, says so and fails.
func TestApplyConfigOnTrialAnswerLost(t *testing.T) {
	for name, delay := range map[string]time.Duration{"dropped": 0, "late": 2 * time.Second} {
		t.Run(name, func(t *testing.T) {
			ta := newTestApp(t)
			s, run, first, second := trialNode(t, ta)
			ta.setWrap(holdAnswer("ApplyConfig", delay))
			err := run(context.Background(), "apply-config", "n1", "--try", "1s")
			if err == nil || !strings.Contains(err.Error(), "n1: config "+second+" was not confirmed within its trial: the answer to its delivery did not come: ") {
				t.Fatalf("a delivery on trial whose answer was lost = %v\n%s", err, ta.stdout)
			}
			if strings.Contains(err.Error(), "failed_precondition") || strings.Contains(ta.stdout.String(), "waiting") {
				t.Errorf("chalkctl tried to confirm config %s: %v\n%s", second, err, ta.stdout)
			}
			for _, want := range []string{"n1: delivering config " + second + " on trial until ", "n1 rolled back to config " + first + " at the trial's deadline"} {
				if !strings.Contains(ta.stdout.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, ta.stdout)
				}
			}
			if v, _ := slot.Version(slotOf(t, s)); v != first {
				t.Errorf("the node runs %s, want %s", v, first)
			}
		})
	}
}

// TestConfirmAnswerLate: a confirmation the node carried out, whose answer came after chalkctl
// stopped waiting at the deadline, is found in the node's status and reported as confirmed.
func TestConfirmAnswerLate(t *testing.T) {
	ta := newTestApp(t)
	s, run, _, second := trialNode(t, ta)
	ta.setWrap(holdAnswer("ConfirmConfig", 2*time.Second))
	if err := run(context.Background(), "apply-config", "n1", "--try", "1s"); err != nil {
		t.Fatalf("%v\n%s", err, ta.stdout)
	}
	if want := "n1 keeps config " + second + ": the trial is confirmed"; !strings.Contains(ta.stdout.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, ta.stdout)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != second {
		t.Errorf("the node runs %s, want %s", v, second)
	}
}

// TestConfirmInterrupted: chalkctl stopped while it waits to confirm leaves the trial open, and
// says until when and how to end it.
func TestConfirmInterrupted(t *testing.T) {
	ta := newTestApp(t)
	_, run, _, second := trialNode(t, ta)
	// chalkctl is interrupted once it asks for the confirmation, which the node refuses meanwhile.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unhealthy := refuse("ConfirmConfig", connect.NewError(connect.CodeUnavailable, errors.New("the node is not healthy: units failed: demo.service")))
	ta.setWrap(func(next http.Handler) http.Handler {
		h := unhealthy(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/ConfirmConfig") {
				cancel()
			}
			h.ServeHTTP(w, r)
		})
	})
	err := run(ctx, "apply-config", "n1", "--try", "1m")
	if err == nil || !strings.Contains(err.Error(), "; n1 runs config "+second+" on trial until ") || !strings.Contains(err.Error(), "; chalkctl apply-config --confirm n1 keeps it; chalkctl apply-config --rollback n1 rolls it back now") {
		t.Errorf("an interrupted confirmation = %v\n%s", err, ta.stdout)
	}
}

// TestConfirmRefused: a confirmation the node refuses for good before the deadline ends the
// attempts, and chalkctl says when the node rolls back and how to end the trial before.
func TestConfirmRefused(t *testing.T) {
	ta := newTestApp(t)
	_, run, _, _ := trialNode(t, ta)
	ta.setWrap(refuse("ConfirmConfig", connect.NewError(connect.CodeFailedPrecondition, errors.New("the trial is of config x, not y"))))
	err := run(context.Background(), "apply-config", "n1", "--try", "1m")
	if err == nil || !strings.Contains(err.Error(), "the trial is of config x, not y; it rolls back at ") || !strings.Contains(err.Error(), " unless confirmed; chalkctl apply-config --confirm n1 keeps it; chalkctl apply-config --rollback n1 rolls it back now") {
		t.Errorf("a refused confirmation = %v\n%s", err, ta.stdout)
	}
}

// spoilTrial has the node carry out each request for the procedure after it made the record of
// its trial unreadable; with drop, it does so after the request and drops the connection without
// an answer.
func spoilTrial(s *chalkd.Server, procedure string, drop bool) func(http.Handler) http.Handler {
	spoil := func() { os.WriteFile(filepath.Join(s.Paths.StateDir, "chalkd", "trial"), []byte("{"), 0o600) }
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case !strings.HasSuffix(r.URL.Path, "/"+procedure):
				next.ServeHTTP(w, r)
			case drop:
				next.ServeHTTP(httptest.NewRecorder(), r.WithContext(context.WithoutCancel(r.Context())))
				spoil()
				panic(http.ErrAbortHandler)
			default:
				spoil()
				next.ServeHTTP(w, r)
			}
		})
	}
}

// TestUnreadableTrialAfterDelivery: a delivery on trial whose answer did not come, after which the
// node cannot read the record of its trial, is not reported as confirmed; chalkctl names the
// rollback that ends it.
func TestUnreadableTrialAfterDelivery(t *testing.T) {
	ta := newTestApp(t)
	s, run, _, second := trialNode(t, ta)
	ta.setWrap(spoilTrial(s, "ApplyConfig", true))
	err := run(context.Background(), "apply-config", "n1", "--try", "1m")
	if err == nil || !strings.Contains(err.Error(), "n1: config "+second+" was not confirmed within its trial: ") || !strings.Contains(err.Error(), "; the record of its trial cannot be read: parse the trial record: unexpected end of JSON input; chalkctl apply-config --rollback n1 restores the previous config") {
		t.Errorf("a delivery whose trial record cannot be read = %v\n%s", err, ta.stdout)
	}
	if strings.Contains(ta.stdout.String(), "the trial is confirmed") {
		t.Errorf("chalkctl reports the trial confirmed:\n%s", ta.stdout)
	}
}

// TestConfirmUnreadableTrial: a confirmation the node refuses because it cannot read the record of
// the trial names the rollback that ends it, and no deadline or --confirm.
func TestConfirmUnreadableTrial(t *testing.T) {
	ta := newTestApp(t)
	s, run, _, second := trialNode(t, ta)
	ta.setWrap(spoilTrial(s, "ConfirmConfig", false))
	err := run(context.Background(), "apply-config", "n1", "--try", "1m")
	if err == nil || !strings.Contains(err.Error(), "n1: config "+second+" was not confirmed within its trial: ") || !strings.Contains(err.Error(), "chalkctl apply-config --rollback n1 restores the previous config") {
		t.Fatalf("a confirmation of a trial whose record cannot be read = %v\n%s", err, ta.stdout)
	}
	if strings.Contains(err.Error(), "it rolls back at") || strings.Contains(err.Error(), "--confirm n1 keeps it") {
		t.Errorf("chalkctl names a deadline or --confirm: %v", err)
	}
}

// TestTrialRollbackWaitInterrupted: chalkctl stopped while it waits for the node to end a trial
// whose delivery did not answer leaves the trial open, and says until when and how to end it.
func TestTrialRollbackWaitInterrupted(t *testing.T) {
	ta := newTestApp(t)
	_, run, _, second := trialNode(t, ta)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lost := holdAnswer("ApplyConfig", 0)
	var delivered atomic.Bool
	ta.setWrap(func(next http.Handler) http.Handler {
		h := lost(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/ApplyConfig"):
				delivered.Store(true)
			case strings.HasSuffix(r.URL.Path, "/Status") && delivered.Load():
				cancel()
			}
			h.ServeHTTP(w, r)
		})
	})
	err := run(ctx, "apply-config", "n1", "--try", "1m")
	if err == nil || !strings.Contains(err.Error(), "the answer to its delivery did not come: ") || !strings.Contains(err.Error(), "; n1 may run config "+second+" on trial until ") || !strings.Contains(err.Error(), "; chalkctl apply-config --confirm n1 keeps it; chalkctl apply-config --rollback n1 rolls it back now") {
		t.Errorf("an interrupted wait for the rollback = %v\n%s", err, ta.stdout)
	}
}

// TestUnreadableTrialRecord: a trial whose record the node cannot read shows in status, holds back
// deliveries, and --rollback warns which config it delivers, which ends it.
func TestUnreadableTrialRecord(t *testing.T) {
	ta := newTestApp(t)
	s, run, first, _ := trialNode(t, ta)
	if err := run(context.Background(), "apply-config", "n1", "--try", "1m", "--no-confirm"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(s.Paths.StateDir, "chalkd", "trial"), "{")
	if err := run(context.Background(), "status", "n1"); err != nil || !strings.Contains(ta.stdout.String(), "\nthe record of a config's trial cannot be read: parse the trial record: unexpected end of JSON input; other changes are refused until apply-config --rollback restores the previous config\n") {
		t.Errorf("status = %v:\n%s", err, ta.stdout)
	}
	ta.writeNode(t, n1SystemDisk, n1Network, timeSync("ntp2.lan"))
	if err := run(context.Background(), "apply-config", "n1"); err == nil || !strings.Contains(err.Error(), "n1: the record of a config's trial cannot be read: parse the trial record: unexpected end of JSON input; other changes are refused until chalkctl apply-config --rollback n1 restores the previous config") {
		t.Errorf("a delivery past a record that cannot be read: %v", err)
	}
	if err := run(context.Background(), "apply-config", "n1", "--confirm"); err == nil || !strings.Contains(err.Error(), "n1: the record of a config's trial cannot be read: parse the trial record: unexpected end of JSON input; other changes are refused until chalkctl apply-config --rollback n1 restores the previous config") {
		t.Errorf("--confirm past a record that cannot be read: %v", err)
	}
	ta.stderr.Reset()
	if err := run(context.Background(), "apply-config", "n1", "--rollback"); err != nil {
		t.Fatal(err)
	}
	if want := "chalkctl: warning: n1: the record of a config's trial cannot be read: parse the trial record: unexpected end of JSON input; the rollback delivers config " + first + ", the node's previous config, which is the config on trial if a rollback at the trial's deadline replaced it already"; !strings.Contains(ta.stderr.String(), want) {
		t.Errorf("stderr lacks %q:\n%s", want, ta.stderr)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != first {
		t.Errorf("the node runs %s, want %s", v, first)
	}
	if err := run(context.Background(), "status", "n1"); err != nil || strings.Contains(ta.stdout.String(), "trial") {
		t.Errorf("status after the rollback = %v:\n%s", err, ta.stdout)
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
	if err := run("status", "n1"); err != nil || !strings.Contains(ta.stdout.String(), "; an attempt to roll the config back failed and is tried again each minute: output demo failed") {
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
	if err := run("status", "n1"); err != nil || !strings.Contains(ta.stdout.String(), "; an attempt to roll the config back failed after it changed the node and is tried again each minute: restart demo.service failed; apply-config --confirm no longer keeps the config, and apply-config --rollback finishes the rollback now") {
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
		{[]string{"--rollback", "--try", "1m"}, "--rollback goes without --try, --no-confirm and --ignore-unit"},
		{[]string{"--rollback", "--no-confirm"}, "--rollback goes without --try, --no-confirm and --ignore-unit"},
		{[]string{"--rollback", "--ignore-unit", "late.service"}, "--rollback goes without --try, --no-confirm and --ignore-unit"},
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
	if got := trialLine(tr); !strings.HasSuffix(got, "; an attempt to roll the config back failed and is tried again each minute: output demo failed") {
		t.Errorf("with an error: %q", got)
	}
	tr.RollbackChanged = true
	if got := trialLine(tr); !strings.HasSuffix(got, "; an attempt to roll the config back failed after it changed the node and is tried again each minute: output demo failed; apply-config --confirm no longer keeps the config, and apply-config --rollback finishes the rollback now") {
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

// TestTrialOpen refuses a change to a node with an open trial or a trial record it cannot read,
// but not to one that runs the config from before its trial again, which the node finishes.
func TestTrialOpen(t *testing.T) {
	tr := &nodev1.ConfigTrial{Version: "v2", PreviousVersion: "v1", Deadline: timestamppb.New(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))}
	for _, tc := range []struct {
		st   *nodev1.StatusResponse
		want string
	}{
		{&nodev1.StatusResponse{ConfigVersion: "v1"}, ""},
		{&nodev1.StatusResponse{ConfigVersion: "v2", Trial: tr}, "n1 runs config v2 on trial until 2026-10-29T12:00:00Z; confirm it with chalkctl apply-config --confirm n1, or roll it back with chalkctl apply-config --rollback n1, first"},
		{&nodev1.StatusResponse{ConfigVersion: "v1", Trial: tr}, ""},
		{&nodev1.StatusResponse{ConfigVersion: "v2", TrialError: "parse the trial record: unexpected EOF"}, "n1: the record of a config's trial cannot be read: parse the trial record: unexpected EOF; other changes are refused until chalkctl apply-config --rollback n1 restores the previous config"},
	} {
		err := trialOpen("n1", tc.st)
		if got := fmt.Sprint(err); tc.want == "" && err != nil || tc.want != "" && got != tc.want {
			t.Errorf("%v: %v, want %q", tc.st, err, tc.want)
		}
	}
}
