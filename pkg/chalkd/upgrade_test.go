package chalkd

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/identity"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// TestUpgrade streams an image to the node: the header and the chunks reach the installation,
// which may answer that the node runs the image already or boots it next already, and the node
// reboots when asked to into an image it does not run. The node records the image's provenance.
func TestUpgrade(t *testing.T) {
	c := newCreds(t)
	image := bytes.Repeat([]byte("store hash uki "), 200000)
	header := &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{
		ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: []byte{1, 2}, Revision: "a1b2c3d4e5f6", Tag: "v1.5.0",
		Store: &nodev1.ImagePart{Size: 1, Sha256: []byte{4}}, HashTree: &nodev1.ImagePart{Size: 2, Sha256: []byte{5}}, Uki: &nodev1.ImagePart{Size: 3, Sha256: []byte{6}},
	}}
	for _, tc := range []struct {
		name       string
		reboot     bool
		result     upgrade.Result
		err        error
		wantReboot bool
		wantCode   connect.Code
	}{
		{"installed", false, upgrade.Result{Entry: "chalkos_2+3.efi"}, nil, false, 0},
		{"installed and rebooting", true, upgrade.Result{Entry: "chalkos_2+3.efi"}, nil, true, 0},
		{"already installed", true, upgrade.Result{AlreadyInstalled: true}, nil, false, 0},
		{"already staged and rebooting", true, upgrade.Result{AlreadyStaged: true, Entry: "chalkos_2+3.efi"}, nil, true, 0},
		{"refused", true, upgrade.Result{}, errors.New("the image's platform is kvm, the node's metal"), false, connect.CodeFailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, normal, vda)
			record := filepath.Join(s.Paths.StateDir, "chalkd", "rollback")
			write(t, record, strings.Repeat("31", 32)+"\n")
			rebooted := make(chan struct{})
			s.RebootNode = func() { close(rebooted) }
			var got []byte
			s.InstallImage = func(_ context.Context, h upgrade.Header, _, _ string, _ bool, r io.Reader) (upgrade.Result, imageConfig, error) {
				want := upgrade.Header{ImageID: "chalkos", RootHash: []byte{1, 2},
					StoreSize: 1, VeritySize: 2, UKISize: 3, StoreSHA256: []byte{4}, VeritySHA256: []byte{5}, UKISHA256: []byte{6}}
				if h.ImageID != want.ImageID || h.Platform != "metal" || h.Architecture != "x86-64" || h.Provenance != (upgrade.Provenance{Revision: "a1b2c3d4e5f6", Tag: "v1.5.0"}) ||
					!bytes.Equal(h.RootHash, want.RootHash) || h.StoreSize != 1 || h.VeritySize != 2 || h.UKISize != 3 ||
					!bytes.Equal(h.StoreSHA256, want.StoreSHA256) || !bytes.Equal(h.VeritySHA256, want.VeritySHA256) || !bytes.Equal(h.UKISHA256, want.UKISHA256) {
					t.Errorf("header %+v", h)
				}
				var err error
				got, err = io.ReadAll(r)
				if err != nil {
					t.Error(err)
				}
				return tc.result, imageConfig{}, tc.err
			}
			addr := serve(t, s, c, c.pool)
			conn := dial(t, addr, c.clients[pki.RoleOperator])
			stream := conn.Upgrade(context.Background())
			h := proto.Clone(header).(*nodev1.UpgradeHeader)
			h.Reboot = tc.reboot
			if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: h}}); err != nil {
				t.Fatal(err)
			}
			for rest := image; len(rest) > 0; {
				n := min(len(rest), 1<<20)
				if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Chunk{Chunk: &nodev1.ImageChunk{Data: rest[:n]}}}); err != nil {
					t.Fatal(err)
				}
				rest = rest[n:]
			}
			resp, err := stream.CloseAndReceive()
			if connect.CodeOf(err) != tc.wantCode && !(err == nil && tc.wantCode == 0) {
				t.Fatalf("upgrade = %v, want code %v", err, tc.wantCode)
			}
			if err != nil {
				if !strings.Contains(err.Error(), "the image's platform is kvm") {
					t.Errorf("error %v does not say why", err)
				}
			} else if resp.Msg.Entry != tc.result.Entry || resp.Msg.AlreadyInstalled != tc.result.AlreadyInstalled || resp.Msg.AlreadyStaged != tc.result.AlreadyStaged || resp.Msg.Rebooting != tc.wantReboot {
				t.Errorf("response %+v", resp.Msg)
			}
			if p := upgrade.ReadImages(filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord)).Of([]byte{1, 2}); (p == upgrade.Provenance{Revision: "a1b2c3d4e5f6", Tag: "v1.5.0"}) != (tc.err == nil) {
				t.Errorf("the record of installed images names %+v", p)
			}
			if !bytes.Equal(got, image) {
				t.Errorf("the installation received %d bytes, want %d", len(got), len(image))
			}
			// An upgrade that installed or staged an image replaces the rollback's record.
			if _, err := os.Stat(record); errors.Is(err, fs.ErrNotExist) != (tc.err == nil && !tc.result.AlreadyInstalled) {
				t.Errorf("the record of the last rollback: %v", err)
			}
			select {
			case <-rebooted:
				if !tc.wantReboot {
					t.Error("the node rebooted")
				}
			case <-time.After(3 * time.Second):
				if tc.wantReboot {
					t.Error("the node did not reboot")
				}
			}
		})
	}
}

