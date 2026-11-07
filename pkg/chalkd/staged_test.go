package chalkd

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storage"
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
	return stagedServerOf(t, schemas, "chalkos_2.efi", "chalkos_3+3.efi")
}

// stagedServerOf is stagedServer with the staged image's UKI under the name staged, its entry's
// while it is active or .staged-chalkos_3.efi while it is staged alone, and the node booted from
// the entry booted of the running image.
func stagedServerOf(t *testing.T, schemas map[string][]byte, booted, staged string) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := configServer(t)
	_, held, _, err := s.runningSlot()
	if err != nil {
		t.Fatal(err)
	}
	// The boot loader names the entry by its ID, the name without the tries.
	id, _, _ := strings.Cut(booted, "+")
	writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, strings.TrimSuffix(id, ".efi")+".efi", booted, staged)
	for _, h := range [][]byte{runningImage, stagedImage} {
		if err := slot.Write(slot.Dir(s.Loader.Slots, h), held); err != nil {
			t.Fatal(err)
		}
	}
	if err := slot.Retire(s.Loader.Slots, [][]byte{runningImage, stagedImage}); err != nil {
		t.Fatal(err)
	}
	img := imageWith(t, s, schemas, "")
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
// alone: the running slot stays, and nothing restarts. An image staged and not active yet is the
// staged image as one that boots next is, and so is it to GetConfig and Status, whose boot names
// its state. Neither is the known-good image, not even while the running image's boot was not
// found healthy, as a staged UKI carries no tries.
func TestApplyConfigStaged(t *testing.T) {
	for _, tc := range []struct {
		booted, staged string
		state          nodev1.SlotState
	}{
		{"chalkos_2.efi", "chalkos_3+3.efi", nodev1.SlotState_SLOT_STATE_ACTIVE},
		{"chalkos_2.efi", ".staged-chalkos_3.efi", nodev1.SlotState_SLOT_STATE_STAGED},
		{"chalkos_2+2-1.efi", ".staged-chalkos_3.efi", nodev1.SlotState_SLOT_STATE_STAGED},
	} {
		t.Run(tc.booted+" "+tc.staged, func(t *testing.T) {
			s, r := stagedServerOf(t, fixtureSchemas(t), tc.booted, tc.staged)
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
			got := status(t, s)
			if got.StagedConfigVersion != hashOf(t, next) || got.ConfigVersion != hashOf(t, baseConfig) {
				t.Errorf("status names the versions %q and staged %q", got.ConfigVersion, got.StagedConfigVersion)
			}
			if !bytes.Equal(got.Boot.GetStaged().GetRootHash(), stagedImage) || got.Boot.GetStagedState() != tc.state || got.Boot.GetStagedKnownGood() {
				t.Errorf("status names the staged image %x, %v, known-good %v", got.Boot.GetStaged().GetRootHash(), got.Boot.GetStagedState(), got.Boot.GetStagedKnownGood())
			}
			resp, err := s.GetConfig(context.Background(), connect.NewRequest(&nodev1.GetConfigRequest{Staged: true}))
			if err != nil || resp.Msg.Version != hashOf(t, next) {
				t.Errorf("GetConfig of the staged image = %v, %v", resp, err)
			}
		})
	}
}

// TestRecordImagesKeepsTheStagedSlot forgets the config slots of images no UKI boots, but keeps
// the slot of the image staged and not active yet, whose activation boots it with that slot.
func TestRecordImagesKeepsTheStagedSlot(t *testing.T) {
	s, _ := stagedServerOf(t, fixtureSchemas(t), "chalkos_2.efi", ".staged-chalkos_3.efi")
	s.recordImages(nil)
	if !s.keepsSlot(stagedImage) || !s.keepsSlot(runningImage) {
		t.Errorf("the slots kept: staged %v, running %v", s.keepsSlot(stagedImage), s.keepsSlot(runningImage))
	}
	if err := os.Remove(filepath.Join(s.Paths.ESP, "EFI", "Linux", ".staged-chalkos_3.efi")); err != nil {
		t.Fatal(err)
	}
	s.recordImages(nil)
	if s.keepsSlot(stagedImage) {
		t.Error("the slot of an image no UKI boots stayed")
	}
}

