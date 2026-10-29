package chalkd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// nextConfig is baseConfig with another name in the Demo, which restarts demo.service.
var nextConfig = strings.Replace(baseConfig, "name: first", "name: second", 1)

// recordTrial delivers next for good and records it as on trial for an hour, as a trial's
// delivery leaves the node: the slot runs next and keeps baseConfig as its previous config.
func recordTrial(t *testing.T, s *Server, next string) trialRecord {
	t.Helper()
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	r := trialRecord{
		Slot:     filepath.Base(slotOf(t, s)),
		Version:  hashOf(t, next),
		Previous: hashOf(t, baseConfig),
		Deadline: time.Now().Add(time.Hour).Truncate(time.Second).UTC(),
	}
	if err := writeTrial(s.Paths.StateDir, r); err != nil {
		t.Fatal(err)
	}
	return r
}

// trialOpenOn reports whether STATE holds a trial record, read under s.mu so that a deadline's
// rollback has finished.
func trialOpenOn(t *testing.T, s *Server) bool {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, open, err := readTrial(s.Paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	return open
}

func sendConfirm(s *Server, version string) error {
	_, err := s.ConfirmConfig(context.Background(), connect.NewRequest(&nodev1.ConfirmConfigRequest{Version: version}))
	return err
}

func statusOf(t *testing.T, s *Server) *nodev1.StatusResponse {
	t.Helper()
	resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg
}

// TestConfirmConfig ends a trial once the node is healthy, with the units the trial ignores left
// out of the check: Status shows the trial until then, an unhealthy node and another version are
// refused, and a confirmation sent again succeeds while the node runs the version.
func TestConfirmConfig(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	r.IgnoreUnits = []string{"late.service"}
	if err := writeTrial(s.Paths.StateDir, r); err != nil {
		t.Fatal(err)
	}
	if tr := statusOf(t, s).Trial; tr == nil || tr.Version != r.Version || tr.PreviousVersion != r.Previous || !tr.Deadline.AsTime().Equal(r.Deadline) || tr.Error != "" || !slices.Equal(tr.IgnoreUnits, r.IgnoreUnits) {
		t.Errorf("status trial = %+v, want %+v", tr, r)
	}
	if err := sendConfirm(s, ""); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "the request names no config version to confirm") {
		t.Errorf("without a version: %v", err)
	}
	var ignored []string
	s.Healthy = func(_ context.Context, ignore []string) error {
		ignored = ignore
		return errors.New("units failed: demo.service")
	}
	if err := sendConfirm(s, r.Version); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "the node is not healthy: units failed: demo.service") {
		t.Errorf("unhealthy: %v", err)
	}
	if !slices.Equal(ignored, r.IgnoreUnits) {
		t.Errorf("the health check left out %q, want the trial's %q", ignored, r.IgnoreUnits)
	}
	if !trialOpenOn(t, s) {
		t.Fatal("an unhealthy node ended the trial")
	}
	s.Healthy = nil
	if err := sendConfirm(s, r.Previous); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the trial is of config "+r.Version+", not "+r.Previous) {
		t.Errorf("another version: %v", err)
	}
	if err := sendConfirm(s, r.Version); err != nil {
		t.Fatal(err)
	}
	if trialOpenOn(t, s) || statusOf(t, s).Trial != nil {
		t.Error("the confirmed trial is still open")
	}
	if v, _ := slot.Version(slotOf(t, s)); v != r.Version {
		t.Errorf("the node runs %s after the confirmation, want %s", v, r.Version)
	}
	if err := sendConfirm(s, r.Version); err != nil {
		t.Errorf("a confirmation sent again: %v", err)
	}
	if err := sendConfirm(s, r.Previous); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "no trial is open, and the node runs config "+r.Version+", not "+r.Previous) {
		t.Errorf("no trial: %v", err)
	}
}

// TestTrialState classifies a trial by its slot: applied while the slot runs the version on trial
// and keeps the one before; ended while it runs the one before, also after an interrupted write
// left both links on it; lost otherwise, as while it runs the version on trial but keeps another
// previous config.
func TestTrialState(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	dir := slotOf(t, s)
	check := func(want trialState) {
		t.Helper()
		if _, got, err := r.state(s.Loader.Slots); err != nil || got != want {
			t.Errorf("state = %v, %v; want %v", got, err, want)
		}
	}
	readlink := func(link string) string {
		t.Helper()
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	relink := func(link, target string) {
		t.Helper()
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	next, base := readlink(dir), readlink(slot.Previous(dir))
	check(trialApplied)
	relink(slot.Previous(dir), next)
	check(trialLost)
	relink(slot.Previous(dir), base)
	if err := slot.RestorePrevious(dir); err != nil {
		t.Fatal(err)
	}
	check(trialEnded)
	relink(slot.Previous(dir), base)
	check(trialEnded)
	r.Version, r.Previous = "v8", "v9"
	check(trialLost)
}

// TestChangesWaitForATrial refuses every delivery, an upgrade and an image rollback while a config
// is on trial, names how to end it, and changes nothing.
func TestChangesWaitForATrial(t *testing.T) {
	s, r := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	c := newCreds(t)
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleAdmin])
	r.Calls = nil
	ctx := context.Background()
	apply := func(m *nodev1.ApplyConfigRequest) func() error {
		return func() error {
			_, err := s.ApplyConfig(ctx, connect.NewRequest(m))
			return err
		}
	}
	for _, tc := range []struct {
		name, what string
		call       func() error
	}{
		{"config", "another delivery", apply(&nodev1.ApplyConfigRequest{Config: baseConfig})},
		{"staged config", "another delivery", apply(&nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true})},
		{"OS CAs", "another delivery", apply(&nodev1.ApplyConfigRequest{Trust: []byte("x")})},
		{"image rollback", "an image rollback", func() error {
			_, err := s.RollbackImage(ctx, connect.NewRequest(&nodev1.RollbackImageRequest{RootHash: bytes.Repeat([]byte{1}, 32)}))
			return err
		}},
		{"upgrade", "an upgrade", func() error {
			stream := conn.Upgrade(ctx)
			stream.Send(&nodev1.UpgradeRequest{})
			_, err := stream.CloseAndReceive()
			return err
		}},
	} {
		want := "config " + rec.Version + " is on trial until " + rec.Deadline.Format(time.RFC3339) + ": confirm it with chalkctl apply-config --confirm n1, or roll it back with chalkctl apply-config --rollback n1, before " + tc.what
		if err := tc.call(); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", tc.name, err, want)
		}
	}
	if len(r.Calls) != 0 {
		t.Errorf("the refusals ran %v", r.Calls)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Version {
		t.Errorf("the node runs %s, want the config on trial", v)
	}
}

