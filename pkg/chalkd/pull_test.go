package chalkd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-containerregistry/pkg/authn"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// registryConfig is a Registry of the host with the user ci-bot and the token s3cret, and its
// Secret.
func registryConfig(host string) string {
	return fmt.Sprintf(`apiVersion: chalkos.dev/v1alpha1
kind: Registry
metadata: {name: lab}
spec: {host: %q, username: ci-bot, password: {secretRef: {name: lab, key: token}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: lab}
spec: {data: {token: s3cret}}
`, host)
}

// pulledHeader pushes a small chalkos image to the registry at host and returns the header of an
// upgrade that pulls it, as chalkctl sends it.
func pulledHeader(t *testing.T, host string) *nodev1.UpgradeHeader {
	t.Helper()
	h := ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("ab", 32), Architecture: "x86-64", Platform: "metal"}
	img := ocitest.Image(t, h, ociimage.Contract{Metadata: []byte("{}"), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}})
	ref := ocitest.Push(t, img, host+"/lab/node-metal:v1")
	part := func(data string) *nodev1.ImagePart {
		sum := sha256.Sum256([]byte(data))
		return &nodev1.ImagePart{Size: uint64(len(data)), Sha256: sum[:]}
	}
	return &nodev1.UpgradeHeader{
		Image: &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: bytes.Repeat([]byte{0xab}, 32),
			Store: part("store"), HashTree: part("hash tree"), Uki: part("uki")},
		Source: &nodev1.ImageSource{Source: &nodev1.ImageSource_Oci{Oci: &nodev1.RegistryImage{Reference: ref}}},
	}
}

// TestUpgradePullsTheImage pulls the image an upgrade names in a registry, with the credentials of
// the Registry of its host in the config the upgrade delivers, or else in the running config, and
// anonymously without one, which a registry that asks for credentials refuses. An image whose
// header is not the upgrade's is refused. The installation reads the store, hash tree and UKI as
// it reads them from chunks; failures name the reference.
func TestUpgradePullsTheImage(t *testing.T) {
	c := newCreds(t)
	host := ocitest.AuthRegistry(t, "ci-bot", "s3cret")
	header := pulledHeader(t, host)
	ref := header.GetSource().GetOci().GetReference()
	for _, tc := range []struct {
		name, running, config, platform, want string
	}{
		{name: "with the running config's Registry", running: registryConfig(host)},
		{name: "with the delivered config's Registry", config: registryConfig(host)},
		{name: "with the delivered config's Registry over the running one's", running: registryConfig("other.example"), config: registryConfig(host)},
		{name: "not with the running config's Registry when the upgrade delivers config", running: registryConfig(host), config: registryConfig("other.example"), want: "UNAUTHORIZED"},
		{name: "anonymously", want: "UNAUTHORIZED"},
		{name: "another header", running: registryConfig(host), platform: "kvm", want: "its header describes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := configServer(t)
			if tc.running != "" {
				docs, err := config.Parse(strings.NewReader(tc.running))
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Write(slotOf(t, s), store.Stored{Delivered: docs, Documents: docs, Version: "v1"}); err != nil {
					t.Fatal(err)
				}
			}
			var got []byte
			s.InstallImage = func(_ context.Context, _ upgrade.Header, _, _ string, r io.Reader) (upgrade.Result, error) {
				var err error
				got, err = io.ReadAll(r)
				return upgrade.Result{Entry: "chalkos_2+3.efi"}, err
			}
			conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleAdmin])
			h := proto.Clone(header).(*nodev1.UpgradeHeader)
			h.Config = tc.config
			if tc.platform != "" {
				h.Image.Platform = tc.platform
			}
			stream := conn.Upgrade(context.Background())
			if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: h}}); err != nil {
				t.Fatal(err)
			}
			_, err := stream.CloseAndReceive()
			if tc.want == "" && (err != nil || string(got) != "storehash treeuki") {
				t.Errorf("upgrade = %v, received %q", err, got)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), ref)) {
				t.Errorf("upgrade = %v, want an error naming %s and %q", err, ref, tc.want)
			}
		})
	}
}

// TestUpgradePullsNothingForAnImageItHolds contacts no registry for an image that runs or boots
// next already: the installation reads nothing of the stream.
func TestUpgradePullsNothingForAnImageItHolds(t *testing.T) {
	c := newCreds(t)
	host, manifests := ocitest.CountingRegistry(t)
	header := pulledHeader(t, host)
	before := manifests.Load()
	s, _ := newTestServer(t, normal, vda)
	s.InstallImage = func(context.Context, upgrade.Header, string, string, io.Reader) (upgrade.Result, error) {
		return upgrade.Result{AlreadyInstalled: true}, nil
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleOperator])
	stream := conn.Upgrade(context.Background())
	if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: header}}); err != nil {
		t.Fatal(err)
	}
	resp, err := stream.CloseAndReceive()
	if err != nil || !resp.Msg.GetAlreadyInstalled() {
		t.Fatalf("upgrade = %v, %v", resp, err)
	}
	if n := manifests.Load() - before; n != 0 {
		t.Errorf("fetched %d manifests", n)
	}
}

// TestUpgradeRefusesChunksOfAPull refuses an upgrade that names its image in a registry and
// streams chunks too.
func TestUpgradeRefusesChunksOfAPull(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	s.InstallImage = func(context.Context, upgrade.Header, string, string, io.Reader) (upgrade.Result, error) {
		t.Error("installed the image")
		return upgrade.Result{}, nil
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleOperator])
	stream := conn.Upgrade(context.Background())
	stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: pulledHeader(t, ocitest.Registry(t))}})
	stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Chunk{Chunk: &nodev1.ImageChunk{Data: []byte("store")}}})
	if _, err := stream.CloseAndReceive(); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "an upgrade that pulls its image carries no chunks") {
		t.Errorf("upgrade = %v", err)
	}
}

// TestRegistryAuth authenticates with the Registry of the reference's registry host, Docker Hub's
// by either of its names, and pulls anonymously from a host no Registry names, whatever its port.
func TestRegistryAuth(t *testing.T) {
	docs, err := config.Parse(strings.NewReader(registryConfig("docker.io")))
	if err != nil {
		t.Fatal(err)
	}
	digest := "@sha256:" + strings.Repeat("ab", 32)
	for ref, want := range map[string]authn.Authenticator{
		"docker.io/library/chalkos" + digest:       &authn.Basic{Username: "ci-bot", Password: "s3cret"},
		"index.docker.io/library/chalkos" + digest: &authn.Basic{Username: "ci-bot", Password: "s3cret"},
		"docker.io:5000/library/chalkos" + digest:  authn.Anonymous,
		"ghcr.io/library/chalkos" + digest:         authn.Anonymous,
	} {
		got, err := registryAuth(ref, docs)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, %v; want %v", ref, got, err, want)
		}
	}
	if _, err := registryAuth("docker.io/library/chalkos:v1", docs); err == nil || !strings.Contains(err.Error(), "is no image reference by digest") {
		t.Errorf("by tag: %v", err)
	}
}