// TestApplyConfigStagedRefuses refuses a staged config the staged image does not accept, one that
// changes the machine's storage destructively, and one that comes with node state, and keeps the
// staged slot as it was.
func TestApplyConfigStagedRefuses(t *testing.T) {
	withoutDemo := fixtureSchemas(t)
	delete(withoutDemo, "demo.example.org_v1alpha1_Demo.json")
	for name, tc := range map[string]struct {
		schemas map[string][]byte
		req     *nodev1.ApplyConfigRequest
		code    connect.Code
		want    string
	}{
		"an unknown kind":       {withoutDemo, &nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true}, connect.CodeInvalidArgument, `does not accept kind "Demo"`},
		"a VAR of another size": {fixtureSchemas(t), &nodev1.ApplyConfigRequest{Config: strings.Replace(baseConfig, "{disk: {path: /dev/vda}}", "{disk: {path: /dev/vda}, var: {size: 1G}}", 1), Staged: true}, connect.CodeFailedPrecondition, "only a reinstall of"},
		"no config":             {fixtureSchemas(t), &nodev1.ApplyConfigRequest{Staged: true}, connect.CodeInvalidArgument, "delivers config alone"},
		"a share":               {fixtureSchemas(t), &nodev1.ApplyConfigRequest{Config: baseConfig, Staged: true, Layers: shareLayers(t, []byte("{}"))}, connect.CodeInvalidArgument, "delivers config alone"},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := stagedServer(t, tc.schemas)
			_, err := s.ApplyConfig(context.Background(), connect.NewRequest(tc.req))
			if codeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) {
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

// TestApplyConfigCoversTheStagedImage delivers config to the running image and, once that image
// applied it, to the staged image's slot, in either state, which the staged image's checker passed:
// both slots run the config, and the answer names the staged slot's versions.
func TestApplyConfigCoversTheStagedImage(t *testing.T) {
	for _, staged := range []string{"chalkos_3+3.efi", ".staged-chalkos_3.efi"} {
		t.Run(staged, func(t *testing.T) {
			s, r := stagedServerOf(t, fixtureSchemas(t), "chalkos_2.efi", staged)
			next := strings.Replace(baseConfig, "name: first", "name: second", 1)
			resp, err := sendConfig(s, next)
			if err != nil {
				t.Fatal(err)
			}
			for name, image := range map[string][]byte{"running": runningImage, "staged": stagedImage} {
				if v := slotVersion(t, s, image); v != hashOf(t, next) {
					t.Errorf("%s version = %q, want the delivered config's", name, v)
				}
			}
			if got := resp.Staged; got.GetVersion() != hashOf(t, next) || got.GetReplaces() != hashOf(t, baseConfig) {
				t.Errorf("the answer names the staged slot %v", got)
			}
			if !contains(r.Calls, "systemctl try-restart demo.service") {
				t.Errorf("the running image did not apply the config: %v", r.Calls)
			}
		})
	}
}

// TestApplyConfigRefusedByEitherImage refuses config that either image refuses before anything
// changes: the slots of both images stay, nothing runs and no baseline of the effects stays. The
// staged image's refusal names it.
func TestApplyConfigRefusedByEitherImage(t *testing.T) {
	withoutDemo := fixtureSchemas(t)
	delete(withoutDemo, "demo.example.org_v1alpha1_Demo.json")
	for name, tc := range map[string]struct {
		schemas map[string][]byte
		config  string
		code    connect.Code
		want    string
	}{
		"by the staged image":  {withoutDemo, strings.Replace(baseConfig, "name: first", "name: second", 1), connect.CodeInvalidArgument, `the image 333333333333 staged in the inactive slot: the config does not suit the image: `},
		"by the running image": {fixtureSchemas(t), strings.Replace(baseConfig, "{disk: {path: /dev/vda}}", "{disk: {path: /dev/vda}, var: {size: 1G}}", 1), connect.CodeFailedPrecondition, "only a reinstall of"},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := stagedServer(t, tc.schemas)
			_, err := sendConfig(s, tc.config)
			if codeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %v naming %q", err, tc.code, tc.want)
			}
			for name, image := range map[string][]byte{"running": runningImage, "staged": stagedImage} {
				if v := slotVersion(t, s, image); v != hashOf(t, baseConfig) {
					t.Errorf("%s version = %q, want it unchanged", name, v)
				}
			}
			if changed := withoutChecks(r.Calls); len(changed) != 0 {
				t.Errorf("a refused config ran %v", changed)
			}
			if _, err := os.Stat(s.Paths.EffectsBaseline); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("baseline after a refusal: %v", err)
			}
		})
	}
}

