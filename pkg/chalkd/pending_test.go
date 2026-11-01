package chalkd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/upgrade"
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

// TestStageConfigTakesThePendingConfig writes the pending config to the new image's slot when the
// upgrade carries none, in place of the running config, and keeps it until the upgrade staged the
// image, noting that the image took it.
func TestStageConfigTakesThePendingConfig(t *testing.T) {
	s, _ := configServer(t)
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	p := sendPending(t, s, next)
	c, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := staged(t, s); got.Version != hashOf(t, next) {
		t.Errorf("staged version = %q, want the pending config's", got.Version)
	}
	if v := pendingVersion(t, s); v != p.Version {
		t.Errorf("pending version = %q before the image is staged, want %s kept", v, p.Version)
	}
	if c.from != fromPending || c.pending != p.Version {
		t.Errorf("staged config from %v, pending %q; want the pending config %s taken", c.from, c.pending, p.Version)
	}
}

// TestStageConfigOverridesThePendingConfig writes the config the upgrade carries, keeps the pending
// config until the upgrade staged the image, and notes that the upgrade replaced it.
func TestStageConfigOverridesThePendingConfig(t *testing.T) {
	s, _ := configServer(t)
	p := sendPending(t, s, strings.Replace(baseConfig, "name: first", "name: second", 1))
	next := strings.Replace(baseConfig, "name: first", "name: third", 1)
	c, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, next, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := staged(t, s); got.Version != hashOf(t, next) {
		t.Errorf("staged version = %q, want the delivered config's", got.Version)
	}
	if v := pendingVersion(t, s); v != p.Version {
		t.Errorf("pending version = %q before the image is staged, want %s kept", v, p.Version)
	}
	if c.from != fromUpgrade || c.pending != p.Version {
		t.Errorf("staged config from %v, pending %q; want the pending config %s replaced", c.from, c.pending, p.Version)
	}
}

// TestStageConfigKeepsARefusedPendingConfig refuses the upgrade whose image refuses the pending
// config, naming it and the commands that replace or drop it, and keeps it. One that adds an
// encrypted volume names the upgrade that carries the fallback secret.
func TestStageConfigKeepsARefusedPendingConfig(t *testing.T) {
	withoutDemo := fixtureSchemas(t)
	delete(withoutDemo, "demo.example.org_v1alpha1_Demo.json")
	for name, tc := range map[string]struct {
		schemas map[string][]byte
		config  string
		want    []string
	}{
		"an unknown kind":        {withoutDemo, baseConfig, []string{`does not accept kind "Demo"`, "chalkctl apply-config --staged n1 replaces it, and chalkctl apply-config --staged --discard n1 drops it"}},
		"a new encrypted volume": {fixtureSchemas(t), extraConfig("ext4"), []string{"no fallback secret was sent", "a pending config is kept without the fallback secret, so upgrade with the config from an inventory, which carries the secret and needs the admin role"}},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := configServer(t)
			r.Rules = volumeRules(s)
			p := sendPending(t, s, tc.config)
			c, err := s.stageConfig(context.Background(), imageWith(t, s, tc.schemas, ""), "/nix/store/x", "metal", newImage, "", "")
			if err == nil || !strings.Contains(err.Error(), "the pending config "+p.Version+" does not suit the image: ") {
				t.Fatalf("err = %v, want the pending config named", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %v, want it to say %q", err, w)
				}
			}
			if _, found := staged(t, s); found {
				t.Error("wrote the new image's slot")
			}
			if v := pendingVersion(t, s); v != p.Version {
				t.Errorf("pending version = %q, want it kept", v)
			}
			if c.pending != "" {
				t.Errorf("the refused upgrade notes the pending config %s as staged", c.pending)
			}
		})
	}
}

