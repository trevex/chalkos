package chalkd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/slot"
)

// sendRollback calls RollbackConfig for the version.
func sendRollback(s *Server, version string) (*nodev1.RollbackConfigResponse, error) {
	resp, err := s.RollbackConfig(context.Background(), connect.NewRequest(&nodev1.RollbackConfigRequest{Version: version}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// TestRollbackConfig delivers the previous config again through the pipeline: the units that read
// what changed restart, the secret returns to the units' credentials, and the replaced config
// becomes the previous one, so a second rollback undoes the first.
func TestRollbackConfig(t *testing.T) {
	s, r := configServer(t)
	next := strings.NewReplacer("token: s3cret", "token: n3w", "name: first", "name: second").Replace(baseConfig)
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	r.Calls = nil
	resp, err := sendRollback(s, hashOf(t, baseConfig))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"demo.service"}) || !slices.Contains(r.Calls, "systemctl try-restart demo.service") {
		t.Errorf("response %+v, commands %v; want demo.service restarted", resp, r.Calls)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
		t.Errorf("running version = %q, want baseConfig's", v)
	}
	if v, _ := slot.Version(slot.Previous(slotOf(t, s))); v != hashOf(t, next) {
		t.Errorf("previous version = %q, want next's", v)
	}
	if got := readFile(t, filepath.Join(s.Loader.RunDir, "credentials", "demo", "demo.token")); got != "s3cret" {
		t.Errorf("demo's credential = %q, want the previous config's", got)
	}
	if _, err := sendRollback(s, hashOf(t, next)); err != nil {
		t.Fatal(err)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, next) {
		t.Errorf("after a second rollback the node runs %q, want next's", v)
	}
}

// TestRollbackConfigRefuses refuses a request without a version, one naming another version than
// the previous config's, a node without a previous config, and a rollback while an upgrade runs;
// nothing changes.
func TestRollbackConfigRefuses(t *testing.T) {
	s, r := configServer(t)
	if _, err := sendRollback(s, ""); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "the request names no version to roll back to") {
		t.Errorf("without a version: %v", err)
	}
	if _, err := sendRollback(s, hashOf(t, baseConfig)); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the node keeps no previous config of the running image") {
		t.Errorf("without a previous config: %v", err)
	}
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	r.Calls = nil
	if _, err := sendRollback(s, hashOf(t, next)); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the node's previous config is "+hashOf(t, baseConfig)+", not "+hashOf(t, next)) {
		t.Errorf("another version: %v", err)
	}
	s.upgrading.Lock()
	if _, err := sendRollback(s, hashOf(t, baseConfig)); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("during an upgrade: %v, want aborted", err)
	}
	s.upgrading.Unlock()
	if len(r.Calls) != 0 {
		t.Errorf("refusals ran %v", r.Calls)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, next) {
		t.Errorf("running version = %q, want it unchanged", v)
	}
}

// TestRollbackConfigMeetsTheRules refuses a rollback the image's rules refuse, as any delivery:
// the previous config lacks the volume the config it would replace added.
func TestRollbackConfigMeetsTheRules(t *testing.T) {
	s, r := configServer(t)
	r.Rules = volumeRules(s)
	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), FallbackSecret: secret})); err != nil {
		t.Fatal(err)
	}
	_, err := sendRollback(s, hashOf(t, baseConfig))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "Volume extra: the config removes it; only a reinstall of n1 removes a volume") {
		t.Errorf("rollback = %v, want the storage rule's refusal", err)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, extraConfig("ext4")) {
		t.Errorf("running version = %q, want it unchanged", v)
	}
}

// TestRollbackConfigAfterAnInterruptedWrite treats a previous config that is the set the slot
// holds, as a write interrupted between the two links leaves it, as none: Status names no previous
// config, and a rollback is refused as on a node without one.
func TestRollbackConfigAfterAnInterruptedWrite(t *testing.T) {
	s, r := configServer(t)
	dir := slotOf(t, s)
	held, err := os.Readlink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(held, slot.Previous(dir)); err != nil {
		t.Fatal(err)
	}
	resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.ConfigVersion != hashOf(t, baseConfig) || resp.Msg.PreviousConfigVersion != "" {
		t.Errorf("status names config %q and previous %q, want baseConfig and none", resp.Msg.ConfigVersion, resp.Msg.PreviousConfigVersion)
	}
	r.Calls = nil
	if _, err := sendRollback(s, hashOf(t, baseConfig)); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the node keeps no previous config of the running image") {
		t.Errorf("rollback = %v, want no previous config", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("the refusal ran %v", r.Calls)
	}
}