// TestStagedRefusalNamesTheFallbackSecret names the image in a refusal of the staged image, with
// the refusal's code, and, when the staged image's storage needs the fallback secret the request
// does not carry, apply-config --staged, which sends it for the staged image's storage.
func TestStagedRefusalNamesTheFallbackSecret(t *testing.T) {
	s, _ := stagedServer(t, fixtureSchemas(t))
	refusal := layer.Errorf(connect.CodeInvalidArgument, "volume extra is encrypted and the node's fallback is recovery-key, but %s", storage.NoFallbackSecret)
	err := s.stagedRefusal(stagedImage, refusal, "")
	want := "the image 333333333333 staged in the inactive slot: volume extra is encrypted and the node's fallback is recovery-key, but no fallback secret was sent; the staged image's storage enrolls the fallback secret on a volume the running image's does not, and chalkctl sends the secret for the running image's storage alone: chalkctl apply-config --staged n1 --secrets FILE delivers the config to the staged image with the secret"
	if codeOf(err) != connect.CodeInvalidArgument || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("err = %v, want %q", err, want)
	}
	for name, tc := range map[string]struct {
		err    error
		secret string
	}{
		"with a secret": {refusal, secret},
		"other":         {layer.Errorf(connect.CodeFailedPrecondition, "the config does not suit the image"), ""},
	} {
		err := s.stagedRefusal(stagedImage, tc.err, tc.secret)
		if codeOf(err) != codeOf(tc.err) || strings.Contains(err.Error(), "--staged") || !strings.Contains(err.Error(), "the image 333333333333 staged in the inactive slot: ") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// TestApplyConfigAddsAVolumeForBothImages adds a volume the config names to the machine once, as
// the running image applies the config, and writes the staged image's slot after: that image
// checked its config against the storage the running image records by then, which the new
// volume is in, so its slot adds nothing more.
func TestApplyConfigAddsAVolumeForBothImages(t *testing.T) {
	s, r := stagedServer(t, fixtureSchemas(t))
	r.Rules = volumeRules(s)
	m, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), FallbackSecret: secret}))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Msg.Changes; len(got) != 1 || got[0].Volume != "extra" {
		t.Errorf("changes = %v, want the new volume once", got)
	}
	for name, image := range map[string][]byte{"running": runningImage, "staged": stagedImage} {
		if v := slotVersion(t, s, image); v != hashOf(t, extraConfig("ext4")) {
			t.Errorf("%s version = %q, want the config with the volume", name, v)
		}
	}
}

// TestApplyConfigNamesAStagedSlotItCouldNotWrite fails a delivery whose write of the staged
// image's slot fails once the running image applied it, here as something that is no config link
// stands where the slot's previous config would be: the running image runs the config, the staged
// slot keeps its earlier one, and the error says so, with the units the running image restarted. Delivered again, the config goes to the staged
// slot.
func TestApplyConfigNamesAStagedSlotItCouldNotWrite(t *testing.T) {
	s, _ := stagedServer(t, fixtureSchemas(t))
	blocker := slot.Previous(slot.Dir(s.Loader.Slots, stagedImage))
	write(t, blocker, "")
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	_, err := sendConfig(s, next)
	want := "the node runs config " + hashOf(t, next) + " (restarted demo.service), but writing the config slot of the image 333333333333 staged in the inactive slot failed, so it keeps config " + hashOf(t, baseConfig) + ": "
	if codeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), want) || !strings.HasSuffix(err.Error(), "; deliver the config again to write it") {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if v := slotVersion(t, s, runningImage); v != hashOf(t, next) {
		t.Errorf("running version = %q, want the delivered config's", v)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
		t.Errorf("staged version = %q, want it kept", v)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, next) {
		t.Errorf("staged version after the second delivery = %q, want the delivered config's", v)
	}
}