// TestRollbackConfigEndsATrial rolls the config on trial back on request and ends the trial, so
// the node takes deliveries again.
func TestRollbackConfigEndsATrial(t *testing.T) {
	s, _ := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	if _, err := sendRollback(s, rec.Previous); err != nil {
		t.Fatal(err)
	}
	if trialOpenOn(t, s) {
		t.Error("the trial is open after the rollback")
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Previous {
		t.Errorf("the node runs %s, want %s", v, rec.Previous)
	}
	if _, err := sendConfig(s, nextConfig); err != nil {
		t.Errorf("a delivery after the trial ended: %v", err)
	}
}

// TestConfirmConfigChecksTheTrialFirst refuses another version, and a node without a trial, before
// the health check: an unhealthy node answers FAILED_PRECONDITION, not UNAVAILABLE.
func TestConfirmConfigChecksTheTrialFirst(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	checked := false
	s.Healthy = func(context.Context, []string) error {
		checked = true
		return errors.New("units failed: demo.service")
	}
	if err := sendConfirm(s, r.Previous); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the trial is of config "+r.Version+", not "+r.Previous) {
		t.Errorf("another version: %v", err)
	}
	if err := removeTrial(s.Paths.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := sendConfirm(s, r.Previous); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "no trial is open, and the node runs config "+r.Version+", not "+r.Previous) {
		t.Errorf("no trial: %v", err)
	}
	if checked {
		t.Error("the node's health was checked for a confirmation that cannot succeed")
	}
}

// TestConfirmConfigTrialChanged refuses to confirm a trial whose record changed while the node's
// health was checked, so the client asks again.
func TestConfirmConfigTrialChanged(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	later := r
	later.Deadline = r.Deadline.Add(time.Hour)
	s.Healthy = func(context.Context, []string) error {
		return writeTrial(s.Paths.StateDir, later)
	}
	if err := sendConfirm(s, r.Version); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "the trial changed while the node's health was checked") {
		t.Errorf("a changed trial: %v", err)
	}
	if got, open, err := readTrial(s.Paths.StateDir); err != nil || !open || !got.Deadline.Equal(later.Deadline) {
		t.Errorf("the record is %+v, open %v, %v; want the changed trial open", got, open, err)
	}
}

// TestConfirmConfigNotApplied refuses a trial whose slot no longer runs the config on trial: one
// whose slot keeps another previous config, and one rolled back.
func TestConfirmConfigNotApplied(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	lost := r
	lost.Previous = "v0"
	if err := writeTrial(s.Paths.StateDir, lost); err != nil {
		t.Fatal(err)
	}
	if err := sendConfirm(s, r.Version); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the node does not run config "+r.Version+" on trial with config v0 as its previous config") {
		t.Errorf("a trial lost: %v", err)
	}
	if err := writeTrial(s.Paths.StateDir, r); err != nil {
		t.Fatal(err)
	}
	if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
		t.Fatal(err)
	}
	if err := sendConfirm(s, r.Version); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the node does not run config "+r.Version+" on trial") {
		t.Errorf("a trial rolled back: %v", err)
	}
	if !trialOpenOn(t, s) {
		t.Error("the refusal ended the trial")
	}
}

// TestConfirmConfigAfterTheDeadline refuses a trial past its deadline without checking the node's
// health, unless the rollback at the deadline failed: then the confirmation checks the node and
// ends the trial and its retries.
func TestConfirmConfigAfterTheDeadline(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	r.Deadline = time.Now().Add(-time.Minute).Truncate(time.Second).UTC()
	if err := writeTrial(s.Paths.StateDir, r); err != nil {
		t.Fatal(err)
	}
	checks := 0
	s.Healthy = func(context.Context, []string) error {
		checks++
		return nil
	}
	if err := sendConfirm(s, r.Version); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the trial of config "+r.Version+" ended at its deadline, "+r.Deadline.Format(time.RFC3339)) {
		t.Errorf("past the deadline: %v", err)
	}
	if checks != 0 {
		t.Error("the node's health was checked for a trial past its deadline")
	}
	if !trialOpenOn(t, s) {
		t.Fatal("the refusal ended the trial")
	}
	r.Error, r.Attempts = "an output failed", 1
	if err := writeTrial(s.Paths.StateDir, r); err != nil {
		t.Fatal(err)
	}
	if err := sendConfirm(s, r.Version); err != nil {
		t.Fatalf("past the deadline with a failed rollback: %v", err)
	}
	if checks != 1 {
		t.Errorf("the node's health was checked %d times, want once", checks)
	}
	if trialOpenOn(t, s) {
		t.Error("the confirmed trial is still open")
	}
}

// TestConfirmConfigCorruptRecord fails a confirmation on a trial record that cannot be read, and
// Status leaves the trial out and says why.
func TestConfirmConfigCorruptRecord(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	if err := os.WriteFile(filepath.Join(s.Paths.StateDir, trialFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := sendConfirm(s, r.Version)
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "read the record of the config's trial: ") ||
		!strings.Contains(err.Error(), "; other changes are refused until chalkctl apply-config --rollback ") ||
		!strings.Contains(err.Error(), " restores the previous config, which removes the record") {
		t.Errorf("a corrupt record: %v", err)
	}
	st := statusOf(t, s)
	if st.Trial != nil {
		t.Errorf("status trial = %+v, want none", st.Trial)
	}
	if !strings.Contains(st.TrialError, "unexpected end of JSON input") {
		t.Errorf("status trial error = %q, want why the record cannot be read", st.TrialError)
	}
}

// TestReadTrialRefusesNoDeadline refuses a record without a deadline.
func TestReadTrialRefusesNoDeadline(t *testing.T) {
	dir := t.TempDir()
	if err := writeTrial(dir, trialRecord{Slot: "ab", Version: "v2", Previous: "v1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readTrial(dir); err == nil || !strings.Contains(err.Error(), "names no deadline") {
		t.Errorf("readTrial = %v, want the missing deadline", err)
	}
}

// TestRollbackConfigFailingKeepsTheTrial keeps the trial open when the rollback on request fails,
// so the config on trial still waits for its confirmation or its deadline.
func TestRollbackConfigFailingKeepsTheTrial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s, _ := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	slots := filepath.Dir(slotOf(t, s))
	// The directory of slots takes no new slot directory.
	if err := os.Chmod(slots, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slots, 0o755) })
	if _, err := sendRollback(s, rec.Previous); err == nil || !strings.Contains(err.Error(), "store the config") {
		t.Fatalf("err = %v, want the slot's failure", err)
	}
	if !trialOpenOn(t, s) {
		t.Error("a failed rollback ended the trial")
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Version {
		t.Errorf("the node runs %s, want the config on trial", v)
	}
}

