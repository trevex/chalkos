package chalkd

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/slot"
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
// left both links on it; lost otherwise.
func TestTrialState(t *testing.T) {
	s, _ := configServer(t)
	r := recordTrial(t, s, nextConfig)
	check := func(want trialState) {
		t.Helper()
		if _, got, err := r.state(s.Loader.Slots); err != nil || got != want {
			t.Errorf("state = %v, %v; want %v", got, err, want)
		}
	}
	check(trialApplied)
	if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
		t.Fatal(err)
	}
	check(trialEnded)
	r.Version, r.Previous = "v8", "v9"
	check(trialLost)
}