// TestStagedSlotOfItsOwnVolumeIsWrittenAfterIt makes the storage change the staged image's config
// makes on top of the running image's, a volume of its own here, before it writes that image's
// slot, and records it in storage.json first. A slot that cannot be written then leaves the volume
// made and recorded, and the error says so with what the running image's delivery did; written
// again, the slot takes the config and the volume is not made again.
func TestStagedSlotOfItsOwnVolumeIsWrittenAfterIt(t *testing.T) {
	s, r := stagedServer(t, fixtureSchemas(t))
	r.Rules = volumeRules(s)
	schemas, err := schema.LoadSchemas(s.Loader.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	next, err := configure("n1", extraConfig("ext4"), schemas, config.Target{Platform: "metal"})
	if err != nil {
		t.Fatal(err)
	}
	var id identity.Identity
	if err := json.Unmarshal(next.Identity, &id); err != nil {
		t.Fatal(err)
	}
	w := secondWrite{image: stagedImage, held: hashOf(t, baseConfig), slot: slotWrite{next: next, machine: machineChange{section: id.Storage}}}
	blocker := slot.Previous(slot.Dir(s.Loader.Slots, stagedImage))
	write(t, blocker, "")
	resp := &nodev1.ApplyConfigResponse{RestartedUnits: []string{"demo.service"}, Warnings: []string{"careful"}}
	err = w.write(context.Background(), s, "v1", secret, resp)
	want := "the node runs config v1 (volume extra: new volume on new disk extra; restarted demo.service; warning: careful), but writing the config slot of the image 333333333333 staged in the inactive slot failed, so it keeps config " + hashOf(t, baseConfig) + ": "
	if codeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if !contains(r.Calls, "systemctl start systemd-cryptsetup@extra.service srv-extra.mount") {
		t.Errorf("the volume was not created: %v", r.Calls)
	}
	recorded, _, err := s.recorded()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recorded.Volumes["extra"]; !ok {
		t.Errorf("storage.json records %v, want the new volume", recorded.Volumes)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
		t.Errorf("staged version = %q, want it kept", v)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	resp = &nodev1.ApplyConfigResponse{}
	if err := w.write(context.Background(), s, "v1", secret, resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Changes) != 0 || resp.Staged.GetVersion() != next.Version {
		t.Errorf("written again: changes %v, staged %v; want no change and the slot", resp.Changes, resp.Staged)
	}
	if v := slotVersion(t, s, stagedImage); v != next.Version {
		t.Errorf("staged version = %q, want the config with the volume", v)
	}
}

// TestStagedStorageChangeFailingPartWay names the volume a storage change of the staged image's
// config recorded before it failed, here as its units do not start, in the error of the delivery,
// and leaves the slot as it was; one that fails before it records anything names none.
func TestStagedStorageChangeFailingPartWay(t *testing.T) {
	for name, tc := range map[string]struct {
		fail string
		want string
	}{
		"after the record":  {"systemctl start systemd-cryptsetup@extra.service", "the node runs config v1 (volume extra: new volume on new disk extra), but "},
		"before the record": {"systemd-repart --dry-run=no", "the node runs config v1, but "},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := stagedServer(t, fixtureSchemas(t))
			r.Rules = append([]tooltest.Rule{{Prefix: tc.fail, Err: errors.New("exit status 1")}}, volumeRules(s)...)
			schemas, err := schema.LoadSchemas(s.Loader.Schemas)
			if err != nil {
				t.Fatal(err)
			}
			next, err := configure("n1", extraConfig("ext4"), schemas, config.Target{Platform: "metal"})
			if err != nil {
				t.Fatal(err)
			}
			var id identity.Identity
			if err := json.Unmarshal(next.Identity, &id); err != nil {
				t.Fatal(err)
			}
			w := secondWrite{image: stagedImage, held: hashOf(t, baseConfig), slot: slotWrite{next: next, machine: machineChange{section: id.Storage}}}
			err = w.write(context.Background(), s, "v1", secret, &nodev1.ApplyConfigResponse{})
			if err == nil || !strings.Contains(err.Error(), tc.want+"writing the config slot of the image 333333333333 staged in the inactive slot failed") {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
				t.Errorf("staged version = %q, want it kept", v)
			}
		})
	}
}

// TestDryRunPlansTheStagedSlot has a dry run check the config with both images and name the slot
// the staged image's checker passed, with the storage its config adds, and change nothing; the
// staged image's refusal refuses it.
func TestDryRunPlansTheStagedSlot(t *testing.T) {
	s, r := stagedServer(t, fixtureSchemas(t))
	r.Rules = volumeRules(s)
	m, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: extraConfig("ext4"), FallbackSecret: secret, DryRun: true}))
	if err != nil {
		t.Fatal(err)
	}
	resp := m.Msg
	if got := resp.Staged; got.GetVersion() != hashOf(t, extraConfig("ext4")) || got.GetReplaces() != hashOf(t, baseConfig) {
		t.Errorf("the plan names the staged slot %v", got)
	}
	if len(resp.Changes) != 1 || resp.Changes[0].Volume != "extra" {
		t.Errorf("changes = %v, want the running image's new volume alone", resp.Changes)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
		t.Errorf("staged version = %q, want it unchanged", v)
	}
	if changed := withoutChecks(r.Calls); len(changed) != 0 {
		t.Errorf("a dry run ran %v", changed)
	}
	withoutDemo := fixtureSchemas(t)
	delete(withoutDemo, "demo.example.org_v1alpha1_Demo.json")
	s, _ = stagedServer(t, withoutDemo)
	if _, err := dryRun(s, baseConfig); codeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "the image 333333333333 staged in the inactive slot: ") {
		t.Errorf("a dry run the staged image refuses = %v", err)
	}
}