// TestUpgradeStageOnly has the installation stop once the image is staged when the header says so,
// and answers with the staged UKI and the image not active; without it, the installation
// activates the image too, and the answer says it is.
func TestUpgradeStageOnly(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	s.InstallImage = func(_ context.Context, _ upgrade.Header, _, _ string, stageOnly bool, r io.Reader) (upgrade.Result, imageConfig, error) {
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Error(err)
		}
		if stageOnly {
			return upgrade.Result{Entry: ".staged-chalkos_2.efi"}, imageConfig{}, nil
		}
		return upgrade.Result{Entry: "chalkos_2+3.efi", Active: true}, imageConfig{}, nil
	}
	addr := serve(t, s, c, c.pool)
	conn := dial(t, addr, c.clients[pki.RoleOperator])
	for _, tc := range []struct {
		stageOnly bool
		entry     string
	}{{true, ".staged-chalkos_2.efi"}, {false, "chalkos_2+3.efi"}} {
		stream := conn.Upgrade(context.Background())
		h := &nodev1.UpgradeHeader{StageOnly: tc.stageOnly, Image: &nodev1.ImageHeader{
			ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: []byte{1, 2},
			Store: &nodev1.ImagePart{Size: 1, Sha256: []byte{4}}, HashTree: &nodev1.ImagePart{Size: 1, Sha256: []byte{5}}, Uki: &nodev1.ImagePart{Size: 1, Sha256: []byte{6}},
		}}
		if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: h}}); err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Chunk{Chunk: &nodev1.ImageChunk{Data: []byte("suh")}}}); err != nil {
			t.Fatal(err)
		}
		resp, err := stream.CloseAndReceive()
		if err != nil || resp.Msg.Entry != tc.entry || resp.Msg.Active == tc.stageOnly || resp.Msg.Rebooting {
			t.Errorf("stage only %v: %v, %v", tc.stageOnly, resp, err)
		}
	}
}

// TestUpgradeWhoseActivationFails answers an upgrade whose image was staged and then not activated
// with the activation's error, and keeps what a staged image needs: the record of its provenance,
// and none of the rollback the staging replaced.
func TestUpgradeWhoseActivationFails(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	record := filepath.Join(s.Paths.StateDir, "chalkd", "rollback")
	write(t, record, strings.Repeat("31", 32)+"\n")
	s.InstallImage = func(_ context.Context, _ upgrade.Header, _, _ string, _ bool, r io.Reader) (upgrade.Result, imageConfig, error) {
		io.Copy(io.Discard, r)
		return upgrade.Result{}, imageConfig{}, &upgrade.StagedError{Result: upgrade.Result{Entry: ".staged-chalkos_2.efi", Wrote: true}, Err: &upgrade.RefusalError{Err: errors.New("Secure Boot would refuse the UKI")}}
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleOperator])
	stream := conn.Upgrade(context.Background())
	h := &nodev1.UpgradeHeader{Reboot: true, Image: &nodev1.ImageHeader{ImageId: "chalkos", RootHash: []byte{1, 2}, Revision: "a1b2c3d4e5f6", Tag: "v1.5.0"}}
	if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: h}}); err != nil {
		t.Fatal(err)
	}
	_, err := stream.CloseAndReceive()
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "upgrade: the image is staged as .staged-chalkos_2.efi, but its activation failed: Secure Boot would refuse the UKI") {
		t.Errorf("upgrade = %v", err)
	}
	if p := upgrade.ReadImages(filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord)).Of([]byte{1, 2}); p != (upgrade.Provenance{Revision: "a1b2c3d4e5f6", Tag: "v1.5.0"}) {
		t.Errorf("the record of installed images names %+v for the staged image", p)
	}
	if _, err := os.Stat(record); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the record of the last rollback: %v", err)
	}
}

