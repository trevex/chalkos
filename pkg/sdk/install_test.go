package sdk_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/chalkdtest"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
	imagecontract "github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/kubernetes/kubetest"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema/schematest"
	"github.com/trevex/chalkos/pkg/sdk"
	sdkkubernetes "github.com/trevex/chalkos/pkg/sdk/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/sdk/sdktest"
)

const (
	w1SystemDisk = "apiVersion: storage.chalkos.dev/v1alpha1\nkind: SystemDisk\nmetadata: {name: system}\nspec: {disk: {serial: chalk-target}}\n"
	w1Network    = "apiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-uplink}\nspec: {network: {address: [10.0.0.21/24]}}\n"
	w1Host       = "apiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: default}\nspec: {hostname: w1.lab}\n"
	// monitoringAddon is an Addon, which control planes alone get.
	monitoringAddon = "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Addon\nmetadata: {name: monitoring}\nspec: {objects: []}\n"
)

// An install in place sends the node its certificate, the OS CA bundle, its fallback secret, its
// layers' payloads and the documents a worker gets, without the Addon documents.
func TestInstallInPlaceSendsWhatTheNodeNeeds(t *testing.T) {
	secrets := generateSecrets(t)
	var got install.Request
	s := sdktest.Maintenance()
	s.InPlace = func(_ context.Context, r install.Request, _ []byte) error { got = r; return nil }
	kubernetesImage(t, s)
	conn := dialInstall(t, s, secrets)
	docs := parseDocuments(t, w1SystemDisk, w1Network, w1Host, kubetest.ClusterDoc("default", "{}"), kubetest.NodeDoc("default", "{type: worker}"), monitoringAddon)
	if err := sdk.Install(context.Background(), conn, installRequest(secrets, docs)); err != nil {
		t.Fatal(err)
	}
	cert, err := pki.VerifyNode(string(got.NodeCertificate), string(got.NodeKey), secrets.OSCA.Certificate, time.Now())
	if err != nil {
		t.Fatalf("node certificate: %v", err)
	}
	leaf := cert.Leaf
	if leaf.Subject.CommonName != "w1" {
		t.Errorf("node certificate for %q", leaf.Subject.CommonName)
	}
	if err := leaf.VerifyHostname("w1.lab"); err != nil {
		t.Errorf("node certificate: %v", err)
	}
	if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP("10.0.0.21")) {
		t.Errorf("node certificate addresses = %v, want the static address", leaf.IPAddresses)
	}
	if string(got.CA) != secrets.OSCABundle() {
		t.Error("the OS CA bundle differs")
	}
	key, _ := clustersecrets.RecoveryKey(secrets.RecoverySecret, "w1")
	if got.FallbackSecret != key {
		t.Errorf("fallback secret = %q, want the node's recovery key", got.FallbackSecret)
	}
	for _, d := range got.Config.Delivered {
		if d.Kind == "Addon" {
			t.Errorf("the worker gets the Addon %s", d.Name)
		}
	}
	if len(got.Layers) != 1 {
		t.Errorf("%d layer payloads, want the Kubernetes share", len(got.Layers))
	}
}

func TestInstallRefusesConfigTheImageRefuses(t *testing.T) {
	secrets := generateSecrets(t)
	called := false
	s := sdktest.Maintenance()
	s.InPlace = func(context.Context, install.Request, []byte) error { called = true; return nil }
	conn := dialInstall(t, s, secrets)
	docs := parseDocuments(t, w1SystemDisk, w1Network, kubetest.NodeDoc("default", "{type: bogus}"))
	err := sdk.Install(context.Background(), conn, installRequest(secrets, docs))
	if r := (*check.Refusal)(nil); !errors.As(err, &r) {
		t.Fatalf("install = %v, want a refusal", err)
	}
	if called {
		t.Error("installed a config the image refuses")
	}
}

func TestInstallTwiceContinues(t *testing.T) {
	secrets := generateSecrets(t)
	calls := 0
	s := sdktest.Maintenance()
	s.InPlace = func(context.Context, install.Request, []byte) error {
		calls++
		if calls == 1 {
			return errors.New("the disk went away")
		}
		return nil
	}
	conn := dialInstall(t, s, secrets)
	r := installRequest(secrets, parseDocuments(t, w1SystemDisk, w1Network))
	if err := sdk.Install(context.Background(), conn, r); err == nil || !strings.Contains(err.Error(), "the disk went away") {
		t.Fatalf("first install = %v, want the node's error", err)
	}
	if err := sdk.Install(context.Background(), conn, r); err != nil {
		t.Fatalf("second install = %v", err)
	}
	if calls != 2 {
		t.Errorf("%d installs, want 2", calls)
	}
}