// TestApplyConfigLeavesTheKnownGoodImage leaves the known-good image a rollback staged with the
// config it last ran, without its check, and says so; Status names the image known-good, so a
// client advises no delivery for it.
func TestApplyConfigLeavesTheKnownGoodImage(t *testing.T) {
	s, _ := knownGoodServer(t)
	if b := status(t, s).Boot; !bytes.Equal(b.GetStaged().GetRootHash(), stagedImage) || !b.GetStagedKnownGood() {
		t.Errorf("status names the staged image %x, known-good %v", b.GetStaged().GetRootHash(), b.GetStagedKnownGood())
	}
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	resp, err := sendConfig(s, next)
	if err != nil {
		t.Fatal(err)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
		t.Errorf("known-good version = %q, want it kept", v)
	}
	if resp.Staged != nil || len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "is the one the node falls back to while the boot of chalkos_2+1-2.efi is not found healthy; its config stays as it last ran") {
		t.Errorf("answer = %v", resp)
	}
}

// TestTrialWritesTheStagedSlotOnceConfirmed checks config on trial with the staged image too, and
// writes that image's slot from the copy the trial keeps only once the trial is confirmed; a trial
// rolled back leaves it as it was. Either way the copy goes with the trial.
func TestTrialWritesTheStagedSlotOnceConfirmed(t *testing.T) {
	copyOf := func(s *Server) string { return slotVersionAt(t, filepath.Join(s.Paths.StateDir, trialStagedSlot)) }
	for _, confirm := range []bool{true, false} {
		t.Run(fmt.Sprintf("confirmed %v", confirm), func(t *testing.T) {
			s, _ := stagedServer(t, fixtureSchemas(t))
			stopTrials(t, s)
			resp, err := sendTrial(s, nextConfig, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if got := resp.Staged; got.GetVersion() != hashOf(t, nextConfig) || got.GetReplaces() != hashOf(t, baseConfig) {
				t.Errorf("the answer names the staged slot %v", got)
			}
			if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
				t.Errorf("staged version on trial = %q, want it kept", v)
			}
			if v := copyOf(s); v != hashOf(t, nextConfig) {
				t.Errorf("the trial's copy holds %q", v)
			}
			want := hashOf(t, baseConfig)
			if confirm {
				m, err := s.ConfirmConfig(context.Background(), connect.NewRequest(&nodev1.ConfirmConfigRequest{Version: hashOf(t, nextConfig)}))
				if err != nil || len(m.Msg.Warnings) != 0 {
					t.Fatalf("confirm = %v, %v", m, err)
				}
				want = hashOf(t, nextConfig)
			} else if _, err := s.RollbackConfig(context.Background(), connect.NewRequest(&nodev1.RollbackConfigRequest{Version: hashOf(t, baseConfig)})); err != nil {
				t.Fatal(err)
			}
			if v := slotVersion(t, s, stagedImage); v != want {
				t.Errorf("staged version = %q, want %q", v, want)
			}
			if v := copyOf(s); v != "" {
				t.Errorf("the trial's copy stayed, holding %q", v)
			}
		})
	}
}

// stagedTrial is stagedServer with config nextConfig on trial for an hour, whose staged slot the
// trial holds back in its copy.
func stagedTrial(t *testing.T) *Server {
	t.Helper()
	s, _ := stagedServer(t, fixtureSchemas(t))
	stopTrials(t, s)
	if _, err := sendTrial(s, nextConfig, time.Hour); err != nil {
		t.Fatal(err)
	}
	return s
}

// trialCopy is the version the copy of the staged image's slot that a trial keeps holds; empty
// without one.
func trialCopy(t *testing.T, s *Server) string {
	t.Helper()
	return slotVersionAt(t, filepath.Join(s.Paths.StateDir, trialStagedSlot))
}

// TestConfirmConfigKeepsTheStagedSlotWhileTheTrialStays writes the staged slot only once the
// trial's record is gone: a confirmation that cannot remove it fails and writes nothing, so the
// rollback at the deadline or on request leaves both slots as they were before the trial.
func TestConfirmConfigKeepsTheStagedSlotWhileTheTrialStays(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s := stagedTrial(t)
	dir := filepath.Dir(filepath.Join(s.Paths.StateDir, trialFile))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if err := sendConfirm(s, hashOf(t, nextConfig)); codeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "end the trial: ") {
		t.Fatalf("confirm = %v, want the record's removal failed", err)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
		t.Errorf("staged version = %q, want it kept while the trial stays", v)
	}
	if !trialOpenOn(t, s) || trialCopy(t, s) != hashOf(t, nextConfig) {
		t.Errorf("the trial is open %v with the copy %q, want both kept", trialOpenOn(t, s), trialCopy(t, s))
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := sendRollback(s, hashOf(t, baseConfig)); err != nil {
		t.Fatal(err)
	}
	if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) || trialCopy(t, s) != "" {
		t.Errorf("after the rollback the staged slot holds %q and the copy %q", v, trialCopy(t, s))
	}
}