// activateOn asks s to activate the image with the root hash.
func activateOn(s *Server, root []byte) (*nodev1.ActivateImageResponse, error) {
	resp, err := s.ActivateImage(context.Background(), connect.NewRequest(&nodev1.ActivateImageRequest{RootHash: root}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// TestActivateImage activates the staged image through the boot disk's activation and answers with
// the entry that boots it next, or that it boots next or runs already. It refuses a request without
// an image, and passes an activation's refusal on as a failed precondition and any other failure
// as internal.
func TestActivateImage(t *testing.T) {
	s, _ := stagedServerOf(t, fixtureSchemas(t), "chalkos_2.efi", ".staged-chalkos_3.efi")
	var asked [][]byte
	result, refusal := upgrade.Result{Entry: "chalkos_3+3.efi", Active: true}, error(nil)
	s.ActivateStaged = func(_ context.Context, rootHash []byte) (upgrade.Result, error) {
		asked = append(asked, rootHash)
		return result, refusal
	}
	if _, err := activateOn(s, nil); codeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("activate no image = %v, want invalid", err)
	}
	if len(asked) != 0 {
		t.Fatalf("activated %x", asked)
	}
	if resp, err := activateOn(s, stagedImage); err != nil || resp.Entry != "chalkos_3+3.efi" || resp.AlreadyActive || resp.AlreadyInstalled {
		t.Errorf("activate = %v, %v", resp, err)
	}
	result = upgrade.Result{AlreadyStaged: true, Entry: "chalkos_3+3.efi", Active: true}
	if resp, err := activateOn(s, stagedImage); err != nil || !resp.AlreadyActive {
		t.Errorf("activate again = %v, %v", resp, err)
	}
	result = upgrade.Result{AlreadyInstalled: true}
	if resp, err := activateOn(s, runningImage); err != nil || !resp.AlreadyInstalled {
		t.Errorf("activate the running image = %v, %v", resp, err)
	}
	refusal = &upgrade.RefusalError{Err: errors.New("the image staged in the inactive slot is 33, not 34")}
	if _, err := activateOn(s, stagedImage); codeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "activate: the image staged") {
		t.Errorf("a refused activation = %v", err)
	}
	refusal = errors.New("rename the UKI: input/output error")
	if _, err := activateOn(s, stagedImage); codeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "activate: rename the UKI") {
		t.Errorf("a failed activation = %v, want it internal", err)
	}
	if len(asked) != 5 || !bytes.Equal(asked[2], runningImage) {
		t.Errorf("activated %x", asked)
	}
}

// TestActivateImageRefusals activates on the boot disk and refuses what is staged before the config
// slot: without an image staged, or with another one staged, the answer says so, and only the image
// staged is refused for a config slot STATE does not keep. Each is a failed precondition, before
// anything changes.
func TestActivateImageRefusals(t *testing.T) {
	other := bytes.Repeat([]byte{'4'}, 32)
	for _, tc := range []struct {
		name string
		root []byte
		// prepare changes the node with the image staged as .staged-chalkos_3.efi.
		prepare func(t *testing.T, s *Server)
		want    string
	}{
		{"nothing staged", stagedImage, func(t *testing.T, s *Server) {
			if err := os.Remove(filepath.Join(s.Paths.ESP, "EFI", "Linux", ".staged-chalkos_3.efi")); err != nil {
				t.Fatal(err)
			}
			// The retirement that removed the staged UKI removed its config slot too.
			if err := slot.Retire(s.Loader.Slots, [][]byte{runningImage}); err != nil {
				t.Fatal(err)
			}
		}, "activate: no image is staged in the inactive slot"},
		// STATE keeps no config slot for the other image either.
		{"another image staged", other, nil, "activate: the image staged in the inactive slot is " + hex.EncodeToString(stagedImage) + ", not " + hex.EncodeToString(other)},
		{"the staged image without a config slot", stagedImage, func(t *testing.T, s *Server) {
			if err := slot.Retire(s.Loader.Slots, [][]byte{runningImage}); err != nil {
				t.Fatal(err)
			}
		}, "activate: STATE holds no config for the image " + hex.EncodeToString(stagedImage)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := stagedServerOf(t, fixtureSchemas(t), "chalkos_2.efi", ".staged-chalkos_3.efi")
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			r.Calls = nil
			if _, err := activateOn(s, tc.root); codeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("activate = %v, want %q", err, tc.want)
			}
			if len(r.Calls) != 0 {
				t.Errorf("the refusal ran %v", r.Calls)
			}
		})
	}
}