// upgradePending runs an upgrade of newImage by role, carrying cfg, on s, whose installation
// writes the image's slot and then returns fail; afterwards it runs after, which may change
// what the node holds. It returns the version the image's slot was written with and the
// upgrade's error.
func upgradePending(t *testing.T, s *Server, role pki.Role, cfg string, fail error, after func()) (string, error) {
	t.Helper()
	c := newCreds(t)
	// The upgrade retires the slots of images the ESP does not boot, newImage's among them, so
	// the installation reads the slot it wrote.
	var version string
	s.InstallImage = func(ctx context.Context, _ upgrade.Header, config, secret string, r io.Reader) (upgrade.Result, imageConfig, error) {
		io.Copy(io.Discard, r)
		wrote, err := s.stageConfig(ctx, imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, config, secret)
		if err != nil {
			return upgrade.Result{}, imageConfig{}, err
		}
		got, _ := staged(t, s)
		version = got.Version
		if after != nil {
			after()
		}
		return upgrade.Result{Entry: "chalkos_2+3.efi"}, wrote, fail
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[role])
	stream := conn.Upgrade(context.Background())
	header := &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{ImageId: "chalkos", RootHash: newImage}, Config: cfg}
	if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: header}}); err != nil {
		t.Fatal(err)
	}
	_, err := stream.CloseAndReceive()
	return version, err
}

// TestUpgradeDropsThePendingConfig has an upgrade without config, as an operator's, boot the image
// with the pending config an admin held, and an admin's upgrade with config replace it; either
// drops the pending config once the image is staged.
func TestUpgradeDropsThePendingConfig(t *testing.T) {
	pending := strings.Replace(baseConfig, "name: first", "name: second", 1)
	carried := strings.Replace(baseConfig, "name: first", "name: third", 1)
	for _, tc := range []struct {
		name   string
		role   pki.Role
		config string
		want   string
	}{
		{"an operator's", pki.RoleOperator, "", pending},
		{"an admin's with config", pki.RoleAdmin, carried, carried},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := configServer(t)
			sendPending(t, s, pending)
			version, err := upgradePending(t, s, tc.role, tc.config, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if version != hashOf(t, tc.want) {
				t.Errorf("staged version = %q, want %q", version, hashOf(t, tc.want))
			}
			if v := pendingVersion(t, s); v != "" {
				t.Errorf("pending config %s is kept", v)
			}
		})
	}
}

// TestUpgradeKeepsThePendingConfigWhenStagingFails keeps the pending config the image's slot took
// when the image does not become the next boot after all, as when its UKI fails to activate.
func TestUpgradeKeepsThePendingConfigWhenStagingFails(t *testing.T) {
	s, _ := configServer(t)
	p := sendPending(t, s, strings.Replace(baseConfig, "name: first", "name: second", 1))
	if _, err := upgradePending(t, s, pki.RoleOperator, "", errors.New("activate the UKI: no space left on device"), nil); err == nil || !strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("upgrade = %v, want the activation's failure", err)
	}
	if v := pendingVersion(t, s); v != p.Version {
		t.Errorf("pending version = %q, want %s kept", v, p.Version)
	}
	if got := status(t, s).PendingConfig; got.GetVersion() != p.Version {
		t.Errorf("status names the pending config %v, want %s", got, p.Version)
	}
}

// TestUpgradeSucceedsWhenThePendingConfigStays answers the upgrade whose image is staged although
// the pending config could not be removed; status keeps naming it.
func TestUpgradeSucceedsWhenThePendingConfigStays(t *testing.T) {
	s, _ := configServer(t)
	sendPending(t, s, strings.Replace(baseConfig, "name: first", "name: second", 1))
	// A directory where the pending config's link would be is refused by its removal.
	dir := slot.Pending(s.Loader.Slots)
	block := func() {
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "version"), []byte("p9"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := upgradePending(t, s, pki.RoleOperator, "", nil, block); err != nil {
		t.Fatalf("upgrade = %v, want it staged", err)
	}
	if got := status(t, s).PendingConfig; got.GetVersion() != "p9" {
		t.Errorf("status names the pending config %v, want p9", got)
	}
}