// TestALeftoverTrialCopyIsNeverWritten leaves the copy of the staged slot as a crash does after a
// confirmation removed the trial's record: nothing writes it, a confirmation sent again finds the
// node on the config and warns that the staged slot was not written, the boot and chalkd's start
// remove it, and the config delivered again writes the staged slot.
func TestALeftoverTrialCopyIsNeverWritten(t *testing.T) {
	confirmAgain := func(t *testing.T, s *Server) []string {
		t.Helper()
		m, err := s.ConfirmConfig(context.Background(), connect.NewRequest(&nodev1.ConfirmConfigRequest{Version: hashOf(t, nextConfig)}))
		if err != nil {
			t.Fatalf("a confirmation sent again = %v", err)
		}
		return m.Msg.Warnings
	}
	for name, remove := range map[string]func(*Server) error{
		"at boot": func(s *Server) error { return RestoreTrialAtBoot(s.Paths.StateDir, s.Loader.Slots) },
		"as chalkd starts": func(s *Server) error {
			s.ResumeTrial(t.Context())
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := stagedTrial(t)
			if err := removeTrialRecord(s.Paths.StateDir); err != nil {
				t.Fatal(err)
			}
			want := "config " + hashOf(t, nextConfig) + " is confirmed, but the confirmation that ended its trial stopped before it wrote the config slot of the image 333333333333 staged in the inactive slot, which keeps config " + hashOf(t, baseConfig) + "; deliver the config again to write it"
			if w := confirmAgain(t, s); len(w) != 1 || w[0] != want {
				t.Errorf("warnings = %q, want %q", w, want)
			}
			if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
				t.Errorf("staged version = %q, want the copy left unwritten", v)
			}
			if err := remove(s); err != nil {
				t.Fatal(err)
			}
			if v := trialCopy(t, s); v != "" {
				t.Errorf("the copy stayed, holding %q", v)
			}
			if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
				t.Errorf("staged version = %q, want the copy left unwritten", v)
			}
			if _, err := sendConfig(s, nextConfig); err != nil {
				t.Fatal(err)
			}
			if v := slotVersion(t, s, stagedImage); v != hashOf(t, nextConfig) {
				t.Errorf("staged version after the delivery = %q, want the config's", v)
			}
			// A copy whose slot was written, as a removal that failed after the write leaves it, says
			// nothing.
			held, _, err := slot.Read(slot.Dir(s.Loader.Slots, stagedImage))
			if err != nil {
				t.Fatal(err)
			}
			if err := slot.Write(filepath.Join(s.Paths.StateDir, trialStagedSlot), held); err != nil {
				t.Fatal(err)
			}
			if w := confirmAgain(t, s); len(w) != 0 {
				t.Errorf("warnings of a copy that was written = %q", w)
			}
		})
	}
}

// TestConfirmConfigWarnsOfAHeldBackWriteThatFailed confirms a trial whose staged slot or pending
// config cannot be written, as something that is no config link stands where the slot's previous
// config would be, and warns of it: the trial ends, the slot keeps what it held, and the copy
// goes.
func TestConfirmConfigWarnsOfAHeldBackWriteThatFailed(t *testing.T) {
	confirm := func(t *testing.T, s *Server) []string {
		t.Helper()
		m, err := s.ConfirmConfig(context.Background(), connect.NewRequest(&nodev1.ConfirmConfigRequest{Version: hashOf(t, nextConfig)}))
		if err != nil {
			t.Fatal(err)
		}
		if trialOpenOn(t, s) || trialCopy(t, s) != "" {
			t.Errorf("the trial is open %v with the copy %q, want both gone", trialOpenOn(t, s), trialCopy(t, s))
		}
		return m.Msg.Warnings
	}
	t.Run("staged slot", func(t *testing.T) {
		s := stagedTrial(t)
		write(t, slot.Previous(slot.Dir(s.Loader.Slots, stagedImage)), "")
		want := "config " + hashOf(t, nextConfig) + " is confirmed, but writing the config slot of the image 333333333333 staged in the inactive slot failed, so it keeps its earlier config: "
		if w := confirm(t, s); len(w) != 1 || !strings.HasPrefix(w[0], want) || !strings.HasSuffix(w[0], "; deliver the config again to write it") {
			t.Errorf("warnings = %q, want %q", w, want)
		}
		if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
			t.Errorf("staged version = %q, want it kept", v)
		}
	})
	t.Run("pending config", func(t *testing.T) {
		s := trialServer(t)
		p := sendPending(t, s, baseConfig)
		if _, err := sendTrial(s, nextConfig, time.Hour); err != nil {
			t.Fatal(err)
		}
		write(t, slot.Previous(slot.Pending(s.Loader.Slots)), "")
		want := "config " + hashOf(t, nextConfig) + " is confirmed, but replacing the pending config with it failed, so the pending config stays: "
		if w := confirm(t, s); len(w) != 1 || !strings.HasPrefix(w[0], want) {
			t.Errorf("warnings = %q, want %q", w, want)
		}
		if v := pendingVersion(t, s); v != p.Version {
			t.Errorf("pending version = %q, want it kept", v)
		}
	})
}