// TestRollbackConfigKeepsATrialItCannotRemove rolls the config on trial back even when the trial's
// record cannot be removed, warns of it, and keeps the record and the deadline. The next delivery
// finds the trial ended and removes the record.
func TestRollbackConfigKeepsATrialItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s, _ := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	timer := time.AfterFunc(time.Hour, func() {})
	defer timer.Stop()
	s.trialTimer = timer
	dir := filepath.Dir(filepath.Join(s.Paths.StateDir, trialFile))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	resp, err := sendRollback(s, rec.Previous)
	if err != nil {
		t.Fatal(err)
	}
	want := "the config was rolled back, but the record of its trial could not be removed for good: "
	if !slices.ContainsFunc(resp.Warnings, func(w string) bool { return strings.HasPrefix(w, want) }) {
		t.Errorf("warnings = %q, want one starting %q", resp.Warnings, want)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Previous {
		t.Errorf("the node runs %s, want %s", v, rec.Previous)
	}
	if !trialOpenOn(t, s) {
		t.Error("the record is gone")
	}
	s.mu.Lock()
	armed := s.trialTimer == timer
	s.mu.Unlock()
	if !armed {
		t.Error("the deadline was stopped although the record stays")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := sendConfig(s, nextConfig); err != nil {
		t.Fatalf("a delivery after the rollback: %v", err)
	}
	if trialOpenOn(t, s) {
		t.Error("the delivery kept the ended trial's record")
	}
	s.mu.Lock()
	stopped := s.trialTimer == nil
	s.mu.Unlock()
	if !stopped {
		t.Error("the delivery kept the ended trial's deadline")
	}
}

// TestChangesPassAnEndedTrial lets every change through a trial record whose slot runs the config
// from before the trial already, as after a rollback whose record a crash or a failed removal
// kept, and removes the record.
func TestChangesPassAnEndedTrial(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func(s *Server, conn *client.Conn) error
		want string
	}{
		{"delivery", func(s *Server, _ *client.Conn) error {
			_, err := sendConfig(s, nextConfig)
			return err
		}, ""},
		{"image rollback", func(s *Server, _ *client.Conn) error {
			_, err := s.RollbackImage(ctx, connect.NewRequest(&nodev1.RollbackImageRequest{RootHash: bytes.Repeat([]byte{1}, 32)}))
			return err
		}, "STATE holds no config for the image"},
		{"upgrade", func(_ *Server, conn *client.Conn) error {
			stream := conn.Upgrade(ctx)
			stream.Send(&nodev1.UpgradeRequest{})
			_, err := stream.CloseAndReceive()
			return err
		}, "the first upgrade message must be the header"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := configServer(t)
			recordTrial(t, s, nextConfig)
			if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
				t.Fatal(err)
			}
			c := newCreds(t)
			conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleAdmin])
			err := tc.call(s, conn)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if trialOpenOn(t, s) {
				t.Error("the ended trial's record stays")
			}
		})
	}
}

// lostTrial records a trial whose slot runs neither its config nor the one from before it.
func lostTrial(t *testing.T, s *Server) trialRecord {
	t.Helper()
	rec := recordTrial(t, s, nextConfig)
	rec.Version, rec.Previous = "v8", "v9"
	if err := writeTrial(s.Paths.StateDir, rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// TestChangesDropALostTrial lets a change pass a trial whose slot runs neither its config nor the
// one from before it, and removes its record, as its deadline would.
func TestChangesDropALostTrial(t *testing.T) {
	s, _ := configServer(t)
	lostTrial(t, s)
	if _, err := sendConfig(s, nextConfig); err != nil {
		t.Errorf("a delivery past a lost trial: %v", err)
	}
	if trialOpenOn(t, s) {
		t.Error("the lost trial's record stays")
	}
}

// TestLostTrialKeepsARecordItCannotRemove refuses a change with INTERNAL when the record of a lost
// trial cannot be removed, and settling the trial arms the deadline to try again.
func TestLostTrialKeepsARecordItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s := trialServer(t)
	s.trialRetry = time.Hour
	rec := lostTrial(t, s)
	dir := filepath.Dir(filepath.Join(s.Paths.StateDir, trialFile))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := sendConfig(s, nextConfig); connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "remove the record of the lost trial of config v8") {
		t.Errorf("err = %v, want the record that stays", err)
	}
	s.upgrading.Lock()
	s.mu.Lock()
	_, _ = s.settleTrial(rec)
	armed := s.trialTimer != nil
	s.mu.Unlock()
	s.upgrading.Unlock()
	if !trialOpenOn(t, s) || !armed {
		t.Errorf("the record stays %v and the deadline is armed %v, want both", trialOpenOn(t, s), armed)
	}
}

// TestChangesStopAtAnEndedTrialTheyCannotRemove refuses a change with INTERNAL when the record of
// a trial that ended cannot be removed, and keeps the record.
func TestChangesStopAtAnEndedTrialTheyCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s, _ := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(filepath.Join(s.Paths.StateDir, trialFile))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	_, err := sendConfig(s, nextConfig)
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "remove the record of the ended trial of config "+rec.Version) {
		t.Errorf("err = %v, want the record that stays", err)
	}
	if !trialOpenOn(t, s) {
		t.Error("the record is gone")
	}
}

// TestConfirmConfigDuringARollbackAttempt refuses with UNAVAILABLE a confirmation during whose
// health check the deadline tried to roll the trial back again, failing as before or otherwise,
// so the client asks again.
func TestConfirmConfigDuringARollbackAttempt(t *testing.T) {
	for name, attempt := range map[string]func(*trialRecord){
		"the same failure": func(r *trialRecord) { r.Attempts++ },
		"another failure":  func(r *trialRecord) { r.Attempts, r.Error = r.Attempts+1, "another output failed" },
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := configServer(t)
			r := recordTrial(t, s, nextConfig)
			r.Error, r.Attempts = "an output failed", 1
			if err := writeTrial(s.Paths.StateDir, r); err != nil {
				t.Fatal(err)
			}
			s.Healthy = func(context.Context, []string) error {
				again := r
				attempt(&again)
				return writeTrial(s.Paths.StateDir, again)
			}
			if err := sendConfirm(s, r.Version); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "the trial changed while the node's health was checked") {
				t.Errorf("err = %v, want the changed trial", err)
			}
			if !trialOpenOn(t, s) {
				t.Error("the refusal ended the trial")
			}
		})
	}
}

// waitForLock waits until a goroutine whose stack names fn blocks on a mutex. It reads the Go
// runtime's goroutine dump, where a goroutine that waits for a sync.Mutex shows
// sync.runtime_SemacquireMutex; a runtime that names the wait otherwise makes it time out.
func waitForLock(t *testing.T, fn string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	eventually(t, fn+" waiting for a lock", func() bool {
		for _, g := range strings.Split(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
			if strings.Contains(g, fn) && strings.Contains(g, "SemacquireMutex") {
				return true
			}
		}
		return false
	})
}

