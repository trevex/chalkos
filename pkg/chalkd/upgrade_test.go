package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// TestUpgrade streams an image to the node: the header and the chunks reach the installation,
// which may answer that the node runs the image already, and the node reboots when asked to.
func TestUpgrade(t *testing.T) {
	c := newCreds(t)
	image := bytes.Repeat([]byte("store hash uki "), 200000)
	header := &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{
		Version: "0.2.0", ImageId: "chalkos", Role: "worker", Platform: "metal", Architecture: "x86-64", RootHash: []byte{1, 2},
		Store: &nodev1.ImagePart{Size: 1, Sha256: []byte{4}}, HashTree: &nodev1.ImagePart{Size: 2, Sha256: []byte{5}}, Uki: &nodev1.ImagePart{Size: 3, Sha256: []byte{6}},
	}, Schemas: fixtureSchemas(t)}
	for _, tc := range []struct {
		name       string
		reboot     bool
		result     upgrade.Result
		err        error
		wantReboot bool
		wantCode   connect.Code
	}{
		{"installed", false, upgrade.Result{Entry: "chalkos_0.2.0+3.efi"}, nil, false, 0},
		{"installed and rebooting", true, upgrade.Result{Entry: "chalkos_0.2.0+3.efi"}, nil, true, 0},
		{"already installed", true, upgrade.Result{AlreadyInstalled: true}, nil, false, 0},
		{"refused", true, upgrade.Result{}, errors.New("the image's role is controlplane, the node's worker"), false, connect.CodeFailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, normal, vda)
			rebooted := make(chan struct{})
			s.RebootNode = func() { close(rebooted) }
			var got []byte
			s.InstallImage = func(_ context.Context, h upgrade.Header, r io.Reader) (upgrade.Result, error) {
				want := upgrade.Header{ImageID: "chalkos", Version: "0.2.0", Role: "worker", RootHash: []byte{1, 2},
					StoreSize: 1, VeritySize: 2, UKISize: 3, StoreSHA256: []byte{4}, VeritySHA256: []byte{5}, UKISHA256: []byte{6}}
				if h.Version != want.Version || h.ImageID != want.ImageID || h.Role != want.Role || h.Platform != "metal" || h.Architecture != "x86-64" ||
					!bytes.Equal(h.RootHash, want.RootHash) || h.StoreSize != 1 || h.VeritySize != 2 || h.UKISize != 3 ||
					!bytes.Equal(h.StoreSHA256, want.StoreSHA256) || !bytes.Equal(h.VeritySHA256, want.VeritySHA256) || !bytes.Equal(h.UKISHA256, want.UKISHA256) {
					t.Errorf("header %+v", h)
				}
				var err error
				got, err = io.ReadAll(r)
				if err != nil {
					t.Error(err)
				}
				return tc.result, tc.err
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
				if !strings.Contains(err.Error(), "the image's role is controlplane") {
					t.Errorf("error %v does not say why", err)
				}
			} else if resp.Msg.Entry != tc.result.Entry || resp.Msg.AlreadyInstalled != tc.result.AlreadyInstalled || resp.Msg.Rebooting != tc.wantReboot {
				t.Errorf("response %+v", resp.Msg)
			}
			if !bytes.Equal(got, image) {
				t.Errorf("the installation received %d bytes, want %d", len(got), len(image))
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

// TestUpgradeRefusesMalformedHeaders refuses a header without an image, and one naming a boot
// loader, as invalid before anything else.
func TestUpgradeRefusesMalformedHeaders(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	s.InstallImage = func(context.Context, upgrade.Header, io.Reader) (upgrade.Result, error) {
		t.Error("installed the image")
		return upgrade.Result{}, nil
	}
	addr := serve(t, s, c, c.pool)
	conn := dial(t, addr, c.clients[pki.RoleOperator])
	for _, tc := range []struct {
		header *nodev1.UpgradeHeader
		want   string
	}{
		{&nodev1.UpgradeHeader{Reboot: true}, "the header names no image"},
		{&nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{Version: "0.2.0", BootLoader: &nodev1.ImagePart{Size: 1, Sha256: []byte{1}}}}, "an upgrade carries no boot loader"},
		{&nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{Version: "0.2.0"}}, "no schemas"},
	} {
		stream := conn.Upgrade(context.Background())
		stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: tc.header}})
		_, err := stream.CloseAndReceive()
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.header, err, tc.want)
		}
	}
}