// TestConfirmConfigDropsTheCopyOfAnImageNoLongerStaged writes the trial's copy only to the slot of
// the image still staged in the inactive slot, and not the running one, and not the known-good
// one: after a boot into the staged image whose rollback of the trial failed, the record names the
// running image, whose slot never got the config; once the staged image's slot is retired it
// names none; and after a fallback that left the staged image as the one the node falls back to,
// it names the known-good image, which keeps the config it last ran. Each time the confirmation
// warns, the copy goes, and no slot takes it.
func TestConfirmConfigDropsTheCopyOfAnImageNoLongerStaged(t *testing.T) {
	version := hashOf(t, nextConfig)
	for name, tc := range map[string]struct {
		change func(*Server) error
		// kept is whether STATE still keeps the staged image's slot, which then holds the config
		// from before the trial.
		kept bool
		want string
	}{
		"booted": {func(s *Server) error {
			writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_3.efi")
			return nil
		}, true, "config " + version + " is confirmed, but the image 333333333333, staged in the inactive slot when the trial began, is the running image now, and its config slot never got the config; deliver the config again to write it"},
		"retired": {func(s *Server) error { return slot.Retire(s.Loader.Slots, [][]byte{runningImage}) }, false,
			"config " + version + " is confirmed, but the image 333333333333 is no longer staged in the inactive slot, so the config it checked for its slot is dropped"},
		"known-good": {func(s *Server) error {
			for _, f := range []string{"chalkos_2.efi", "chalkos_3+3.efi"} {
				if err := os.Remove(filepath.Join(s.Paths.ESP, "EFI", "Linux", f)); err != nil {
					return err
				}
			}
			writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", "chalkos_2+1-2.efi", "chalkos_3.efi")
			return nil
		}, true, "config " + version + " is confirmed, but the image 333333333333 staged in the inactive slot is the one the node falls back to now while the boot it runs is not found healthy, so its config stays as it last ran and the config it checked for its slot is dropped"},
	} {
		t.Run(name, func(t *testing.T) {
			s := stagedTrial(t)
			if err := tc.change(s); err != nil {
				t.Fatal(err)
			}
			m, err := s.ConfirmConfig(context.Background(), connect.NewRequest(&nodev1.ConfirmConfigRequest{Version: version}))
			if err != nil {
				t.Fatal(err)
			}
			if w := m.Msg.Warnings; len(w) != 1 || w[0] != tc.want {
				t.Errorf("warnings = %q, want %q", w, tc.want)
			}
			held := ""
			if tc.kept {
				held = hashOf(t, baseConfig)
			}
			if v := slotVersion(t, s, stagedImage); v != held {
				t.Errorf("the slot of the image no longer staged holds %q, want %q", v, held)
			}
			if trialOpenOn(t, s) || trialCopy(t, s) != "" {
				t.Errorf("the trial is open %v with the copy %q, want both gone", trialOpenOn(t, s), trialCopy(t, s))
			}
		})
	}
}

// TestEveryEndOfATrialDropsItsCopy removes the copy of the staged slot a trial kept, and writes
// nothing to the staged slot, at every end of the trial besides its confirmation: a rollback on
// request, a boot, and the deadline that finds the slot running the config from before the trial.
func TestEveryEndOfATrialDropsItsCopy(t *testing.T) {
	for name, end := range map[string]func(*Server) error{
		"rollback": func(s *Server) error {
			_, err := sendRollback(s, hashOf(t, baseConfig))
			return err
		},
		"boot": func(s *Server) error { return RestoreTrialAtBoot(s.Paths.StateDir, s.Loader.Slots) },
		"ended at the deadline": func(s *Server) error {
			r, _, err := readTrial(s.Paths.StateDir)
			if err != nil {
				return err
			}
			if err := slot.RestorePrevious(slotOf(t, s)); err != nil {
				return err
			}
			expire(s, r)
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := stagedTrial(t)
			if err := end(s); err != nil {
				t.Fatal(err)
			}
			if trialOpenOn(t, s) || trialCopy(t, s) != "" {
				t.Errorf("the trial is open %v with the copy %q, want both gone", trialOpenOn(t, s), trialCopy(t, s))
			}
			if v := slotVersion(t, s, stagedImage); v != hashOf(t, baseConfig) {
				t.Errorf("staged version = %q, want it kept", v)
			}
		})
	}
}