// TestConfirmConfigTrialRecordedMeanwhile refuses with UNAVAILABLE a confirmation that found no
// trial before it took the lock and finds one of its version under it: the node's health was not
// checked for that trial.
func TestConfirmConfigTrialRecordedMeanwhile(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	if err := removeTrial(s.Paths.StateDir); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- sendConfirm(s, r.Version) }()
	waitForLock(t, "(*Server).ConfirmConfig")
	err := writeTrial(s.Paths.StateDir, r)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "the trial changed while the node's health was checked") {
		t.Errorf("err = %v, want the changed trial", err)
	}
	if !trialOpenOn(t, s) {
		t.Error("the refusal ended the trial")
	}
}

// expire runs the deadline of the trial r as the timer armed last would.
func expire(s *Server, r trialRecord) {
	s.mu.Lock()
	armed := s.trialArmed
	s.mu.Unlock()
	s.expireTrial(r.Version, r.Deadline, armed)
}

func sendTrial(s *Server, text string, d time.Duration) (*nodev1.ApplyConfigResponse, error) {
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: text, Try: durationpb.New(d)}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// trialServer is configServer whose trial deadlines stop with the test.
func trialServer(t *testing.T) *Server {
	t.Helper()
	s, _ := configServer(t)
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.trialTimer != nil {
			s.trialTimer.Stop()
		}
	})
	return s
}

// TestApplyConfigOnTrial delivers the config through the pipeline as any delivery, records the
// trial with the config from before it and the units its health check leaves out, and arms its
// deadline.
func TestApplyConfigOnTrial(t *testing.T) {
	s := trialServer(t)
	before := time.Now()
	ignore := []string{"late.service", "sync@a.timer"}
	m, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: nextConfig, Try: durationpb.New(time.Hour), IgnoreUnits: ignore}))
	if err != nil {
		t.Fatal(err)
	}
	resp := m.Msg
	if len(resp.RestartedUnits) != 1 || resp.RestartedUnits[0] != "demo.service" {
		t.Errorf("restarted %v, want demo.service", resp.RestartedUnits)
	}
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil || !open {
		t.Fatalf("no trial recorded: %v", err)
	}
	if r.Slot != filepath.Base(slotOf(t, s)) || r.Version != hashOf(t, nextConfig) || r.Previous != hashOf(t, baseConfig) || r.Deadline.Before(before.Add(time.Hour)) || r.Deadline.After(time.Now().Add(time.Hour)) || !slices.Equal(r.IgnoreUnits, ignore) || r.Error != "" || r.Attempts != 0 || r.Changed {
		t.Errorf("record %+v", r)
	}
	s.mu.Lock()
	armed := s.trialTimer != nil
	s.mu.Unlock()
	if !armed {
		t.Error("the deadline is not armed")
	}
}

// TestTrialRollsBackAtItsDeadline rolls the config back through the pipeline once the deadline
// passed unconfirmed: demo.service restarts, the node runs the config from before the trial and
// keeps the one on trial as its previous config, and the trial ends.
func TestTrialRollsBackAtItsDeadline(t *testing.T) {
	s := trialServer(t)
	if _, err := sendTrial(s, nextConfig, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the rollback", func() bool { return !trialOpenOn(t, s) })
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
		t.Errorf("the node runs %s, want baseConfig's", v)
	}
	if v, _ := slot.Version(slot.Previous(slotOf(t, s))); v != hashOf(t, nextConfig) {
		t.Errorf("the previous config is %s, want the one on trial", v)
	}
	if got := readFile(t, filepath.Join(s.Loader.RunDir, "credentials", "demo", "demo.token")); got != "s3cret" {
		t.Errorf("demo's credential = %q", got)
	}
}

// TestTrialRefusals refuses a trial of anything a rollback cannot undo, without config to roll
// back to, or of the config the node runs, and records no trial.
func TestTrialRefusals(t *testing.T) {
	s := trialServer(t)
	s.Run.(*tooltest.Runner).Rules = volumeRules(s)
	try := durationpb.New(time.Minute)
	for _, tc := range []struct {
		name string
		req  *nodev1.ApplyConfigRequest
		code connect.Code
		want string
	}{
		{"staged", &nodev1.ApplyConfigRequest{Config: nextConfig, Staged: true, Try: try}, connect.CodeInvalidArgument, "a trial delivers config to the running image"},
		{"no config", &nodev1.ApplyConfigRequest{Trust: []byte("x"), Try: try}, connect.CodeInvalidArgument, "a trial delivers config to the running image"},
		{"OS CAs", &nodev1.ApplyConfigRequest{Config: nextConfig, Trust: []byte("x"), Try: try}, connect.CodeInvalidArgument, "a trial delivers config alone"},
		{"payload", &nodev1.ApplyConfigRequest{Config: nextConfig, Layers: []*anypb.Any{{TypeUrl: "type.googleapis.com/x"}}, Try: try}, connect.CodeInvalidArgument, "a trial delivers config alone"},
		{"zero", &nodev1.ApplyConfigRequest{Config: nextConfig, Try: durationpb.New(0)}, connect.CodeInvalidArgument, "a trial lasts more than 0s and at most 24h0m0s, not 0s"},
		{"a day and more", &nodev1.ApplyConfigRequest{Config: nextConfig, Try: durationpb.New(25 * time.Hour)}, connect.CodeInvalidArgument, "not 25h0m0s"},
		{"ignored units without a trial", &nodev1.ApplyConfigRequest{Config: nextConfig, IgnoreUnits: []string{"late.service"}}, connect.CodeInvalidArgument, "ignore_units goes with try"},
		{"no unit name", &nodev1.ApplyConfigRequest{Config: nextConfig, Try: try, IgnoreUnits: []string{"late.service", "../late"}}, connect.CodeInvalidArgument, `"../late" is no unit name, such as demo.service`},
		{"a unit without a type", &nodev1.ApplyConfigRequest{Config: nextConfig, Try: try, IgnoreUnits: []string{"late"}}, connect.CodeInvalidArgument, `"late" is no unit name`},
		{"the running config", &nodev1.ApplyConfigRequest{Config: baseConfig, Try: try}, connect.CodeFailedPrecondition, "the node runs config " + hashOf(t, baseConfig) + " already; a trial needs a config that changes it"},
		{"a volume", &nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), FallbackSecret: secret, Try: try}, connect.CodeFailedPrecondition, "the config changes the node's storage, volume extra: "},
	} {
		_, err := s.ApplyConfig(context.Background(), connect.NewRequest(tc.req))
		if connect.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %v %q", tc.name, err, tc.code, tc.want)
		}
	}
	if trialOpenOn(t, s) {
		t.Error("a refused trial was recorded")
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
		t.Errorf("the node runs %s after the refusals", v)
	}
}