// TestActivateImageWaitsForATrial refuses to activate the staged image while a config is on trial,
// naming how to end it, and leaves the image staged.
func TestActivateImageWaitsForATrial(t *testing.T) {
	s, r := stagedServerOf(t, fixtureSchemas(t), "chalkos_2.efi", ".staged-chalkos_3.efi")
	rec := recordTrial(t, s, nextConfig)
	r.Calls = nil
	want := "config " + rec.Version + " is on trial until " + rec.Deadline.Format(time.RFC3339) + ": confirm it with chalkctl apply-config --confirm n1, or roll it back with chalkctl apply-config --rollback n1, before an image's activation"
	if _, err := activateOn(s, stagedImage); codeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
		t.Errorf("activate = %v, want %q", err, want)
	}
	if _, err := os.Stat(filepath.Join(s.Paths.ESP, "EFI", "Linux", ".staged-chalkos_3.efi")); err != nil {
		t.Errorf("the staged UKI: %v", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("the refusal ran %v", r.Calls)
	}
}

// TestUpgradeRefusesMalformedHeaders refuses a header without an image, one naming a boot loader,
// and one that asks to stop at staging and to reboot, as invalid before anything else.
func TestUpgradeRefusesMalformedHeaders(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	s.InstallImage = func(context.Context, upgrade.Header, string, string, bool, io.Reader) (upgrade.Result, imageConfig, error) {
		t.Error("installed the image")
		return upgrade.Result{}, imageConfig{}, nil
	}
	addr := serve(t, s, c, c.pool)
	conn := dial(t, addr, c.clients[pki.RoleOperator])
	for _, tc := range []struct {
		header *nodev1.UpgradeHeader
		want   string
	}{
		{&nodev1.UpgradeHeader{Reboot: true}, "the header names no image"},
		{&nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{ImageId: "chalkos", BootLoader: &nodev1.ImagePart{Size: 1, Sha256: []byte{1}}}}, "an upgrade carries no boot loader"},
		{&nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{ImageId: "chalkos"}, Source: &nodev1.ImageSource{}}, "the header's source names no image in a registry"},
		{&nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{ImageId: "chalkos"}, StageOnly: true, Reboot: true}, "an upgrade that stops once the image is staged does not reboot"},
	} {
		stream := conn.Upgrade(context.Background())
		stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: tc.header}})
		_, err := stream.CloseAndReceive()
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.header, err, tc.want)
		}
	}
}

// TestUpgradeDeliversConfigOnlyForAdmins refuses an operator's upgrade that carries config or a
// fallback secret before anything is installed, naming the admin role. An operator's upgrade
// without them carries the running config over to the image, and an admin's keeps the config it
// carries.
func TestUpgradeDeliversConfigOnlyForAdmins(t *testing.T) {
	c := newCreds(t)
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	for _, tc := range []struct {
		name           string
		role           pki.Role
		config, secret string
		// want is the config the image's slot keeps; empty for a refused upgrade.
		want string
	}{
		{"an operator with config", pki.RoleOperator, next, "", ""},
		{"an operator with a fallback secret", pki.RoleOperator, "", secret, ""},
		{"an operator without config", pki.RoleOperator, "", "", baseConfig},
		{"an admin with config", pki.RoleAdmin, next, "", next},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := configServer(t)
			installed, version := false, ""
			s.InstallImage = func(ctx context.Context, _ upgrade.Header, config, secret string, _ bool, r io.Reader) (upgrade.Result, imageConfig, error) {
				installed = true
				io.Copy(io.Discard, r)
				wrote, err := s.stageConfig(ctx, imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, config, secret)
				got, _ := staged(t, s)
				version = got.Version
				return upgrade.Result{AlreadyStaged: true, Entry: "chalkos_2+3.efi"}, wrote, err
			}
			conn := dial(t, serve(t, s, c, c.pool), c.clients[tc.role])
			stream := conn.Upgrade(context.Background())
			header := &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{ImageId: "chalkos", RootHash: newImage}, Config: tc.config, FallbackSecret: tc.secret}
			if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: header}}); err != nil {
				t.Fatal(err)
			}
			_, err := stream.CloseAndReceive()
			if tc.want == "" {
				if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "needs the admin role") {
					t.Errorf("upgrade = %v, want it refused for the admin role", err)
				}
				if installed {
					t.Error("installed the image")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if version != hashOf(t, tc.want) {
				t.Errorf("the image's slot keeps the version %q, want %q", version, hashOf(t, tc.want))
			}
		})
	}
}