// TestUpgradeChecksTheStoredConfig refuses, before anything is installed, an image whose schemas
// or Kubernetes facts the node's stored config does not suit, and installs one it suits.
func TestUpgradeChecksTheStoredConfig(t *testing.T) {
	c := newCreds(t)
	withoutDemo := fixtureSchemas(t)
	delete(withoutDemo, "demo.example.org_v1alpha1_Demo.json")
	for name, tc := range map[string]struct {
		schemas         map[string][]byte
		kubernetesImage string
		code            connect.Code
		want            string
	}{
		"suits":                     {fixtureSchemas(t), "", 0, ""},
		"an unknown kind":           {withoutDemo, "", connect.CodeFailedPrecondition, "Demo"},
		"Kubernetes without a type": {fixtureSchemas(t), testImage, connect.CodeFailedPrecondition, "it fails the node's identity"},
		"a broken image.json":       {fixtureSchemas(t), `{"version": ""}`, connect.CodeInvalidArgument, "image.json"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := configServer(t)
			var installed atomic.Bool
			s.InstallImage = func(_ context.Context, _ upgrade.Header, r io.Reader) (upgrade.Result, error) {
				installed.Store(true)
				_, err := io.Copy(io.Discard, r)
				return upgrade.Result{Entry: "chalkos_0.2.0+3.efi"}, err
			}
			addr := serve(t, s, c, c.pool)
			conn := dial(t, addr, c.clients[pki.RoleOperator])
			stream := conn.Upgrade(context.Background())
			stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: &nodev1.UpgradeHeader{
				Image:           &nodev1.ImageHeader{Version: "0.2.0", Role: "worker", Platform: "metal"},
				Schemas:         tc.schemas,
				KubernetesImage: []byte(tc.kubernetesImage),
			}}})
			_, err := stream.CloseAndReceive()
			if tc.want == "" {
				if err != nil || !installed.Load() {
					t.Fatalf("upgrade = %v, installed %v", err, installed.Load())
				}
				return
			}
			if connect.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) || installed.Load() {
				t.Errorf("upgrade = %v, installed %v; want %v naming %q", err, installed.Load(), tc.code, tc.want)
			}
		})
	}
}

// upgradeOnce streams an upgrade header without chunks to s as an operator, and tells whether the
// node went on to install the image.
func upgradeOnce(t *testing.T, s *Server, h *nodev1.UpgradeHeader) (bool, error) {
	t.Helper()
	c := newCreds(t)
	var installed atomic.Bool
	s.InstallImage = func(_ context.Context, _ upgrade.Header, r io.Reader) (upgrade.Result, error) {
		installed.Store(true)
		_, err := io.Copy(io.Discard, r)
		return upgrade.Result{Entry: "chalkos_0.2.0+3.efi"}, err
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleOperator])
	stream := conn.Upgrade(context.Background())
	stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: h}})
	_, err := stream.CloseAndReceive()
	return installed.Load(), err
}

// TestUpgradeWithoutStoredConfig installs on a node that holds no config, which translates none
// at boot, also an image with Kubernetes.
func TestUpgradeWithoutStoredConfig(t *testing.T) {
	s, _ := newTestServer(t, normal, vda)
	installed, err := upgradeOnce(t, s, &nodev1.UpgradeHeader{
		Image:           &nodev1.ImageHeader{Version: "0.2.0", Role: "worker", Platform: "metal"},
		Schemas:         fixtureSchemas(t),
		KubernetesImage: []byte(testImage),
	})
	if err != nil || !installed {
		t.Fatalf("upgrade = %v, installed %v", err, installed)
	}
}

// patternSecretSchema is the Secret schema with a pattern that refuses every value but "", so a
// refusal would quote the value checked.
func patternSecretSchema(t *testing.T) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(fixtureSchemas(t)[secretSchema], &doc); err != nil {
		t.Fatal(err)
	}
	spec := doc["properties"].(map[string]any)["spec"].(map[string]any)
	data := spec["properties"].(map[string]any)["data"].(map[string]any)
	data["additionalProperties"] = map[string]any{"type": "string", "pattern": "^$"}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestUpgradeKeepsSecretsFromTheHeadersSchemas: an operator may not read the node's Secrets, so
// the check of the stored config never shows a Secret's value to a schema the header carries,
// nor quotes one in the reply or the journal.
func TestUpgradeKeepsSecretsFromTheHeadersSchemas(t *testing.T) {
	var journal bytes.Buffer
	log.SetOutput(&journal)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	attack := fixtureSchemas(t)
	attack[secretSchema] = patternSecretSchema(t)
	header := &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{Version: "0.2.0", Role: "worker", Platform: "metal"}, Schemas: attack}

	// The header's Secret schema is not the one the Secrets are checked against.
	s, _ := configServer(t)
	if installed, err := upgradeOnce(t, s, header); err != nil || !installed {
		t.Errorf("upgrade = %v, installed %v; want the running image's Secret schema used", err, installed)
	}

	// Even a schema that refuses a value sees none.
	s, _ = configServer(t)
	write(t, filepath.Join(s.Identity.Schemas, secretSchema), string(patternSecretSchema(t)))
	installed, err := upgradeOnce(t, s, header)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || installed {
		t.Fatalf("upgrade = %v, installed %v; want the redacted value refused", err, installed)
	}
	if strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), `Secret "demo": - at '/spec/data/token': pattern`) {
		t.Errorf("err = %v, want the place and keyword and not the Secret's value", err)
	}
	if !strings.Contains(journal.String(), "refused") || strings.Contains(journal.String(), "s3cret") {
		t.Errorf("the journal holds no refusal, or the Secret's value:\n%s", journal.String())
	}
}