// TestTrialExpiryAfterTheTrialEnded finishes the trial of a node that runs the config from before
// it already: after a rollback on request whose record a crash kept, and after an interrupted
// write left both links on that config. Delivering that config again restarts nothing, and the
// record goes. A record of a later trial is
// not this deadline's and stays, whether of another version or of the same one with another
// deadline.
func TestTrialExpiryAfterTheTrialEnded(t *testing.T) {
	s := trialServer(t)
	r := s.Run.(*tooltest.Runner)
	rec := recordTrial(t, s, nextConfig)
	if _, err := sendRollback(s, rec.Previous); err != nil {
		t.Fatal(err)
	}
	dir := slotOf(t, s)
	check := func(name string) {
		t.Helper()
		if err := writeTrial(s.Paths.StateDir, rec); err != nil {
			t.Fatal(err)
		}
		r.Calls = nil
		expire(s, rec)
		if trialOpenOn(t, s) {
			t.Errorf("%s: the record stays", name)
		}
		if len(r.Calls) != 0 {
			t.Errorf("%s: ran %v", name, r.Calls)
		}
		if v, _ := slot.Version(dir); v != rec.Previous {
			t.Errorf("%s: the node runs %s, want %s", name, v, rec.Previous)
		}
	}
	check("after a rollback on request")
	held, err := os.Readlink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(slot.Previous(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(held, slot.Previous(dir)); err != nil {
		t.Fatal(err)
	}
	check("after an interrupted write")
	later := rec
	later.Version = "later"
	if err := writeTrial(s.Paths.StateDir, later); err != nil {
		t.Fatal(err)
	}
	expire(s, rec)
	if !trialOpenOn(t, s) {
		t.Error("an earlier deadline ended a later trial")
	}
}

// TestTrialExpiryOfAnEarlierTrial leaves a trial alone whose deadline is not the one that
// expired, although it is of the same version: the deadline of an earlier trial of that version
// does not roll it back.
func TestTrialExpiryOfAnEarlierTrial(t *testing.T) {
	s := trialServer(t)
	rec := recordTrial(t, s, nextConfig)
	early := rec
	early.Deadline = rec.Deadline.Add(-time.Hour)
	expire(s, early)
	if !trialOpenOn(t, s) {
		t.Error("an earlier deadline ended the trial")
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Version {
		t.Errorf("the node runs %s, want the config on trial", v)
	}
}

// TestTrialExpiryRetries keeps the trial open when its rollback fails, with the failure and the
// attempts in the record and the failure in Status, and rolls back once the rollback goes through.
// A rollback refused before it changed the node leaves the trial to a confirmation.
func TestTrialExpiryRetries(t *testing.T) {
	s := trialServer(t)
	s.trialRetry = time.Hour
	if _, err := sendTrial(s, nextConfig, time.Hour); err != nil {
		t.Fatal(err)
	}
	outputsWith := func(demo string) map[string]outputs.Definition {
		return map[string]outputs.Definition{"demo": demoOutput(t, s, demo, ""), "networkd": networkdOutput(t, s), "chrony": chronyOutput(t, s)}
	}
	writeOutputs(t, s, outputsWith("exit 3"))
	first, _, err := readTrial(s.Paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	expire(s, first)
	expire(s, first)
	rec, open, err := readTrial(s.Paths.StateDir)
	if err != nil || !open || rec.Error == "" || rec.Attempts != 2 || rec.Changed {
		t.Fatalf("after two failed rollbacks the record is %+v, open %v, %v; want it open with the failure and two attempts", rec, open, err)
	}
	if tr := statusOf(t, s).Trial; tr == nil || tr.Error != rec.Error {
		t.Errorf("status trial = %+v, want the failure", tr)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, nextConfig) {
		t.Errorf("the node runs %s after the failed rollback", v)
	}
	writeOutputs(t, s, outputsWith(`cat > "$OUT/demo.json"`))
	expire(s, rec)
	if trialOpenOn(t, s) {
		t.Error("the trial is open after the rollback went through")
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
		t.Errorf("the node runs %s, want baseConfig's", v)
	}
}

// TestTrialExpiryFailingPartWay records that a rollback at the deadline failed once it changed the
// node, and a confirmation is then refused: a rollback on request finishes the trial's.
func TestTrialExpiryFailingPartWay(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s := trialServer(t)
	s.trialRetry = time.Hour
	if _, err := sendTrial(s, nextConfig, time.Hour); err != nil {
		t.Fatal(err)
	}
	first, _, err := readTrial(s.Paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	slots := filepath.Dir(slotOf(t, s))
	// The directory of slots takes no new slot directory.
	if err := os.Chmod(slots, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slots, 0o755) })
	expire(s, first)
	rec, open, err := readTrial(s.Paths.StateDir)
	if err != nil || !open || !strings.Contains(rec.Error, "store the config") || rec.Attempts != 1 || !rec.Changed {
		t.Fatalf("after a rollback that failed part way the record is %+v, open %v, %v; want it open and changed", rec, open, err)
	}
	if err := os.Chmod(slots, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sendConfirm(s, rec.Version); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "chalkctl apply-config --rollback n1") {
		t.Errorf("a confirmation: %v, want the rollback on request", err)
	}
	if _, err := sendRollback(s, rec.Previous); err != nil {
		t.Fatal(err)
	}
	if trialOpenOn(t, s) {
		t.Error("the trial is open after the rollback on request")
	}
}

// TestResumeTrial arms a recorded trial as chalkd starts: one past its deadline rolls back at
// once; a record a crash left before the slot was written goes once the running config is
// delivered again. A record that cannot be read stays, and every change is refused until a
// rollback removes it.
func TestResumeTrial(t *testing.T) {
	s := trialServer(t)
	rec := recordTrial(t, s, nextConfig)
	rec.Deadline = time.Now().Add(-time.Minute)
	if err := writeTrial(s.Paths.StateDir, rec); err != nil {
		t.Fatal(err)
	}
	s.ResumeTrial(context.Background())
	eventually(t, "the rollback", func() bool { return !trialOpenOn(t, s) })
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Previous {
		t.Errorf("the node runs %s, want %s", v, rec.Previous)
	}
	pending := rec
	pending.Version, pending.Deadline = "pending", time.Now().Add(time.Hour)
	if err := writeTrial(s.Paths.StateDir, pending); err != nil {
		t.Fatal(err)
	}
	s.ResumeTrial(context.Background())
	s.mu.Lock()
	armed := s.trialTimer != nil
	s.mu.Unlock()
	if trialOpenOn(t, s) || armed {
		t.Error("a trial whose slot was never written stays")
	}
	write(t, filepath.Join(s.Paths.StateDir, trialFile), "{")
	s.ResumeTrial(context.Background())
	if got := readFile(t, filepath.Join(s.Paths.StateDir, trialFile)); got != "{" {
		t.Errorf("a record that cannot be read is %q, want it kept", got)
	}
	if _, err := sendConfig(s, nextConfig); connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "read the record of the config's trial") {
		t.Errorf("a delivery past a record that cannot be read: %v", err)
	}
}

// TestResumeTrialStopsWithChalkd ends the delivery that finishes a trial's rollback, and a
// rollback at the deadline, once the context ResumeTrial was given ends, as when chalkd stops,
// before their own timeout; the trial stays open for the next start.
func TestResumeTrialStopsWithChalkd(t *testing.T) {
	for name, prepare := range map[string]func(*testing.T, *Server, *trialRecord){
		"ended": func(t *testing.T, s *Server, _ *trialRecord) {
			if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
				t.Fatal(err)
			}
		},
		"past its deadline": func(_ *testing.T, _ *Server, r *trialRecord) { r.Deadline = time.Now().Add(-time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			s := trialServer(t)
			s.trialTimeout, s.trialRetry = time.Hour, time.Hour
			rec := recordTrial(t, s, nextConfig)
			prepare(t, s, &rec)
			if err := writeTrial(s.Paths.StateDir, rec); err != nil {
				t.Fatal(err)
			}
			writeOutputs(t, s, map[string]outputs.Definition{
				"demo": demoOutput(t, s, `exec sleep 10`, ""), "networkd": networkdOutput(t, s), "chrony": chronyOutput(t, s),
			})
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			s.ResumeTrial(ctx)
			eventually(t, "the failed attempt", func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				r, _, _ := readTrial(s.Paths.StateDir)
				return r.Attempts == 1
			})
			if took := time.Since(start); took > 5*time.Second {
				t.Errorf("the rollback took %v after chalkd stopped", took)
			}
		})
	}
}

