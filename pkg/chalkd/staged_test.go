package chalkd

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// The images of stagedServer's ESP: the one it runs, booted from chalkos_2.efi, and the one
// chalkos_3+3.efi stages to boot next, as writeESP names their stores.
var (
	runningImage = bytes.Repeat([]byte{'2'}, 32)
	stagedImage  = bytes.Repeat([]byte{'3'}, 32)
)

// stagedServer is configServer after an upgrade without a reboot: the running image's slot and
// the staged image's both hold baseConfig, and the staged image's store, as ReadImage mounts it,
// holds the schemas given.
func stagedServer(t *testing.T, schemas map[string][]byte) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := configServer(t)
	_, held, _, err := s.runningSlot()
	if err != nil {
		t.Fatal(err)
	}
	writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", "chalkos_2.efi", "chalkos_3+3.efi")
	for _, h := range [][]byte{runningImage, stagedImage} {
		if err := slot.Write(slot.Dir(s.Loader.Slots, h), held); err != nil {
			t.Fatal(err)
		}
	}
	if err := slot.Retire(s.Loader.Slots, [][]byte{runningImage, stagedImage}); err != nil {
		t.Fatal(err)
	}
	img := imageWith(t, schemas, "")
	s.ReadImage = func(_ context.Context, rootHash []byte, read func(imageStore, string) error) error {
		if !bytes.Equal(rootHash, stagedImage) {
			return errors.New("the inactive slot does not hold the image")
		}
		return read(img, "/nix/store/x")
	}
	return s, r
}

func slotVersion(t *testing.T, s *Server, rootHash []byte) string {
	t.Helper()
	v, err := slot.Version(slot.Dir(s.Loader.Slots, rootHash))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func sendStaged(s *Server, text string) error {
	_, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: text, Staged: true}))
	return err
}

// TestApplyConfigStaged checks config against the staged image and keeps it in that image's slot
// alone: the running slot stays, and nothing restarts.
func TestApplyConfigStaged(t *testing.T) {
	s, r := stagedServer(t, fixtureSchemas(t))
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	if err := sendStaged(s, next); err != nil {
		t.Fatal(err)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, next) {
		t.Errorf("staged version = %q, want the delivered config's", v)
	}
	if v := slotVersion(t, s, runningImage); v != hashOf(t, baseConfig) {
		t.Errorf("running version = %q, want it unchanged", v)
	}
	if len(r.Calls) != 0 {
		t.Errorf("ran %v", r.Calls)
	}
	if got := status(t, s); got.StagedConfigVersion != hashOf(t, next) || got.ConfigVersion != hashOf(t, baseConfig) {
		t.Errorf("status names the versions %q and staged %q", got.ConfigVersion, got.StagedConfigVersion)
	}
}

// TestApplyConfigStagedRefuses refuses a staged config on a node without a staged image, one the
// staged image does not accept, one that changes the machine's storage destructively, and one
// that comes with node state, and keeps the staged slot as it was.
func TestApplyConfigStagedRefuses(t *testing.T) {
	withoutDemo := fixtureSchemas(t)
	delete(withoutDemo, "demo.example.org_v1alpha1_Demo.json")
	for name, tc := range map[string]struct {
		schemas  map[string][]byte
		req      *nodev1.ApplyConfigRequest
		unstaged bool
		code     connect.Code
		want     string
	}{
		"no staged image":       {fixtureSchemas(t), &nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true}, true, connect.CodeFailedPrecondition, "no image is staged"},
		"an unknown kind":       {withoutDemo, &nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true}, false, connect.CodeInvalidArgument, `does not accept kind "Demo"`},
		"a VAR of another size": {fixtureSchemas(t), &nodev1.ApplyConfigRequest{Config: strings.Replace(baseConfig, "{disk: {path: /dev/vda}}", "{disk: {path: /dev/vda}, var: {size: 1G}}", 1), Staged: true}, false, connect.CodeFailedPrecondition, "only a reinstall of"},
		"no config":             {fixtureSchemas(t), &nodev1.ApplyConfigRequest{Staged: true}, false, connect.CodeInvalidArgument, "delivers config alone"},
		"a share":               {fixtureSchemas(t), &nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true, Layers: shareLayers(t, []byte("{}"))}, false, connect.CodeInvalidArgument, "delivers config alone"},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := stagedServer(t, tc.schemas)
			if tc.unstaged {
				if err := os.Remove(filepath.Join(s.Paths.ESP, "EFI", "Linux", "chalkos_3+3.efi")); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.ApplyConfig(context.Background(), connect.NewRequest(tc.req))
			if connect.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %v naming %q", err, tc.code, tc.want)
			}
			if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
				t.Errorf("staged version = %q, want it unchanged", v)
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v", r.Calls)
			}
		})
	}
}