// TestInstallSendsThePassword installs a node whose storage falls back to a password, which it
// refuses without one and enrolls as the fallback secret with one.
func TestInstallSendsThePassword(t *testing.T) {
	secrets := generateSecrets(t)
	var got install.Request
	s := sdktest.Maintenance()
	s.InPlace = func(_ context.Context, r install.Request, _ []byte) error { got = r; return nil }
	conn := dialInstall(t, s, secrets)
	disk := "apiVersion: storage.chalkos.dev/v1alpha1\nkind: SystemDisk\nmetadata: {name: system}\nspec: {disk: {serial: chalk-target}, encryption: {fallback: password}}\n"
	r := installRequest(secrets, parseDocuments(t, disk, w1Network))
	err := sdk.Install(context.Background(), conn, r)
	if err == nil || !strings.Contains(err.Error(), "unlocks with a password when its TPM fails, but the install request has none") {
		t.Fatalf("install = %v, want the missing password named", err)
	}
	r.Password = "correct horse"
	if err := sdk.Install(context.Background(), conn, r); err != nil {
		t.Fatal(err)
	}
	if got.FallbackSecret != "correct horse" {
		t.Errorf("fallback secret = %q, want the password", got.FallbackSecret)
	}
}

func TestNodeNames(t *testing.T) {
	names := sdk.NodeNames("w1", parseDocuments(t, w1Network, w1Host))
	if names.CommonName != "w1" || strings.Join(names.DNSNames, ",") != "w1,w1.lab" || len(names.IPs) != 1 || !names.IPs[0].Equal(net.ParseIP("10.0.0.21")) {
		t.Errorf("names = %+v", names)
	}
	if names := sdk.NodeNames("w1", parseDocuments(t, strings.Replace(w1Host, "w1.lab", "w1", 1))); strings.Join(names.DNSNames, ",") != "w1" {
		t.Errorf("a hostname that is the name is named twice: %v", names.DNSNames)
	}
}

func generateSecrets(t *testing.T) clustersecrets.Secrets {
	t.Helper()
	secrets, err := clustersecrets.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return secrets
}

