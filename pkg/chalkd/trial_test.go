package chalkd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/pki"
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

// TestConfirmConfigNotApplied refuses a trial whose slot no longer runs the config on trial.
func TestConfirmConfigNotApplied(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
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

// TestConfirmConfigAfterTheDeadline refuses a trial past its deadline, unless the rollback at the
// deadline failed: then the confirmation ends the trial and its retries.
func TestConfirmConfigAfterTheDeadline(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	r.Deadline = time.Now().Add(-time.Minute).Truncate(time.Second).UTC()
	if err := writeTrial(s.Paths.StateDir, r); err != nil {
		t.Fatal(err)
	}
	if err := sendConfirm(s, r.Version); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the trial of config "+r.Version+" ended at its deadline, "+r.Deadline.Format(time.RFC3339)) {
		t.Errorf("past the deadline: %v", err)
	}
	if !trialOpenOn(t, s) {
		t.Fatal("the refusal ended the trial")
	}
	r.Error = "an output failed"
	if err := writeTrial(s.Paths.StateDir, r); err != nil {
		t.Fatal(err)
	}
	if err := sendConfirm(s, r.Version); err != nil {
		t.Fatalf("past the deadline with a failed rollback: %v", err)
	}
	if trialOpenOn(t, s) {
		t.Error("the confirmed trial is still open")
	}
}

// TestConfirmConfigCorruptRecord fails a confirmation on a trial record that cannot be read, and
// Status leaves the trial out.
func TestConfirmConfigCorruptRecord(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	if err := os.WriteFile(filepath.Join(s.Paths.StateDir, trialFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sendConfirm(s, r.Version); connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "read the record of the config's trial") {
		t.Errorf("a corrupt record: %v", err)
	}
	if tr := statusOf(t, s).Trial; tr != nil {
		t.Errorf("status trial = %+v, want none", tr)
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