// TestApplyConfigStagedAddsAVolume makes the additive change a staged config makes to the
// machine at once, while the running image runs, with the fallback secret, and says so.
func TestApplyConfigStagedAddsAVolume(t *testing.T) {
	s, r := stagedServer(t, fixtureSchemas(t))
	r.Rules = volumeRules(s)
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), Staged: true, FallbackSecret: secret}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Changes) != 1 || resp.Msg.Changes[0].Volume != "extra" {
		t.Errorf("changes = %v, want the new volume", resp.Msg.Changes)
	}
	if !contains(r.Calls, "systemctl start systemd-cryptsetup@extra.service srv-extra.mount") {
		t.Errorf("the volume was not created: %v", r.Calls)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, extraConfig("ext4")) {
		t.Errorf("staged version = %q, want the delivered config's", v)
	}
	if v := slotVersion(t, s, runningImage); v != hashOf(t, baseConfig) {
		t.Errorf("running version = %q, want it unchanged", v)
	}
}

// TestApplyConfigRefusesAMachineChangeTheStagedImageWouldUndo refuses a volume added on the
// running image while the staged image's config lacks it, which booting that image would remove,
// and names apply-config --staged as the first step. Once the staged config adds the volume, as an
// additive change, the running image's config that names it applies.
func TestApplyConfigRefusesAMachineChangeTheStagedImageWouldUndo(t *testing.T) {
	s, r := stagedServer(t, fixtureSchemas(t))
	r.Rules = volumeRules(s)
	_, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), FallbackSecret: secret}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "would refuse its own config once this config changes the storage, saying: Volume extra: ") || !strings.Contains(err.Error(), "; bring its config along first: run chalkctl apply-config --staged n1 with the new config, then apply it to the running image") || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("err = %v, want a refusal naming apply-config --staged", err)
	}
	if changed := withoutChecks(r.Calls); len(changed) != 0 {
		t.Fatalf("a refused config ran %v", changed)
	}
	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), Staged: true, FallbackSecret: secret})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), FallbackSecret: secret})); err != nil {
		t.Fatal(err)
	}
	if v := slotVersion(t, s, runningImage); v != hashOf(t, extraConfig("ext4")) {
		t.Errorf("running version = %q, want the config with the volume", v)
	}
}

// TestApplyConfigWaitsForAnUpgrade refuses config while an upgrade runs, which writes the config
// slots too.
func TestApplyConfigWaitsForAnUpgrade(t *testing.T) {
	s, _ := configServer(t)
	s.upgrading.Lock()
	defer s.upgrading.Unlock()
	if _, err := sendConfig(s, baseConfig); connect.CodeOf(err) != connect.CodeAborted || !strings.Contains(err.Error(), "an upgrade, an image rollback or the rollback at a trial's deadline is running; apply the config once it ended") {
		t.Errorf("err = %v, want aborted", err)
	}
}

// TestGetConfigStaged returns the staged slot's documents.
func TestGetConfigStaged(t *testing.T) {
	s, _ := stagedServer(t, fixtureSchemas(t))
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	if err := sendStaged(s, next); err != nil {
		t.Fatal(err)
	}
	resp, err := s.GetConfig(context.Background(), connect.NewRequest(&nodev1.GetConfigRequest{Staged: true}))
	if err != nil || resp.Msg.Version != hashOf(t, next) || !strings.Contains(resp.Msg.Documents, `"name":"second"`) {
		t.Errorf("staged config = %+v, %v", resp, err)
	}
}