// TestUpgradeRefusalQuotesNoValue: a header schema that refuses a value of a document other than
// a Secret learns where and by which keyword, but neither the reply nor the journal quotes the
// value; nor does a refusal of a later step, whose own message would.
func TestUpgradeRefusalQuotesNoValue(t *testing.T) {
	var journal bytes.Buffer
	log.SetOutput(&journal)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	attack := fixtureSchemas(t)
	var doc map[string]any
	if err := json.Unmarshal(attack["demo.example.org_v1alpha1_Demo.json"], &doc); err != nil {
		t.Fatal(err)
	}
	spec := doc["properties"].(map[string]any)["spec"].(map[string]any)
	spec["properties"].(map[string]any)["name"] = map[string]any{"type": "string", "pattern": "^$"}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	attack["demo.example.org_v1alpha1_Demo.json"] = data

	s, _ := configServer(t)
	installed, err := upgradeOnce(t, s, &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{Version: "0.2.0", Role: "worker", Platform: "metal"}, Schemas: attack})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || installed {
		t.Fatalf("upgrade = %v, installed %v", err, installed)
	}
	if !strings.Contains(err.Error(), `Demo "one": - at '/spec/name': pattern`) || strings.Contains(err.Error(), "first") {
		t.Errorf("err = %v, want the place and keyword and not the value", err)
	}

	// The node's identity on an image with Kubernetes fails for the Node's missing type; its
	// message would quote the Node, the reply names the step alone.
	s, _ = configServer(t)
	installed, err = upgradeOnce(t, s, &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{Version: "0.2.0", Role: "worker", Platform: "metal"},
		Schemas: fixtureSchemas(t), KubernetesImage: []byte(testImage)})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || installed {
		t.Fatalf("upgrade = %v, installed %v", err, installed)
	}
	if !strings.Contains(err.Error(), "it fails the node's identity") || strings.Contains(err.Error(), `Node "n1"`) {
		t.Errorf("err = %v, want the step and not its message", err)
	}
	if !strings.Contains(journal.String(), "refused") || strings.Contains(journal.String(), "first") || strings.Contains(journal.String(), `Node "n1"`) {
		t.Errorf("the journal holds no refusal, or a value:\n%s", journal.String())
	}
}

// TestRedactSecretsReplacesEveryStringOfTheirSpec: every string under a Secret's spec is
// replaced, in fields besides data too, and the documents given are left as they are.
func TestRedactSecretsReplacesEveryStringOfTheirSpec(t *testing.T) {
	docs, err := config.Parse(strings.NewReader(`apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: s}
spec: {data: {k: v1}, more: {list: [v2, {deep: v3}], n: 7}}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: first}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := redactSecrets(docs)
	want := map[string]any{"data": map[string]any{"k": redactedSecret},
		"more": map[string]any{"list": []any{redactedSecret, map[string]any{"deep": redactedSecret}}, "n": json.Number("7")}}
	if !reflect.DeepEqual(got[0].Object["spec"], want) || got[0].Object["metadata"].(map[string]any)["name"] != "s" {
		t.Errorf("redacted Secret = %#v", got[0].Object)
	}
	if !reflect.DeepEqual(got[1], docs[1]) {
		t.Errorf("redacted another document: %#v", got[1].Object)
	}
	if docs[0].Object["spec"].(map[string]any)["data"].(map[string]any)["k"] != "v1" {
		t.Error("changed the documents given")
	}
}

// TestOneUpgradeAtATime refuses a second upgrade while one runs.
func TestOneUpgradeAtATime(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	started, release := make(chan struct{}), make(chan struct{})
	s.InstallImage = func(context.Context, upgrade.Header, io.Reader) (upgrade.Result, error) {
		close(started)
		<-release
		return upgrade.Result{}, nil
	}
	addr := serve(t, s, c, c.pool)
	conn := dial(t, addr, c.clients[pki.RoleOperator])
	send := func() error {
		stream := conn.Upgrade(context.Background())
		stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{Version: "0.2.0"}, Schemas: fixtureSchemas(t)}}})
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
	*fakeRunner
	check func(args []string)
}

func (w lockWatcher) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "sfdisk" {
		w.check(args)
	}
	return w.fakeRunner.Run(ctx, name, args...)
}

// TestUpgradeSharesTheStorageLock reads the boot disk's partition table only while holding the
// lock ApplyConfig and ResetVolume change the node's storage under.
func TestUpgradeSharesTheStorageLock(t *testing.T) {
	s, r := newTestServer(t, normal, vda)
	write(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=0.1.0\nCHALKOS_ROLE=worker\nCHALKOS_PLATFORM=metal\n")
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
	h := upgrade.Header{ImageID: "chalkos", Version: "0.2.0", Role: "worker", Platform: "metal", Architecture: "x86-64", RootHash: bytes.Repeat([]byte{2}, 32),
		StoreSize: 1, VeritySize: 1, UKISize: 1, StoreSHA256: sum, VeritySHA256: sum, UKISHA256: sum}
	// The fake sfdisk prints no table, which ends the upgrade.
	if _, err := s.installImage(context.Background(), h, bytes.NewReader(nil)); err == nil {
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