// dirStore is an image's store laid out in a directory, its paths those of the image.
type dirStore string

func (d dirStore) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(string(d), name))
}

func (d dirStore) Resolve(name string) (string, error) {
	p := filepath.Join(string(d), name)
	_, err := os.Stat(p)
	return p, err
}

func (d dirStore) Dir() string { return filepath.Join(string(d), "nix", "store") }

// imageWith is the store of an image whose system /nix/store/x holds a checker, the schemas
// files, the facts s declares and, when facts is not empty, the image.json facts. Its checker is
// the test's own: s.CheckImage, an imagecheck.Local, runs s.Check in the test's process.
func imageWith(t *testing.T, s *Server, files map[string][]byte, facts string) dirStore {
	t.Helper()
	root := t.TempDir()
	system := filepath.Join(root, "nix/store/x")
	write(t, filepath.Join(system, "sw/bin/chalkd"), "")
	for name, data := range files {
		write(t, filepath.Join(system, "etc/chalkos/schemas", name), string(data))
	}
	declared, err := os.ReadFile(contractFile(s, contract.FactsFile))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(system, "etc/chalkos/facts.json"), string(declared))
	if facts != "" {
		write(t, filepath.Join(system, k8s.ImageFile), facts)
	}
	return dirStore(root)
}

// newImage is the root hash of the store of the image the upgrade tests stage.
var newImage = bytes.Repeat([]byte{0xcd}, 32)

// staged reads the config slot of the image the upgrade tests stage; found is false without one.
func staged(t *testing.T, s *Server) (slot.Stored, bool) {
	t.Helper()
	stored, found, err := slot.Read(slot.Dir(s.Loader.Slots, newImage))
	if err != nil {
		t.Fatal(err)
	}
	return stored, found
}

// TestStageConfigCarriesTheRunningConfig writes the new image's slot from the running slot's
// documents as delivered when the upgrade delivers none, as on a partial cluster, translated
// with the new image's schemas.
func TestStageConfigCarriesTheRunningConfig(t *testing.T) {
	s, _ := configServer(t)
	if _, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, "", ""); err != nil {
		t.Fatal(err)
	}
	got, found := staged(t, s)
	_, running, _, _ := s.runningSlot()
	if !found || got.Version != hashOf(t, baseConfig) || !reflect.DeepEqual(got.Delivered, running.Delivered) {
		t.Errorf("staged slot = %+v, found %v; want the running slot's documents", got, found)
	}
	var id identity.Identity
	if err := json.Unmarshal(got.Identity, &id); err != nil || id.Hostname != "n1" {
		t.Errorf("staged identity = %s, %v", got.Identity, err)
	}
}

// TestStageConfigWritesTheDeliveredConfig keeps the config an upgrade delivers in the new image's
// slot and leaves the running slot as it is.
func TestStageConfigWritesTheDeliveredConfig(t *testing.T) {
	s, _ := configServer(t)
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	if _, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, next, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := staged(t, s); got.Version != hashOf(t, next) {
		t.Errorf("staged version = %q, want the delivered config's", got.Version)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
		t.Errorf("running version = %q, want it unchanged", v)
	}
}

// TestStageConfigRefuses refuses config the image does not accept, and config that changes the
// machine's storage destructively, saying how that is carried out; it writes no slot.
func TestStageConfigRefuses(t *testing.T) {
	withoutDemo := fixtureSchemas(t)
	delete(withoutDemo, "demo.example.org_v1alpha1_Demo.json")
	for name, tc := range map[string]struct {
		schemas map[string][]byte
		facts   string
		config  string
		want    string
	}{
		"an unknown kind":           {withoutDemo, "", "", `does not accept kind "Demo"`},
		"an unknown delivered kind": {withoutDemo, "", baseConfig, `does not accept kind "Demo"`},
		"Kubernetes without a Node": {fixtureSchemas(t), kubernetestest.Image, "", "no Node: the config of a node whose image runs Kubernetes holds one Node document"},
		"a broken image.json":       {fixtureSchemas(t), `{"version": ""}`, "", "image.json"},
		"no schemas":                {nil, "", "", "the image's schemas"},
		"a VAR of another size":     {fixtureSchemas(t), "", strings.Replace(baseConfig, "{disk: {path: /dev/vda}}", "{disk: {path: /dev/vda}, var: {size: 1G}}", 1), "VAR: size 1G instead of filling its disk; only a reinstall of n1 carries it out"},
		"unparsable config":         {fixtureSchemas(t), "", "kind: [", "parse the config"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := configServer(t)
			_, err := s.stageConfig(context.Background(), imageWith(t, s, tc.schemas, tc.facts), "/nix/store/x", "metal", newImage, tc.config, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one naming %q", err, tc.want)
			}
			// The running config is carried over, as for an operator, who cannot deliver config.
			if tc.config == "" && (err == nil || !strings.Contains(err.Error(), "upgrade with config the image accepts, from an inventory, which needs the admin role")) {
				t.Errorf("err = %v, want the admin role named", err)
			}
			if _, found := staged(t, s); found {
				t.Error("wrote the new image's slot")
			}
		})
	}
}