// TestTrialExpiryKeepsARecordItCannotRemove arms the deadline again when the record of a trial
// that ended cannot be removed, so the trial is not lost.
func TestTrialExpiryKeepsARecordItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s := trialServer(t)
	s.trialRetry = time.Hour
	rec := recordTrial(t, s, nextConfig)
	if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(filepath.Join(s.Paths.StateDir, trialFile))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	expire(s, rec)
	s.mu.Lock()
	armed := s.trialTimer != nil
	s.mu.Unlock()
	if !trialOpenOn(t, s) || !armed {
		t.Errorf("the record stays %v and the deadline is armed %v, want both", trialOpenOn(t, s), armed)
	}
}

// TestRestoreTrialAtBoot restores the config from before an open trial, as chalkos-config does
// before it applies the slot, and removes the record. A trial that was never applied or ended
// leaves the slot as it is. A record that cannot be read stays.
func TestRestoreTrialAtBoot(t *testing.T) {
	s := trialServer(t)
	rec := recordTrial(t, s, nextConfig)
	if err := RestoreTrialAtBoot(s.Paths.StateDir, s.Loader.Slots); err != nil {
		t.Fatal(err)
	}
	dir := slotOf(t, s)
	check := func(name string) {
		t.Helper()
		running, _ := slot.Version(dir)
		previous, _ := slot.Version(slot.Previous(dir))
		if running != rec.Previous || previous != rec.Version {
			t.Errorf("%s: the slot runs %s and keeps %s, want %s and %s", name, running, previous, rec.Previous, rec.Version)
		}
		if _, err := os.Stat(filepath.Join(s.Paths.StateDir, trialFile)); !os.IsNotExist(err) {
			t.Errorf("%s: the record stays: %v", name, err)
		}
	}
	check("an open trial")
	if err := RestoreTrialAtBoot(s.Paths.StateDir, s.Loader.Slots); err != nil {
		t.Errorf("without a record: %v", err)
	}
	check("without a record")
	if err := writeTrial(s.Paths.StateDir, rec); err != nil {
		t.Fatal(err)
	}
	if err := RestoreTrialAtBoot(s.Paths.StateDir, s.Loader.Slots); err != nil {
		t.Errorf("an ended trial: %v", err)
	}
	check("an ended trial")
	lost := rec
	lost.Version, lost.Previous = "other", "another"
	if err := writeTrial(s.Paths.StateDir, lost); err != nil {
		t.Fatal(err)
	}
	if err := RestoreTrialAtBoot(s.Paths.StateDir, s.Loader.Slots); err != nil {
		t.Errorf("a lost trial: %v", err)
	}
	check("a lost trial")
	write(t, filepath.Join(s.Paths.StateDir, trialFile), "{")
	if err := RestoreTrialAtBoot(s.Paths.StateDir, s.Loader.Slots); err == nil || !strings.Contains(err.Error(), "parse the trial record: ") {
		t.Errorf("a record that cannot be read: %v", err)
	}
	if got := readFile(t, filepath.Join(s.Paths.StateDir, trialFile)); got != "{" {
		t.Errorf("a record that cannot be read is %q, want it kept", got)
	}
}

// checkExpiredAtBoot checks that the boot kept the record of the trial rec with its deadline
// moved to the boot's time, so chalkd rolls the trial back as it starts.
func checkExpiredAtBoot(t *testing.T, s *Server, rec trialRecord, booted time.Time) {
	t.Helper()
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil || !open {
		t.Fatalf("the record is gone: %v", err)
	}
	if r.Deadline.Before(booted) || r.Deadline.After(time.Now()) {
		t.Errorf("the deadline is %v, want the boot's time", r.Deadline)
	}
	r.Deadline = rec.Deadline
	if !r.sameTrial(rec) {
		t.Errorf("the record is %+v, want %+v with another deadline", r, rec)
	}
}

// TestRestoreTrialAtBootUnreadableSlot keeps the record of a trial whose slot cannot be read, with
// its deadline moved to the boot's time.
func TestRestoreTrialAtBootUnreadableSlot(t *testing.T) {
	s := trialServer(t)
	rec := recordTrial(t, s, nextConfig)
	version := filepath.Join(slotOf(t, s), "version")
	if err := os.Remove(version); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(version, 0o755); err != nil {
		t.Fatal(err)
	}
	booted := time.Now()
	if err := RestoreTrialAtBoot(s.Paths.StateDir, s.Loader.Slots); err == nil || !strings.Contains(err.Error(), "read the config of the trial's slot") {
		t.Errorf("err = %v, want the slot that cannot be read", err)
	}
	checkExpiredAtBoot(t, s, rec, booted)
}