// TestUpgradeOfAStagedImageNamesApplyConfigStaged names apply-config --staged when the config an
// upgrade sends again for a staged image is refused in the recheck, which retires the image;
// apply-config --staged would have kept it. Any other failure names it not, also of the staged
// image: one that came before the recheck, such as of the image's architecture, retired nothing,
// and a recheck that could not mount the store retired the image but refused no config.
func TestUpgradeOfAStagedImageNamesApplyConfigStaged(t *testing.T) {
	c := newCreds(t)
	s, _ := stagedServer(t, fixtureSchemas(t))
	refusal := errors.New("the config does not suit the image")
	installErr := error(refusal)
	s.InstallImage = func(_ context.Context, _ upgrade.Header, _, _ string, r io.Reader) (upgrade.Result, error) {
		io.Copy(io.Discard, r)
		return upgrade.Result{}, installErr
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleAdmin])
	send := func(rootHash []byte) error {
		stream := conn.Upgrade(context.Background())
		header := &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{ImageId: "chalkos", RootHash: rootHash}, Config: baseConfig}
		if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: header}}); err != nil {
			t.Fatal(err)
		}
		_, err := stream.CloseAndReceive()
		return err
	}
	if err := send(bytes.Repeat([]byte{'9'}, 32)); err == nil || strings.Contains(err.Error(), "--staged") {
		t.Errorf("upgrade to another image = %v, want no --staged named", err)
	}
	installErr = errors.New("the image is built for arm64, this machine for x86-64")
	if err := send(stagedImage); err == nil || strings.Contains(err.Error(), "--staged") {
		t.Errorf("upgrade of the staged image that fails before the recheck = %v, want no --staged named", err)
	}
	installErr = &upgrade.StagedRetiredError{Err: &upgrade.CheckError{Err: refusal}}
	if err := send(stagedImage); err == nil || !strings.Contains(err.Error(), "the image staged to boot next was retired with it: chalkctl apply-config --staged") {
		t.Errorf("upgrade of the staged image = %v, want apply-config --staged named", err)
	}
	// A recheck that could not mount the store retired the image without refusing its config.
	installErr = &upgrade.StagedRetiredError{Err: errors.New("mount the written store: wrong fs type")}
	if err := send(stagedImage); err == nil || !strings.Contains(err.Error(), "the image staged to boot next was retired with it") || strings.Contains(err.Error(), "--staged") {
		t.Errorf("upgrade of the staged image whose store did not mount = %v, want it retired and no --staged named", err)
	}
}

// knownGoodServer is stagedServer after a rollback from a boot that was not found healthy: the
// running image booted from the counted chalkos_2+1-2.efi, and the image staged to boot next from
// the uncounted chalkos_3.efi is the one the node falls back to.
func knownGoodServer(t *testing.T) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := stagedServer(t, fixtureSchemas(t))
	for _, f := range []string{"chalkos_2.efi", "chalkos_3+3.efi"} {
		if err := os.Remove(filepath.Join(s.Paths.ESP, "EFI", "Linux", f)); err != nil {
			t.Fatal(err)
		}
	}
	writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", "chalkos_2+1-2.efi", "chalkos_3.efi")
	return s, r
}

// TestKnownGoodImageKeepsItsConfig refuses other config for the image a rollback staged as the
// node's known-good one, from apply-config --staged and from an upgrade, as it boots with the
// config it last ran; the same config, or an upgrade without config, changes nothing and keeps
// the record of the rollback.
func TestKnownGoodImageKeepsItsConfig(t *testing.T) {
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	t.Run("apply-config --staged", func(t *testing.T) {
		s, r := knownGoodServer(t)
		if err := sendStaged(s, next); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "is the one the node falls back to while the boot of chalkos_2+1-2.efi is not found healthy; its config stays as it last ran") {
			t.Errorf("other config = %v", err)
		}
		if err := sendStaged(s, baseConfig); err != nil {
			t.Errorf("the same config = %v", err)
		}
		if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
			t.Errorf("staged version = %q, want it unchanged", v)
		}
		if len(r.Calls) != 0 {
			t.Errorf("ran %v", r.Calls)
		}
	})
	t.Run("upgrade", func(t *testing.T) {
		c := newCreds(t)
		s, _ := knownGoodServer(t)
		record := filepath.Join(s.Paths.StateDir, "chalkd", "rollback")
		if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(record, []byte(hex.EncodeToString(stagedImage)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s.InstallImage = func(_ context.Context, _ upgrade.Header, _, _ string, r io.Reader) (upgrade.Result, error) {
			io.Copy(io.Discard, r)
			return upgrade.Result{AlreadyStaged: true, Entry: "chalkos_3.efi"}, nil
		}
		conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleAdmin])
		send := func(config string) error {
			stream := conn.Upgrade(context.Background())
			header := &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{ImageId: "chalkos", RootHash: stagedImage}, Config: config}
			if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: header}}); err != nil {
				t.Fatal(err)
			}
			_, err := stream.CloseAndReceive()
			return err
		}
		if err := send(next); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "its config stays as it last ran until that boot is found healthy") {
			t.Errorf("an upgrade with other config = %v", err)
		}
		for _, config := range []string{"", baseConfig} {
			if err := send(config); err != nil {
				t.Errorf("an upgrade with the config %q = %v", config, err)
			}
		}
		if data, err := os.ReadFile(record); err != nil || string(data) != hex.EncodeToString(stagedImage)+"\n" {
			t.Errorf("the record of the rollback = %q, %v; want it kept", data, err)
		}
	})
}