// TestTrialDeadlineStartsAfterTheStagedCheck counts a trial's time from its record on: the staged
// image's check, which may take its time, comes before it.
func TestTrialDeadlineStartsAfterTheStagedCheck(t *testing.T) {
	s, _ := stagedServer(t, fixtureSchemas(t))
	stopTrials(t, s)
	read := s.ReadImage
	var checked time.Time
	s.ReadImage = func(ctx context.Context, rootHash []byte, f func(imageStore, string) error) error {
		err := read(ctx, rootHash, f)
		time.Sleep(10 * time.Millisecond)
		checked = time.Now()
		return err
	}
	if _, err := sendTrial(s, nextConfig, time.Hour); err != nil {
		t.Fatal(err)
	}
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil || !open {
		t.Fatalf("no trial recorded: %v", err)
	}
	if r.Deadline.Before(checked.Add(time.Hour)) {
		t.Errorf("the deadline %v comes before an hour after the staged check ended, %v", r.Deadline, checked)
	}
}

// TestCheckSecondTrial refuses a trial whose config adds storage for the staged image on top of
// what the running image's config adds, which a rollback cannot undo, as the image's own
// translation of the config may.
func TestCheckSecondTrial(t *testing.T) {
	if err := checkSecondTrial(secondWrite{image: stagedImage}); err != nil {
		t.Errorf("a staged slot without storage changes = %v", err)
	}
	w := secondWrite{image: stagedImage, slot: slotWrite{machine: machineChange{changes: []storage.Change{{Volume: "extra", Reason: "new volume"}}}}}
	if err := checkSecondTrial(w); codeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "for the image 333333333333 staged in the inactive slot, volume extra: new volume, which a rollback cannot undo") {
		t.Errorf("err = %v", err)
	}
}

// slotVersionAt is the version of the slot dir; empty without one.
func slotVersionAt(t *testing.T, dir string) string {
	t.Helper()
	v, err := slot.Version(dir)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// stopTrials stops s's trial deadlines with the test.
func stopTrials(t *testing.T, s *Server) {
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.trialTimer != nil {
			s.trialTimer.Stop()
		}
	})
}

// TestApplyConfigWaitsForAnUpgrade refuses config while an upgrade runs, which writes the config
// slots too.
func TestApplyConfigWaitsForAnUpgrade(t *testing.T) {
	s, _ := configServer(t)
	s.upgrading.Lock()
	defer s.upgrading.Unlock()
	if _, err := sendConfig(s, baseConfig); codeOf(err) != connect.CodeAborted || !strings.Contains(err.Error(), "an upgrade, an image rollback or the rollback at a trial's deadline is running; apply the config once it ended") {
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
	s.InstallImage = func(_ context.Context, _ upgrade.Header, _, _ string, _ bool, r io.Reader) (upgrade.Result, imageConfig, error) {
		io.Copy(io.Discard, r)
		return upgrade.Result{}, imageConfig{}, installErr
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
	if err := send(stagedImage); err == nil || !strings.Contains(err.Error(), "the staged image was retired with it: chalkctl apply-config --staged") {
		t.Errorf("upgrade of the staged image = %v, want apply-config --staged named", err)
	}
	// A recheck that could not mount the store retired the image without refusing its config.
	installErr = &upgrade.StagedRetiredError{Err: errors.New("mount the written store: wrong fs type")}
	if err := send(stagedImage); err == nil || !strings.Contains(err.Error(), "the staged image was retired with it") || strings.Contains(err.Error(), "--staged") {
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
		if err := sendStaged(s, next); codeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "is the one the node falls back to while the boot of chalkos_2+1-2.efi is not found healthy; its config stays as it last ran") {
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
		s.InstallImage = func(_ context.Context, _ upgrade.Header, _, _ string, _ bool, r io.Reader) (upgrade.Result, imageConfig, error) {
			io.Copy(io.Discard, r)
			return upgrade.Result{AlreadyStaged: true, Entry: "chalkos_3.efi"}, imageConfig{}, nil
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
		if err := send(next); codeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "its config stays as it last ran until that boot is found healthy") {
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