// volumeRules answer the commands that create the volume extraConfig adds on vdb, encrypted and
// enrolled with the fallback secret checked against STATE's keyslot.
func volumeRules(s *Server) []tooltest.Rule {
	dir := filepath.Join(s.Paths.StateDir, "storage")
	return []tooltest.Rule{
		{Prefix: "blkid -p -o export /dev/vdb", Err: &tool.Error{Command: "blkid", Code: 2}},
		{Prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "extra"), Out: `[{"file":"` + filepath.Join(dir, "disks", "extra", "10-extra.conf") + `","uuid":"` + newUUID + `"}]`},
		{Prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "system"), Out: `[{"file":"` + filepath.Join(dir, "disks", "system", "50-var.conf") + `","uuid":"` + varUUID + `"}]`},
		{Prefix: "blkid -p -o value -s TYPE", Out: "crypto_LUKS\n"},
		{Prefix: "cryptsetup luksDump --dump-json-metadata", Out: luksDump},
	}
}

// TestStageConfigAddsAVolume makes the additive change an upgrade's config makes to the machine
// at once, while the running image runs: the volume is created, enrolled with the fallback secret
// and recorded in storage.json, and the new image's slot names it. The running slot does not.
// Without the fallback secret the volume cannot be enrolled, and nothing changes.
func TestStageConfigAddsAVolume(t *testing.T) {
	s, r := configServer(t)
	r.Rules = volumeRules(s)
	_, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, extraConfig("ext4"), "")
	if err == nil || !strings.Contains(err.Error(), "no fallback secret was sent") {
		t.Fatalf("err = %v, want the missing secret", err)
	}
	if _, found := staged(t, s); found || len(r.Calls) != 0 {
		t.Fatalf("wrote the slot or ran %v", r.Calls)
	}
	if _, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, extraConfig("ext4"), secret); err != nil {
		t.Fatal(err)
	}
	if !contains(r.Calls, "systemctl start systemd-cryptsetup@extra.service srv-extra.mount") {
		t.Errorf("the volume was not created: %v", r.Calls)
	}
	recorded, err := storage.ReadSection(filepath.Join(s.Paths.StateDir, "storage", "storage.json"))
	if err != nil || recorded.Volumes["extra"].MountPoint != "/srv/extra" {
		t.Errorf("recorded section = %+v, %v", recorded, err)
	}
	if got, _ := staged(t, s); got.Version != hashOf(t, extraConfig("ext4")) {
		t.Errorf("staged version = %q, want the delivered config's", got.Version)
	}
	if v, _ := slot.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
		t.Errorf("running version = %q, want it unchanged", v)
	}
}

// TestStageConfigRetriedAfterTheVolume writes the slot again when an upgrade is retried after its
// config's volume was created, as when the slot's write or the activation failed: storage.json
// records the volume already, so the retry creates nothing again and needs no fallback secret.
func TestStageConfigRetriedAfterTheVolume(t *testing.T) {
	s, r := configServer(t)
	r.Rules = volumeRules(s)
	stage := func(secret string) error {
		_, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, extraConfig("ext4"), secret)
		return err
	}
	if err := stage(secret); err != nil {
		t.Fatal(err)
	}
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		t.Fatal(err)
	}
	// The slot the first try wrote is gone, as one a failed activation retired.
	if err := slot.Retire(s.Loader.Slots, [][]byte{running}); err != nil {
		t.Fatal(err)
	}
	r.Calls = nil
	if err := stage(""); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("the retry ran %v", r.Calls)
	}
	if got, found := staged(t, s); !found || got.Version != hashOf(t, extraConfig("ext4")) {
		t.Errorf("staged slot = %+v, found %v; want the delivered config's", got, found)
	}
}