// TestRollbackConfigRemovesAnUnreadableTrial removes a trial record that cannot be read, which
// refuses every other change, and rolls the config back.
func TestRollbackConfigRemovesAnUnreadableTrial(t *testing.T) {
	s, _ := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	write(t, filepath.Join(s.Paths.StateDir, trialFile), "{")
	want := "read the record of the config's trial: parse the trial record: "
	hint := "; other changes are refused until chalkctl apply-config --rollback n1 restores the previous config, which removes the record"
	if _, err := sendConfig(s, baseConfig); connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), hint) {
		t.Errorf("a delivery: %v, want %q and %q", err, want, hint)
	}
	if _, err := sendRollback(s, rec.Previous); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Paths.StateDir, trialFile)); !os.IsNotExist(err) {
		t.Errorf("the record stays: %v", err)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Previous {
		t.Errorf("the node runs %s, want %s", v, rec.Previous)
	}
}

// TestRollbackConfigKeepsAnUnreadableTrialItCannotRollBack keeps a trial record that cannot be
// read when the rollback is refused: the record goes only once the config is rolled back.
func TestRollbackConfigKeepsAnUnreadableTrialItCannotRollBack(t *testing.T) {
	s, _ := configServer(t)
	recordTrial(t, s, nextConfig)
	write(t, filepath.Join(s.Paths.StateDir, trialFile), "{")
	if _, err := sendRollback(s, "v9"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a rollback to a config the node does not keep: %v, want it refused", err)
	}
	if got := readFile(t, filepath.Join(s.Paths.StateDir, trialFile)); got != "{" {
		t.Errorf("the record is %q, want it kept", got)
	}
}

// TestRollbackConfigFinishesAnUnreadableTrial delivers the config the slot runs again when it is
// the version asked for, as after a rollback at the deadline that wrote the slot and failed after,
// and removes the record that cannot be read.
func TestRollbackConfigFinishesAnUnreadableTrial(t *testing.T) {
	s, _ := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.Paths.StateDir, trialFile), "{")
	if _, err := sendRollback(s, rec.Previous); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(s.Paths.StateDir, trialFile)) {
		t.Error("the record stays")
	}
	checkRolledBack(t, s, rec)
}

// TestRestoreTrialAtBootFailing keeps the record of a trial whose slot cannot be restored, with
// its deadline moved to the boot's time, so chalkd rolls the trial back as it starts.
func TestRestoreTrialAtBootFailing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s := trialServer(t)
	rec := recordTrial(t, s, nextConfig)
	slots := filepath.Dir(slotOf(t, s))
	// The directory of slots takes no new link.
	if err := os.Chmod(slots, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slots, 0o755) })
	booted := time.Now()
	if err := RestoreTrialAtBoot(s.Paths.StateDir, s.Loader.Slots); err == nil || !strings.Contains(err.Error(), "restore config "+rec.Previous) {
		t.Errorf("err = %v, want the failed restore", err)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Version {
		t.Errorf("the node runs %s, want the config on trial", v)
	}
	checkExpiredAtBoot(t, s, rec, booted)
}

// failRestartOnce has the next restart of demo.service fail.
func failRestartOnce(s *Server) *tooltest.Runner {
	r := s.Run.(*tooltest.Runner)
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl try-restart demo.service", Err: errors.New("exit status 1"), Times: 1}}, r.Rules...)
	return r
}

// openTrialOf sends next on trial for an hour, with retries an hour apart, and returns its record.
func openTrialOf(t *testing.T, s *Server, next string) trialRecord {
	t.Helper()
	s.trialRetry = time.Hour
	if _, err := sendTrial(s, next, time.Hour); err != nil {
		t.Fatal(err)
	}
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil || !open {
		t.Fatalf("no trial recorded: %v", err)
	}
	return r
}

// checkRolledBack checks that the slot runs the config from before the trial r and keeps the
// one on trial as its previous config.
func checkRolledBack(t *testing.T, s *Server, r trialRecord) {
	t.Helper()
	running, _ := slot.Version(slotOf(t, s))
	previous, _ := slot.Version(slot.Previous(slotOf(t, s)))
	if running != r.Previous || previous != r.Version {
		t.Errorf("the slot runs %s and keeps %s, want %s and %s", running, previous, r.Previous, r.Version)
	}
}

// TestTrialExpiryFinishesAFailedRestart keeps the trial open when its rollback at the deadline
// wrote the slot and a restart failed after it: the slot runs the config from before the trial,
// but demo.service still runs the one on trial. Status says the rollback changed the node, a
// confirmation is refused, and the next attempt delivers the running config again, restarts
// demo.service and only then ends the trial.
func TestTrialExpiryFinishesAFailedRestart(t *testing.T) {
	s := trialServer(t)
	first := openTrialOf(t, s, nextConfig)
	r := failRestartOnce(s)
	expire(s, first)
	rec, open, err := readTrial(s.Paths.StateDir)
	if err != nil || !open || !strings.Contains(rec.Error, "try-restart demo.service") || rec.Attempts != 1 || !rec.Changed {
		t.Fatalf("after a restart that failed the record is %+v, open %v, %v; want it open and changed", rec, open, err)
	}
	checkRolledBack(t, s, rec)
	if tr := statusOf(t, s).Trial; tr == nil || !tr.RollbackChanged || tr.Error != rec.Error {
		t.Errorf("status trial = %+v, want the failure that changed the node", tr)
	}
	err = sendConfirm(s, rec.Version)
	for _, want := range []string{"chalkd tries again each minute", "chalkctl apply-config --rollback n1 finishes it now"} {
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
			t.Errorf("a confirmation: %v, want %q", err, want)
		}
	}
	r.ResetCalls()
	expire(s, rec)
	if trialOpenOn(t, s) {
		t.Error("the trial is open after the retry went through")
	}
	if !slices.Contains(r.Calls, "systemctl try-restart demo.service") {
		t.Errorf("the retry ran %v, want demo.service restarted", r.Calls)
	}
	checkRolledBack(t, s, rec)
}

// TestTrialExpiryUndoesAFailedTrialDelivery rolls back a trial whose delivery restarted
// demo.service on the config on trial and failed after it: the rollback at the deadline restarts
// demo.service on the config from before the trial again.
func TestTrialExpiryUndoesAFailedTrialDelivery(t *testing.T) {
	s := trialServer(t)
	s.trialRetry = time.Hour
	write(t, s.Loader.Effects, lateHookEffects)
	r := s.Run.(*tooltest.Runner)
	r.Rules = append([]tooltest.Rule{{Prefix: "hook", Err: errors.New("exit status 1"), Times: 1}}, r.Rules...)
	if _, err := sendTrial(s, nextConfig, time.Hour); err == nil || !strings.Contains(err.Error(), "stays on trial") {
		t.Fatalf("err = %v, want the hook's failure with the config on trial", err)
	}
	rec, open, err := readTrial(s.Paths.StateDir)
	if err != nil || !open {
		t.Fatalf("no trial recorded: %v", err)
	}
	r.ResetCalls()
	expire(s, rec)
	if trialOpenOn(t, s) {
		t.Error("the trial is open after its rollback")
	}
	if !slices.Contains(r.Calls, "systemctl try-restart demo.service") {
		t.Errorf("the rollback ran %v, want demo.service restarted", r.Calls)
	}
	checkRolledBack(t, s, rec)
}