// dialInstall serves s, a node in maintenance mode whose image trusts the OS CA of secrets, and
// dials it with an admin certificate of that CA.
func dialInstall(t *testing.T, s *chalkd.Server, secrets clustersecrets.Secrets) *client.Conn {
	t.Helper()
	node := sdktest.Serve(t, s, secrets.OSCA, secrets.NodeCA, "", nil)
	cert, err := sdk.AdminCertificate(secrets, "sdk-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	conn, _, _, err := sdk.DialMaintenance(context.Background(), node.Addr, sdk.Trust{Fingerprint: node.Fingerprint}, cert)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// installRequest installs w1 with docs and the Kubernetes layer, issued at the time it is sent.
func installRequest(secrets clustersecrets.Secrets, docs []config.Document) sdk.InstallRequest {
	return sdk.InstallRequest{
		Node: "w1", Documents: docs, Secrets: secrets,
		Layers: []sdk.Layer{sdkkubernetes.Layer{}},
	}
}

func parseDocuments(t *testing.T, docs ...string) []config.Document {
	t.Helper()
	parsed, err := config.Parse(strings.NewReader(strings.Join(docs, "---\n")))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// kubernetesImage gives the node s, whose image has no Kubernetes layer, as in maintenance mode, a
// running image with Kubernetes: its image.json and the Kubernetes layer that reads it, whose files
// are in a directory of the test.
func kubernetesImage(t *testing.T, s *chalkd.Server) {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "image.json")
	if err := os.WriteFile(file, []byte(kubernetestest.Image), 0o600); err != nil {
		t.Fatal(err)
	}
	k := kubernetesplugin.New(s, kubernetesplugin.Options{
		Paths: knode.Paths{
			StateDir: filepath.Join(root, "state"), Cluster: filepath.Join(root, "cluster.json"), NodeFile: filepath.Join(root, "node.json"),
			RunDir: filepath.Join(root, "run"), PKI: filepath.Join(root, "pki"), KubeletPKI: filepath.Join(root, "kubelet"), EtcdData: filepath.Join(root, "etcd"),
		},
		ImageFile:   file,
		ClusterFile: filepath.Join(root, "cluster.json"),
	})
	s.Plugins = chalkdtest.Plugins(t, s, layers.Of(layers.Kubernetes), k)
	s.Loader.Layers = layer.Active[nodeconfig.Layer](s.Plugins)
}

// TestInstallFromAnOCIImageSendsTheSource installs in place from an image in a registry: the
// config is checked against the image's contract layer, whose schemas take a Network that the
// running image's do not, and the header the node receives describes the image without the boot
// loader the node keeps and names it by its digest. A reference by tag and an image that carries
// another OS CA than the secrets' are refused before the node gets anything.
func TestInstallFromAnOCIImageSendsTheSource(t *testing.T) {
	secrets := generateSecrets(t)
	host := ocitest.Registry(t)
	root := strings.Repeat("cd", 32)
	md := `{"schemaVersion": 0, "id": "chalkos", "name": "test-metal", "platform": "metal", "rootHash": "` + root + `", "kinds": []}`
	img := ocitest.Image(t, ociimage.Header{ImageID: "chalkos", RootHash: root, Architecture: "x86-64", Platform: "metal", Tag: "v2"},
		ociimage.Contract{Metadata: []byte(md), Schemas: schematest.Files(t, schematest.Builtin, schematest.Networkd), Definitions: map[string][]byte{"50-state.conf": []byte("[Partition]\nLabel=state\n")}})
	ref := ocitest.Push(t, img, host+"/lab/test-metal:v2")

	s := sdktest.Maintenance()
	running := t.TempDir()
	schematest.Copy(t, filepath.Join(running, imagecontract.SchemasDir), schematest.Builtin)
	sdktest.LoadContract(t, s, running)
	var got *nodev1.InstallHeader
	fake := fakeInstall{got: &got}
	node := sdktest.Serve(t, s, secrets.OSCA, secrets.NodeCA, "", func(h http.Handler) http.Handler {
		server := connect.NewServer()
		nodev1connect.RegisterNodeServiceHandler(server, fake)
		installs := http.NewServeMux()
		connecthttp.Mount(installs, server)
		mux := http.NewServeMux()
		mux.Handle(nodev1connect.NodeServiceInstallProcedure, installs)
		mux.Handle("/", h)
		return mux
	})
	cert, err := sdk.AdminCertificate(secrets, "sdk-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	conn, _, _, err := sdk.DialMaintenance(context.Background(), node.Addr, sdk.Trust{Fingerprint: node.Fingerprint}, cert)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	r := installRequest(secrets, parseDocuments(t, w1SystemDisk, w1Network))
	r.Image = host + "/lab/test-metal:v2"
	if err := sdk.Install(context.Background(), conn, r); err == nil || !strings.Contains(err.Error(), "by digest") || got != nil {
		t.Fatalf("install by tag = %v, sent %v; want it refused", err, got)
	}
	osCA, err := json.Marshal(generateSecrets(t).OSCA.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	r.Image = ocitest.Push(t, ocitest.Image(t, ociimage.Header{ImageID: "chalkos", RootHash: root, Architecture: "x86-64", Platform: "metal", Tag: "v2"},
		ociimage.Contract{Metadata: []byte(md[:len(md)-1] + `, "osCA": ` + string(osCA) + `}`), Schemas: schematest.Files(t, schematest.Builtin, schematest.Networkd),
			Definitions: map[string][]byte{"50-state.conf": []byte("[Partition]\nLabel=state\n")}}),
		host+"/lab/test-metal:other-os-ca")
	if err := sdk.Install(context.Background(), conn, r); !errors.Is(err, sdk.ErrOSCARefused) || got != nil {
		t.Fatalf("install of an image of another OS CA = %v, sent %v; want ErrOSCARefused before the node gets anything", err, got)
	}
	r.Image = ref
	if err := sdk.Install(context.Background(), conn, r); err != nil {
		t.Fatal(err)
	}
	if got.GetSource().GetOci().GetReference() != ref || got.GetContract() != nil || got.GetInPlace() == nil {
		t.Errorf("header names the source %v, the contract %v, the target %v", got.GetSource(), got.GetContract(), got.GetTarget())
	}
	image := got.GetImage()
	if hex.EncodeToString(image.GetRootHash()) != root || image.GetImageId() != "chalkos" || image.GetPlatform() != "metal" || image.GetArchitecture() != "x86-64" || image.GetTag() != "v2" ||
		image.GetStore().GetSize() != uint64(len("store")) || image.GetUki().GetSize() != uint64(len("uki")) || image.GetHashTree().GetSize() != uint64(len("hash tree")) || image.GetBootLoader() != nil {
		t.Errorf("image header = %v", image)
	}
	if !strings.Contains(got.GetConfig(), `"kind":"Network"`) {
		t.Errorf("config = %q, want the Network the image's schemas take", got.GetConfig())
	}
}

// fakeInstall answers Install as a node that installed, and keeps the header it received.
type fakeInstall struct {
	nodev1connect.UnimplementedNodeServiceHandler
	got **nodev1.InstallHeader
}

func (f fakeInstall) Install(_ context.Context, stream nodev1connect.NodeServiceInstallServerStream) (*nodev1.InstallResponse, error) {
	msg, err := stream.Receive()
	if err != nil {
		return nil, err
	}
	*f.got = msg.GetHeader()
	return &nodev1.InstallResponse{}, nil
}