// TestApplyConfigNamesAVolumeAnotherImageAdded refuses config for the running image that leaves out
// a volume the config of another image added, as after a fallback from that image, and says to name
// the volume, as the running image's config never named it, and that only a reinstall removes it.
// While the other image's slot names it, the refusal says so.
func TestApplyConfigNamesAVolumeAnotherImageAdded(t *testing.T) {
	s, r := configServer(t)
	r.Rules = volumeRules(s)
	if _, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, extraConfig("ext4"), secret); err != nil {
		t.Fatal(err)
	}
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	_, err := sendConfig(s, next)
	if err == nil || !strings.Contains(err.Error(), "Volume extra: an earlier config of this image or another added it, with an upgrade or chalkctl apply-config --staged, and it stays on the node; name it in this config too") ||
		!strings.Contains(err.Error(), "; the config of the image cdcdcdcdcdcd names volume extra") {
		t.Errorf("err = %v, want the volume named", err)
	}
	// A reinstall removes the volume, but does not carry out a config that is to name it.
	if err != nil && (strings.Contains(err.Error(), "carries it out") || !strings.Contains(err.Error(), "; only a reinstall of n1 removes it")) {
		t.Errorf("err = %v, want the volume named, and a reinstall only as what removes it", err)
	}
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		t.Fatal(err)
	}
	if err := slot.Retire(s.Loader.Slots, [][]byte{running}); err != nil {
		t.Fatal(err)
	}
	if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "name it in this config too") || strings.Contains(err.Error(), "cdcdcdcdcdcd") {
		t.Errorf("err = %v, want the volume named without another image's slot", err)
	}
}

// TestStageConfigKeepsTheFixedFields refuses config that changes what a node holding a share was
// set up for, as ApplyConfig does: here the node's type.
func TestStageConfigKeepsTheFixedFields(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	next := strings.Replace(workerConfig, "type: worker", "type: controlplane", 1)
	_, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), kubernetestest.Image), "/nix/store/x", "metal", newImage, next, "")
	if err == nil || !strings.Contains(err.Error(), `changes the node's type from "worker" to "controlplane"`) {
		t.Errorf("err = %v", err)
	}
	if _, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), kubernetestest.Image), "/nix/store/x", "metal", newImage, workerConfig, ""); err != nil {
		t.Errorf("the same config: %v", err)
	}
}

// TestStageConfigNeedsConfig refuses an upgrade without config on a node whose running slot holds
// no documents to carry over.
func TestStageConfigNeedsConfig(t *testing.T) {
	s, _ := installedServer(t, section("", ""), false)
	_, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, "", "")
	if err == nil || !strings.Contains(err.Error(), "no config to carry over") || !strings.Contains(err.Error(), "which needs the admin role") {
		t.Errorf("err = %v", err)
	}
}

// TestUpgradeRetiresConfigSlots keeps the slots of the images the UKIs on the ESP boot, and of the
// running one, and removes the others with their provenance, also after a refused upgrade, which
// retired the image it refused.
func TestUpgradeRetiresConfigSlots(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", "chalkos_2.efi", "chalkos_3+3.efi")
	booted, kept, stale := bytes.Repeat([]byte{'2'}, 32), bytes.Repeat([]byte{'3'}, 32), bytes.Repeat([]byte{'4'}, 32)
	for _, h := range [][]byte{booted, kept, stale} {
		if err := slot.Write(slot.Dir(s.Loader.Slots, h), slot.Stored{Version: "v1", Identity: []byte("{}")}); err != nil {
			t.Fatal(err)
		}
	}
	record := upgrade.Images{}
	for _, h := range [][]byte{booted, kept, stale} {
		record = record.Record(h, upgrade.Provenance{Tag: "v1"}, [][]byte{booted, kept, stale})
	}
	data, err := record.Encode()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord), string(data))
	s.InstallImage = func(_ context.Context, h upgrade.Header, _, _ string, _ bool, r io.Reader) (upgrade.Result, imageConfig, error) {
		io.Copy(io.Discard, r)
		// The refusal retires the image chalkos_3 boots.
		if err := os.Remove(filepath.Join(s.Paths.ESP, "EFI", "Linux", "chalkos_3+3.efi")); err != nil {
			t.Error(err)
		}
		return upgrade.Result{}, imageConfig{}, errors.New("the node's config does not suit the image")
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleOperator])
	stream := conn.Upgrade(context.Background())
	header := &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{ImageId: "chalkos", RootHash: kept}}
	if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: header}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.CloseAndReceive(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("upgrade = %v, want its refusal", err)
	}
	slots, err := slot.List(s.Loader.Slots)
	if err != nil || len(slots) != 1 || !bytes.Equal(slots[0], booted) {
		t.Errorf("slots = %q, %v; want the booted image's alone", slots, err)
	}
	if images := upgrade.ReadImages(filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord)); len(images) != 1 || images.Of(booted).Tag != "v1" {
		t.Errorf("the record of installed images = %v, want the booted image's alone", images)
	}
}

