package chalkd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/store"
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
func stagedServer(t *testing.T, schemas map[string][]byte) (*Server, *fakeRunner) {
	t.Helper()
	s, r := configServer(t)
	_, held, _, err := s.runningSlot()
	if err != nil {
		t.Fatal(err)
	}
	writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", "chalkos_2.efi", "chalkos_3+3.efi")
	for _, h := range [][]byte{runningImage, stagedImage} {
		if err := store.Write(store.Slot(s.Loader.Slots, h), held); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Retire(s.Loader.Slots, [][]byte{runningImage, stagedImage}); err != nil {
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
	v, err := store.Version(store.Slot(s.Loader.Slots, rootHash))
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
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
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
		"a share":               {fixtureSchemas(t), &nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true, KubernetesShare: []byte("{}")}, false, connect.CodeInvalidArgument, "delivers config alone"},
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
			if len(r.calls) != 0 {
				t.Errorf("ran %v", r.calls)
			}
		})
	}
}

// TestApplyConfigStagedAddsAVolume makes the additive change a staged config makes to the
// machine at once, while the running image runs, with the fallback secret, and says so.
func TestApplyConfigStagedAddsAVolume(t *testing.T) {
	s, r := stagedServer(t, fixtureSchemas(t))
	r.rules = volumeRules(s)
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), Staged: true, FallbackSecret: secret}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Changes) != 1 || resp.Msg.Changes[0].Volume != "extra" {
		t.Errorf("changes = %v, want the new volume", resp.Msg.Changes)
	}
	if !contains(r.calls, "systemctl start systemd-cryptsetup@extra.service srv-extra.mount") {
		t.Errorf("the volume was not created: %v", r.calls)
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
	r.rules = volumeRules(s)
	_, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), FallbackSecret: secret}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "first run chalkctl apply-config --staged") || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("err = %v, want a refusal naming apply-config --staged", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("a refused config ran %v", r.calls)
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
	if _, err := sendConfig(s, baseConfig); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("err = %v, want aborted", err)
	}
}