// TestRollbackConfigFinishesATrialsRollback finishes a rollback at the deadline that failed after
// it wrote the slot: a rollback on request to the config from before the trial, which the slot
// runs already, delivers it again, restarts demo.service and ends the trial.
func TestRollbackConfigFinishesATrialsRollback(t *testing.T) {
	s := trialServer(t)
	first := openTrialOf(t, s, nextConfig)
	r := failRestartOnce(s)
	expire(s, first)
	if !trialOpenOn(t, s) {
		t.Fatal("the trial ended with a restart that failed")
	}
	r.ResetCalls()
	if _, err := sendRollback(s, first.Previous); err != nil {
		t.Fatal(err)
	}
	if trialOpenOn(t, s) {
		t.Error("the trial is open after the rollback on request")
	}
	if !slices.Contains(r.Calls, "systemctl try-restart demo.service") {
		t.Errorf("the rollback ran %v, want demo.service restarted", r.Calls)
	}
	checkRolledBack(t, s, first)
}

// TestRollbackConfigDuringATrialTakesItsPrevious refuses a rollback during a trial to any config
// but the one from before it: once a rollback at the deadline wrote the slot, its previous config
// is the one on trial, which would apply it for good.
func TestRollbackConfigDuringATrialTakesItsPrevious(t *testing.T) {
	s, r := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
		t.Fatal(err)
	}
	r.ResetCalls()
	want := "config " + rec.Version + " is on trial, so a rollback delivers config " + rec.Previous + " from before it, not " + rec.Version
	if _, err := sendRollback(s, rec.Version); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want %q", err, want)
	}
	if !trialOpenOn(t, s) {
		t.Error("the refusal ended the trial")
	}
	checkRolledBack(t, s, rec)
	if len(r.Calls) != 0 {
		t.Errorf("the refusal ran %v", r.Calls)
	}
}

// TestTrialFailingBeforeItsSlotIsFinished delivers the running config again at once when a trial's
// delivery fails before it writes the slot, so nothing of the config on trial stays applied, and
// no trial is open.
func TestTrialFailingBeforeItsSlotIsFinished(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s := trialServer(t)
	slots := filepath.Dir(slotOf(t, s))
	// The directory of slots takes no new slot directory.
	if err := os.Chmod(slots, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slots, 0o755) })
	base := hashOf(t, baseConfig)
	_, err := sendTrial(s, nextConfig, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "store the config") || !strings.Contains(err.Error(), "config "+base+" is delivered again, and no trial is open") {
		t.Errorf("err = %v, want the slot's failure and the running config delivered again", err)
	}
	if trialOpenOn(t, s) {
		t.Error("the trial is open")
	}
	if exists(s.Paths.EffectsBaseline) {
		t.Error("the effects' baseline stays, so the delivery was not applied whole")
	}
	s.mu.Lock()
	armed := s.trialTimer != nil
	s.mu.Unlock()
	if armed {
		t.Error("a deadline is armed")
	}
	if v, _ := slot.Version(slotOf(t, s)); v != base {
		t.Errorf("the node runs %s, want %s", v, base)
	}
}

// TestTrialExpiryStaleDeadline leaves the trial and its deadline alone when a deadline fires that
// was armed before the one armed last: it neither rolls the trial back nor replaces that deadline.
func TestTrialExpiryStaleDeadline(t *testing.T) {
	s := trialServer(t)
	rec := recordTrial(t, s, nextConfig)
	s.mu.Lock()
	s.armTrial(rec, time.Hour)
	stale := s.trialArmed
	s.armTrial(rec, time.Hour)
	timer := s.trialTimer
	s.mu.Unlock()
	s.expireTrial(rec.Version, rec.Deadline, stale)
	if !trialOpenOn(t, s) {
		t.Error("a stale deadline ended the trial")
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Version {
		t.Errorf("the node runs %s, want the config on trial", v)
	}
	s.mu.Lock()
	kept := s.trialTimer == timer
	s.mu.Unlock()
	if !kept {
		t.Error("a stale deadline replaced the one armed after it")
	}
}

// TestTrialExpiryTimesOut counts a rollback at the deadline that outlasts its time as a failed
// attempt, and tries it again.
func TestTrialExpiryTimesOut(t *testing.T) {
	s := trialServer(t)
	s.trialTimeout = time.Second
	first := openTrialOf(t, s, nextConfig)
	writeOutputs(t, s, map[string]outputs.Definition{
		"demo": demoOutput(t, s, `exec sleep 10`, ""), "networkd": networkdOutput(t, s), "chrony": chronyOutput(t, s),
	})
	expire(s, first)
	rec, open, err := readTrial(s.Paths.StateDir)
	if err != nil || !open || !strings.Contains(rec.Error, "its renderer did not finish") || rec.Attempts != 1 || rec.Changed {
		t.Fatalf("after a rollback that timed out the record is %+v, open %v, %v; want it open with one attempt", rec, open, err)
	}
	s.mu.Lock()
	armed := s.trialTimer != nil
	s.mu.Unlock()
	if !armed {
		t.Error("the retry is not armed")
	}
	if v, _ := slot.Version(slotOf(t, s)); v != rec.Version {
		t.Errorf("the node runs %s, want the config on trial", v)
	}
}

// TestChangesWaitForAnEndedTrialsRollback refuses with ABORTED a delivery that finds a trial whose
// slot runs the config from before it while an upgrade, an image rollback or the rollback at the
// deadline runs: that rollback is finished before the record goes, and the lock it needs is held.
func TestChangesWaitForAnEndedTrialsRollback(t *testing.T) {
	s, _ := configServer(t)
	rec := recordTrial(t, s, nextConfig)
	if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
		t.Fatal(err)
	}
	s.upgrading.Lock()
	_, err := sendConfig(s, nextConfig)
	s.upgrading.Unlock()
	if connect.CodeOf(err) != connect.CodeAborted || !strings.Contains(err.Error(), "the trial of config "+rec.Version+" ended, and its record goes once the node delivered config "+rec.Previous+" again") {
		t.Errorf("err = %v, want the rollback to finish first", err)
	}
	if !trialOpenOn(t, s) {
		t.Error("the record is gone")
	}
}
