package chalkd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-containerregistry/pkg/authn"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// registryConfig is a Registry of the host with the user ci-bot and the token s3cret, which
// allows plain HTTP, as the test registries serve it, and its Secret.
func registryConfig(host string) string {
	return fmt.Sprintf(`apiVersion: chalkos.dev/v1alpha1
kind: Registry
metadata: {name: lab}
spec: {host: %q, insecure: true, username: ci-bot, password: {secretRef: {name: lab, key: token}}}
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
// anonymously without one, which a registry that asks for credentials refuses. It pulls over
// HTTPS alone unless the Registry allows plain HTTP. An image whose
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
		{name: "not with the running config's Registry when the upgrade delivers config", running: registryConfig(host), config: registryConfig("other.example"), want: "plain HTTP to " + host + " is refused"},
		{name: "anonymously", running: fmt.Sprintf("apiVersion: chalkos.dev/v1alpha1\nkind: Registry\nmetadata: {name: lab}\nspec: {host: %q, insecure: true}\n", host), want: "UNAUTHORIZED"},
		{name: "over HTTPS alone without a Registry", want: "plain HTTP to " + host + " is refused: set insecure: true"},
		{name: "over HTTPS alone without insecure", running: strings.Replace(registryConfig(host), "insecure: true, ", "", 1), want: "plain HTTP to " + host + " is refused: set insecure: true"},
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

// TestRegistryAuth authenticates with the Registry of the reference's registry host, in any case
// and Docker Hub's by either of its names, and allows plain HTTP if it does. It pulls anonymously
// and over HTTPS alone from a host no Registry names, whatever its port.
func TestRegistryAuth(t *testing.T) {
	docs, err := config.Parse(strings.NewReader(registryConfig("Docker.IO")))
	if err != nil {
		t.Fatal(err)
	}
	digest := "@sha256:" + strings.Repeat("ab", 32)
	basic := &authn.Basic{Username: "ci-bot", Password: "s3cret"}
	for ref, want := range map[string]struct {
		auth     authn.Authenticator
		insecure bool
	}{
		"docker.io/library/chalkos" + digest:       {basic, true},
		"index.docker.io/library/chalkos" + digest: {basic, true},
		"docker.io:5000/library/chalkos" + digest:  {authn.Anonymous, false},
		"ghcr.io/library/chalkos" + digest:         {authn.Anonymous, false},
	} {
		got, insecure, err := registryAuth(ref, docs)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want.auth) || insecure != want.insecure {
			t.Errorf("%s: %v, %v, %v; want %v, %v", ref, got, insecure, err, want.auth, want.insecure)
		}
	}
	if _, _, err := registryAuth("docker.io/library/chalkos:v1", docs); err == nil || !strings.Contains(err.Error(), "is no image reference by digest") {
		t.Errorf("by tag: %v", err)
	}
}

// TestUpgradePullFailsInternallyWithoutTheRunningConfig answers an upgrade that pulls without
// config with an internal error when the running config cannot be read.
func TestUpgradePullFailsInternallyWithoutTheRunningConfig(t *testing.T) {
	c := newCreds(t)
	s, _ := configServer(t)
	slot := slotOf(t, s)
	if err := os.RemoveAll(slot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(slot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.InstallImage = func(context.Context, upgrade.Header, string, string, io.Reader) (upgrade.Result, error) {
		t.Error("installed the image")
		return upgrade.Result{}, nil
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleOperator])
	stream := conn.Upgrade(context.Background())
	stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: pulledHeader(t, ocitest.Registry(t))}})
	if _, err := stream.CloseAndReceive(); connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "read the running image's config slot") {
		t.Errorf("upgrade = %v", err)
	}
}

// imagePart is the size and SHA-256 of a part's data.
func imagePart(data string) *nodev1.ImagePart {
	sum := sha256.Sum256([]byte(data))
	return &nodev1.ImagePart{Size: uint64(len(data)), Sha256: sum[:]}
}

// pulledInstall pushes a small chalkos image, whose contract holds the fixture schemas, a repart
// definition and image.json, to the registry at host. It returns the header of an install from
// the installer that pulls the image, as chalkctl sends it, with the worker config and the
// documents given.
func pulledInstall(t *testing.T, host, docs string) *nodev1.InstallHeader {
	t.Helper()
	root := strings.Repeat("ab", 32)
	md := `{"schemaVersion": 0, "id": "chalkos", "variant": "worker", "platform": "metal", "rootHash": "` + root + `", "kinds": []}`
	img := ocitest.Image(t, ociimage.Header{ImageID: "chalkos", RootHash: root, Architecture: "x86-64", Platform: "metal"},
		ociimage.Contract{Metadata: []byte(md), Schemas: fixtureSchemas(t), Definitions: map[string][]byte{"50-state.conf": []byte("[Partition]\nLabel=state\n")}, ImageJSON: []byte(testImage)})
	ref := ocitest.Push(t, img, host+"/lab/worker-metal:v1")
	h := header(t, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	h.Config = workerConfig + "---\n" + docs
	h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: bytes.Repeat([]byte{0xab}, 32),
		Store: imagePart("store"), HashTree: imagePart("hash tree"), Uki: imagePart("uki"), BootLoader: imagePart("boot loader")}
	h.Source = &nodev1.ImageSource{Source: &nodev1.ImageSource_Oci{Oci: &nodev1.RegistryImage{Reference: ref}}}
	return h
}

// TestInstallPullsTheImage has the installer pull the image an install names in a registry, with
// the credentials of the Registry of its host in the config the install delivers. It reads the
// store, hash tree, UKI and boot loader as it reads them from chunks, and takes the repart
// definitions, schemas and image.json from the image's contract layer: the config is translated
// for an image with Kubernetes.
func TestInstallPullsTheImage(t *testing.T) {
	host := ocitest.AuthRegistry(t, "ci-bot", "s3cret")
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	var got install.PartsRequest
	var pulled []byte
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		var err error
		pulled, err = io.ReadAll(req.Parts)
		return err
	}
	if err := sendInstall(t, s, pulledInstall(t, host, registryConfig(host)), nil); err != nil {
		t.Fatal(err)
	}
	if string(pulled) != "storehash treeukiboot loader" || got.Image.BootLoaderSize != int64(len("boot loader")) {
		t.Errorf("pulled %q for %+v", pulled, got.Image)
	}
	if !reflect.DeepEqual(got.SystemDefinitions, map[string]string{"50-state.conf": "[Partition]\nLabel=state\n"}) {
		t.Errorf("definitions = %v", got.SystemDefinitions)
	}
	if k := got.Kubernetes; k == nil || k.NodeName != "n1" {
		t.Errorf("Kubernetes identity = %+v, want one translated with the contract's image.json", got.Kubernetes)
	}
}

// TestInstallPassesOverAStoreItHolds lets the install pass over the store and hash tree of a
// pulled image without reading them, as it does for a slot an earlier attempt wrote.
func TestInstallPassesOverAStoreItHolds(t *testing.T) {
	host := ocitest.Registry(t)
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	var rest []byte
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		if _, ok := req.Parts.(upgrade.Skipper); !ok {
			return errors.New("the pulled parts cannot be passed over")
		}
		if err := upgrade.Skip(req.Image, req.Parts); err != nil {
			return err
		}
		var err error
		rest, err = io.ReadAll(req.Parts)
		return err
	}
	if err := sendInstall(t, s, pulledInstall(t, host, fmt.Sprintf("apiVersion: chalkos.dev/v1alpha1\nkind: Registry\nmetadata: {name: lab}\nspec: {host: %q, insecure: true}\n", host)), nil); err != nil {
		t.Fatal(err)
	}
	if string(rest) != "ukiboot loader" {
		t.Errorf("after the store: %q", rest)
	}
}

// TestInstallRefusesAPull refuses an install that pulls its image with the image's contract or
// chunks of its own, in place, without credentials the registry accepts, or of another image
// than the header's. Each refusal comes before the target disk is touched; a registry's refusal
// names the reference.
func TestInstallRefusesAPull(t *testing.T) {
	host := ocitest.AuthRegistry(t, "ci-bot", "s3cret")
	for name, tc := range map[string]struct {
		edit    func(*nodev1.InstallHeader)
		chunk   bool
		code    connect.Code
		want    string
		inPlace bool
	}{
		"with schemas":            {edit: func(h *nodev1.InstallHeader) { h.Schemas = fixtureSchemas(t) }, code: connect.CodeInvalidArgument, want: "takes the schemas, image.json and repart definitions from the image's contract layer"},
		"with image.json":         {edit: func(h *nodev1.InstallHeader) { h.KubernetesImage = []byte(testImage) }, code: connect.CodeInvalidArgument, want: "the header must carry none"},
		"with definitions":        {edit: func(h *nodev1.InstallHeader) { h.SystemDefinitions = map[string]string{"x.conf": ""} }, code: connect.CodeInvalidArgument, want: "the header must carry none"},
		"with chunks":             {chunk: true, code: connect.CodeInvalidArgument, want: "an install that pulls its image carries no chunks"},
		"in place":                {inPlace: true, code: connect.CodeInvalidArgument, want: "an install in place takes no image"},
		"with a reference by tag": {edit: func(h *nodev1.InstallHeader) { h.Source.GetOci().Reference = host + "/lab/worker-metal:v1" }, code: connect.CodeInvalidArgument, want: "is no image reference by digest"},
		"with bad Registry documents": {edit: func(h *nodev1.InstallHeader) {
			h.Config = workerConfig + "---\n" + strings.Replace(registryConfig(host), "key: token", "key: other", 1)
		}, code: connect.CodeInvalidArgument, want: "the Registry documents"},
		"without credentials": {edit: func(h *nodev1.InstallHeader) {
			h.Config = workerConfig + "---\n" + strings.Replace(registryConfig(host), "s3cret", "wrong", 1)
		}, code: connect.CodeFailedPrecondition, want: "install: pull "},
		"over HTTPS alone without a Registry": {edit: func(h *nodev1.InstallHeader) { h.Config = workerConfig }, code: connect.CodeFailedPrecondition, want: "plain HTTP to " + host + " is refused"},
		"another header":                      {edit: func(h *nodev1.InstallHeader) { h.Image.Platform = "kvm" }, code: connect.CodeFailedPrecondition, want: "its header describes"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestServer(t, maintenance, vda)
			s.Installer = !tc.inPlace
			s.FromParts = func(context.Context, install.PartsRequest) error { t.Error("installed onto the disk"); return nil }
			s.InPlace = func(context.Context, install.Request) error { t.Error("installed in place"); return nil }
			h := pulledInstall(t, host, registryConfig(host))
			if tc.inPlace {
				h.Target, h.Image = &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}, nil
			}
			if tc.edit != nil {
				tc.edit(h)
			}
			var chunks []byte
			if tc.chunk {
				chunks = []byte("store")
			}
			err := sendInstall(t, s, h, chunks)
			if connect.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("install = %v, want %v and %q", err, tc.code, tc.want)
			}
			if tc.code == connect.CodeFailedPrecondition && !strings.Contains(err.Error(), "install: pull "+h.GetSource().GetOci().GetReference()) {
				t.Errorf("install = %v, want the reference named", err)
			}
		})
	}
}