// TestOneUpgradeAtATime refuses a second upgrade while one runs.
func TestOneUpgradeAtATime(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	started, release := make(chan struct{}), make(chan struct{})
	s.InstallImage = func(context.Context, upgrade.Header, string, string, bool, io.Reader) (upgrade.Result, imageConfig, error) {
		close(started)
		<-release
		return upgrade.Result{}, imageConfig{}, nil
	}
	addr := serve(t, s, c, c.pool)
	conn := dial(t, addr, c.clients[pki.RoleOperator])
	send := func() error {
		stream := conn.Upgrade(context.Background())
		stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{ImageId: "chalkos"}}}})
		_, err := stream.CloseAndReceive()
		return err
	}
	first := make(chan error, 1)
	go func() { first <- send() }()
	<-started
	if err := send(); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("a second upgrade: %v, want aborted", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Error(err)
	}
}

// lockWatcher runs commands as the fake runner, showing each sfdisk call to check first.
type lockWatcher struct {
	*tooltest.Runner
	check func(args []string)
}

func (w lockWatcher) Exec(ctx context.Context, c tool.Cmd) ([]byte, error) {
	if c.Name == "sfdisk" {
		w.check(c.Args)
	}
	return w.Runner.Exec(ctx, c)
}

// TestUpgradeSharesTheStorageLock reads the boot disk's partition table only while holding the
// lock ApplyConfig and ResetVolume change the node's storage under.
func TestUpgradeSharesTheStorageLock(t *testing.T) {
	s, r := newTestServer(t, normal, vda)
	write(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=worker\nCHALKOS_PLATFORM=metal\n")
	write(t, s.Paths.Cmdline, "usrhash="+strings.Repeat("ab", 32)+"\n")
	calls := 0
	s.Run = lockWatcher{r, func(args []string) {
		calls++
		if s.mu.TryLock() {
			s.mu.Unlock()
			t.Errorf("sfdisk %v ran without the storage lock", args)
		}
	}}
	sum := bytes.Repeat([]byte{1}, 32)
	h := upgrade.Header{ImageID: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: bytes.Repeat([]byte{2}, 32),
		StoreSize: 1, VeritySize: 1, UKISize: 1, StoreSHA256: sum, VeritySHA256: sum, UKISHA256: sum}
	// The fake sfdisk prints no table, which ends the upgrade.
	if _, _, err := s.installImage(context.Background(), h, "", "", false, bytes.NewReader(nil)); err == nil {
		t.Fatal("the upgrade went on without a partition table")
	}
	if calls == 0 {
		t.Error("the upgrade did not read the partition table")
	}
	if !s.mu.TryLock() {
		t.Fatal("the upgrade kept the storage lock")
	}
	s.mu.Unlock()
}

// TestStageConfigNamesNoOtherImageForItsOwnVolume refuses config for the staged image that leaves
// out a volume the staged image's own earlier config added, and names no other image for it: the
// slot being written is the one whose config added it.
func TestStageConfigNamesNoOtherImageForItsOwnVolume(t *testing.T) {
	s, r := configServer(t)
	r.Rules = volumeRules(s)
	stage := func(config, secret string) error {
		_, err := s.stageConfig(context.Background(), imageWith(t, s, fixtureSchemas(t), ""), "/nix/store/x", "metal", newImage, config, secret)
		return err
	}
	if err := stage(extraConfig("ext4"), secret); err != nil {
		t.Fatal(err)
	}
	err := stage(baseConfig, "")
	want := "Volume extra: an earlier config of this image or another added it, with an upgrade or chalkctl apply-config --staged, and it stays on the node; name it in this config too; only a reinstall of n1 removes it"
	if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "cdcdcdcdcdcd") {
		t.Errorf("err = %v, want the volume named without the staged image as another", err)
	}
	if got, _ := staged(t, s); got.Version != hashOf(t, extraConfig("ext4")) {
		t.Errorf("staged version = %q, want the earlier config's", got.Version)
	}
}
