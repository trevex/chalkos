package chalkd

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
)

// sendPending sends text with staged to s, which has no image staged, and returns the pending
// config the node answers with.
func sendPending(t *testing.T, s *Server, text string) *nodev1.PendingConfig {
	t.Helper()
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: text, Staged: true}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Pending == nil {
		t.Fatal("the answer names no pending config")
	}
	return resp.Msg.Pending
}

// parseConfig is the documents of text.
func parseConfig(t *testing.T, text string) []config.Document {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// pendingVersion is the version of the pending config s holds; empty without one.
func pendingVersion(t *testing.T, s *Server) string {
	t.Helper()
	v, err := slot.Version(slot.Pending(s.Loader.Slots))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestApplyConfigStagedHoldsPending keeps config for the staged image on a node without one as its
// pending config, unchecked, and changes nothing else: the running slot stays, nothing runs. Status
// and GetConfig with staged name it, and another delivery replaces it.
func TestApplyConfigStagedHoldsPending(t *testing.T) {
	s, r := configServer(t)
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	before := time.Now().Add(-time.Second)
	p := sendPending(t, s, next)
	if p.Version != hashOf(t, next) || p.Since.AsTime().Before(before) {
		t.Errorf("pending = %v, want config %s since now", p, hashOf(t, next))
	}
	if v := pendingVersion(t, s); v != hashOf(t, next) {
		t.Errorf("pending version = %q", v)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
		t.Errorf("running version = %q, want it unchanged", v)
	}
	if len(r.Calls) != 0 {
		t.Errorf("ran %v", r.Calls)
	}
	if got := status(t, s).PendingConfig; got.GetVersion() != p.Version || !got.Since.AsTime().Equal(p.Since.AsTime()) {
		t.Errorf("status names the pending config %v, want %v", got, p)
	}
	resp, err := s.GetConfig(context.Background(), connect.NewRequest(&nodev1.GetConfigRequest{Staged: true}))
	if err != nil || resp.Msg.Version != p.Version || !strings.Contains(resp.Msg.Documents, `"name":"second"`) || strings.Contains(resp.Msg.Documents, "s3cret") {
		t.Errorf("staged config = %+v, %v; want the pending config, its Secret hashed", resp, err)
	}
	// Config no image accepts is held all the same: nothing checks it before an upgrade.
	unknown := baseConfig + "---\napiVersion: nope.example.org/v1\nkind: Nope\nmetadata: {name: x}\n"
	if p := sendPending(t, s, unknown); p.Version != hashOf(t, unknown) || pendingVersion(t, s) != p.Version {
		t.Errorf("replaced pending = %v, held %q", p, pendingVersion(t, s))
	}
}

// TestApplyConfigStagedPendingRefuses refuses a fallback secret and config that does not parse,
// and keeps nothing; a dry run answers with the config it would hold and keeps it not.
func TestApplyConfigStagedPendingRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		req  *nodev1.ApplyConfigRequest
		want string
	}{
		"a fallback secret": {&nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true, FallbackSecret: secret}, "stored without the fallback secret; the upgrade that adds an encrypted volume carries it"},
		"unparsable config": {&nodev1.ApplyConfigRequest{Config: "kind: [", Staged: true}, "parse the config"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := configServer(t)
			_, err := s.ApplyConfig(context.Background(), connect.NewRequest(tc.req))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one naming %q", err, tc.want)
			}
			if v := pendingVersion(t, s); v != "" {
				t.Errorf("holds pending config %s", v)
			}
		})
	}
	s, _ := configServer(t)
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true, DryRun: true}))
	if err != nil || resp.Msg.Pending.GetVersion() != hashOf(t, baseConfig) || resp.Msg.Pending.GetSince() != nil || resp.Msg.Plan != nil {
		t.Errorf("dry run = %v, %v; want the config it would hold, unstored", resp, err)
	}
	if v := pendingVersion(t, s); v != "" {
		t.Errorf("the dry run holds pending config %s", v)
	}
}

// TestApplyConfigDiscardsPending drops the pending config and names it; without one it names none.
// The request takes staged alone.
func TestApplyConfigDiscardsPending(t *testing.T) {
	s, _ := configServer(t)
	p := sendPending(t, s, baseConfig)
	discard := func(req *nodev1.ApplyConfigRequest) (*nodev1.PendingConfig, error) {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return resp.Msg.Pending, nil
	}
	if _, err := discard(&nodev1.ApplyConfigRequest{Staged: true, DiscardPending: true, Config: baseConfig}); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "discard_pending goes with staged alone") {
		t.Errorf("discard with config = %v", err)
	}
	if _, err := discard(&nodev1.ApplyConfigRequest{DiscardPending: true}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("discard without staged = %v", err)
	}
	if got, err := discard(&nodev1.ApplyConfigRequest{Staged: true, DiscardPending: true}); err != nil || got.GetVersion() != p.Version {
		t.Errorf("discard = %v, %v; want %s named", got, err, p.Version)
	}
	if v := pendingVersion(t, s); v != "" || status(t, s).PendingConfig != nil {
		t.Errorf("holds pending config %q after the discard", v)
	}
	if got, err := discard(&nodev1.ApplyConfigRequest{Staged: true, DiscardPending: true}); err != nil || got != nil {
		t.Errorf("a second discard = %v, %v; want none named", got, err)
	}
}

// TestPendingWaitsForAnUpgrade refuses to hold or drop pending config while an upgrade runs, which
// reads and removes it.
func TestPendingWaitsForAnUpgrade(t *testing.T) {
	s, _ := configServer(t)
	s.upgrading.Lock()
	defer s.upgrading.Unlock()
	for _, req := range []*nodev1.ApplyConfigRequest{{Config: baseConfig, Staged: true}, {Staged: true, DiscardPending: true}} {
		if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeAborted {
			t.Errorf("%v = %v, want aborted", req, err)
		}
	}
}

// TestPendingLeavesTheRunningImageAlone delivers config to the running image, plans one and keeps
// the pending config as it is.
func TestPendingLeavesTheRunningImageAlone(t *testing.T) {
	s, _ := configServer(t)
	p := sendPending(t, s, baseConfig)
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	if _, err := dryRun(s, next); err != nil {
		t.Fatal(err)
	}
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, next) {
		t.Errorf("running version = %q, want the delivered config's", v)
	}
	if v := pendingVersion(t, s); v != p.Version {
		t.Errorf("pending version = %q, want %s kept", v, p.Version)
	}
}

// TestApplyConfigStagedKeepsPendingForAStagedImage delivers config to an image staged after the
// pending config was held, as a rollback stages one, and keeps the pending config.
func TestApplyConfigStagedKeepsPendingForAStagedImage(t *testing.T) {
	s, _ := stagedServer(t, fixtureSchemas(t))
	if err := slot.Write(slot.Pending(s.Loader.Slots), slot.Stored{Delivered: parseConfig(t, baseConfig), Version: "p1"}); err != nil {
		t.Fatal(err)
	}
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	if err := sendStaged(s, next); err != nil {
		t.Fatal(err)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, next) {
		t.Errorf("staged version = %q, want the delivered config's", v)
	}
	if v := pendingVersion(t, s); v != "p1" {
		t.Errorf("pending version = %q, want it kept", v)
	}
}
