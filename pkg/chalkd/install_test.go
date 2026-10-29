package chalkd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// installConfig is baseConfig for a node whose system disk has the serial chalk-target.
var installConfig = strings.Replace(baseConfig, "spec: {disk: {path: /dev/vda}}", "spec: {disk: {serial: chalk-target}}", 1)

// nodeChain is a node certificate for name from a node CA of the test OS CA, followed by the node
// CA's, as chalkctl issues it into an install header.
func nodeChain(t *testing.T, name string) []byte {
	t.Helper()
	nodeCA := testNodeCA(t)
	cert, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: name, DNSNames: []string{name}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return []byte(cert.Certificate)
}

func header(t *testing.T, target any) *nodev1.InstallHeader {
	t.Helper()
	h := &nodev1.InstallHeader{
		Config:          installConfig,
		NodeCertificate: nodeChain(t, "n1"),
		NodeKey:         []byte("key"),
		CaCertificate:   []byte("ca"),
		FallbackSecret:  "recovery",
		Layers:          shareLayers(t, []byte("share")),
	}
	switch t := target.(type) {
	case *nodev1.InstallHeader_InPlace:
		h.Target = t
	case *nodev1.InstallHeader_Disk:
		h.Target = t
	}
	return h
}

func sendInstall(t *testing.T, s *Server, h *nodev1.InstallHeader, image []byte) error {
	t.Helper()
	c := newCreds(t)
	conn := dial(t, serve(t, s, c, nil), nil)
	stream := conn.Install(context.Background())
	if err := stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Header{Header: h}}); err != nil {
		t.Fatal(err)
	}
	for len(image) > 0 {
		n := min(len(image), 1000)
		if err := stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Chunk{Chunk: &nodev1.ImageChunk{Data: image[:n]}}}); err != nil {
			t.Fatal(err)
		}
		image = image[n:]
	}
	_, err := stream.CloseAndReceive()
	return err
}

func TestInstallInPlace(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	var got install.Request
	var gotHash []byte
	s.InPlace = func(_ context.Context, req install.Request, rootHash []byte) error {
		got, gotHash = req, rootHash
		return nil
	}
	rebooted := make(chan struct{})
	s.RebootNode = func() { close(rebooted) }

	if err := sendInstall(t, s, header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}), nil); err != nil {
		t.Fatal(err)
	}
	if got.Config.Version != hashOf(t, installConfig) || !bytes.Equal(gotHash, bytes.Repeat([]byte{0xab}, 32)) || string(got.NodeKey) != "key" || string(got.CA) != "ca" || got.FallbackSecret != "recovery" || string(got.KubernetesShare) != "share" ||
		got.Section.Disks[storage.SystemDisk].Ref.Selector.Serial != "chalk-target" {
		t.Errorf("install request = %+v", got)
	}
	select {
	case <-rebooted:
	case <-time.After(time.Minute):
		t.Fatal("the node did not reboot after installing")
	}
	err := sendInstall(t, s, header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}), nil)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("second install: %v", err)
	}
}

// TestInstallTakesTheNodesNameFromTheHeadersCertificate translates the config, whose documents
// name no node, for the node the node certificate in the header names, and refuses a header whose
// certificate names no node before it translates the config.
func TestInstallTakesTheNodesNameFromTheHeadersCertificate(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request, _ []byte) error { got = req; return nil }
	h := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	h.Config = installConfig
	h.NodeCertificate = nodeChain(t, "w1")
	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	var id identity.Identity
	if err := json.Unmarshal(got.Config.Identity, &id); err != nil {
		t.Fatal(err)
	}
	if id.Hostname != "w1" {
		t.Errorf("hostname = %q, want w1, the node certificate's name", id.Hostname)
	}

	for name, cert := range map[string][]byte{
		"not a certificate": []byte("cert"),
		"without a name":    unnamedChain(t),
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestServer(t, maintenance, vda)
			s.InPlace = func(context.Context, install.Request, []byte) error { t.Error("installed"); return nil }
			h := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
			h.NodeCertificate = cert
			err := sendInstall(t, s, h, nil)
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "node certificate") {
				t.Fatalf("err = %v, want a refusal of the node certificate", err)
			}
		})
	}
}

// unnamedChain is a certificate without a common name from a node CA of the test OS CA, followed
// by the node CA's, which pki.IssueNode does not issue.
func unnamedChain(t *testing.T) []byte {
	t.Helper()
	nodeCA := testNodeCA(t)
	caCert, caKey, err := nodeCA.Parse()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{Organization: []string{string(pki.RoleNode)}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nodeCA.Certificate...)
}

func TestInstallFromParts(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	parts := bytes.Repeat([]byte("chalkos"), 5000)
	var got install.PartsRequest
	var streamed []byte
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		var err error
		streamed, err = io.ReadAll(req.Parts)
		return err
	}
	h := header(t, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	h.Image = &nodev1.ImageHeader{
		ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: []byte{1}, Revision: "a1b2c3d4e5f6", Tag: "v1.5.0",
		Store: &nodev1.ImagePart{Size: 1, Sha256: []byte{2}}, HashTree: &nodev1.ImagePart{Size: 3, Sha256: []byte{4}},
		Uki: &nodev1.ImagePart{Size: 5, Sha256: []byte{6}}, BootLoader: &nodev1.ImagePart{Size: 7, Sha256: []byte{8}},
	}
	contractOf(h).SystemDefinitions = map[string]string{"50-state.conf": "[Partition]\nLabel=state\n"}
	h.WipeDisk = true
	contractOf(h).Schemas = fixtureSchemas(t)

	if err := sendInstall(t, s, h, parts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(streamed, parts) {
		t.Errorf("streamed %d bytes, want the %d of the image's parts", len(streamed), len(parts))
	}
	want := upgrade.Header{
		ImageID: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: []byte{1},
		StoreSize: 1, StoreSHA256: []byte{2}, VeritySize: 3, VeritySHA256: []byte{4},
		UKISize: 5, UKISHA256: []byte{6}, BootLoaderSize: 7, BootLoaderSHA256: []byte{8},
		Provenance: upgrade.Provenance{Revision: "a1b2c3d4e5f6", Tag: "v1.5.0"},
	}
	if got.Target != (storage.Ref{Selector: storage.Selector{Serial: "chalk-target"}}) || !reflect.DeepEqual(got.Image, want) ||
		!got.WipeDisk || got.SystemDefinitions["50-state.conf"] == "" {
		t.Errorf("parts request = %+v", got)
	}
	// The node starts its record of installed images with the image.
	if want := "{\n  \"01\": {\n    \"revision\": \"a1b2c3d4e5f6\",\n    \"tag\": \"v1.5.0\"\n  }\n}\n"; string(got.Images) != want {
		t.Errorf("the record of installed images:\n%s\nwant\n%s", got.Images, want)
	}
}

// TestInstallImageMatchesTheTarget refuses an installer install without an image, and an install
// in place with one.
func TestInstallImageMatchesTheTarget(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.InPlace = func(context.Context, install.Request, []byte) error {
		t.Error("installed in place")
		return nil
	}
	in := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	in.Image = &nodev1.ImageHeader{ImageId: "chalkos"}
	if err := sendInstall(t, s, in, nil); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("in place with an image: %v", err)
	}
	s.Installer = true
	s.FromParts = func(context.Context, install.PartsRequest) error {
		t.Error("installed onto the disk")
		return nil
	}
	if err := sendInstall(t, s, header(t, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Path: "/dev/vdb"}}), nil); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("onto a disk without an image: %v", err)
	}
}

func TestInstallTargetMustMatchImage(t *testing.T) {
	for _, c := range []struct {
		name      string
		installer bool
		target    any
	}{
		{"in place on the installer", true, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}},
		{"onto a disk from the node's image", false, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Path: "/dev/vdb"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newTestServer(t, maintenance, vda)
			s.Installer = c.installer
			err := sendInstall(t, s, header(t, c.target), nil)
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestInstallFailureCanBeRepeated(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	attempts := 0
	s.InPlace = func(context.Context, install.Request, []byte) error {
		attempts++
		if attempts == 1 {
			return errors.New("enroll the fallback key on var: TPM unavailable")
		}
		return nil
	}
	var rebooted atomic.Bool
	s.RebootNode = func() { rebooted.Store(true) }
	in := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	if err := sendInstall(t, s, in, nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if rebooted.Load() {
		t.Fatal("rebooted after a failed install")
	}
	if err := sendInstall(t, s, in, nil); err != nil {
		t.Fatalf("the install could not be repeated: %v", err)
	}
}

// testImage is the image.json of a Kubernetes image without flannel.
const testImage = `{"version": "1.37.1", "cni": "none", "images": {"etcd": "e", "kubeAPIServer": "a",
  "kubeControllerManager": "c", "kubeScheduler": "s", "kubeProxy": "p", "coredns": "d", "flannel": ""}}`

// kubernetesImage gives s's running image Kubernetes: its image.json, which the Kubernetes
// layer reads. A node without a Kubernetes side, as in maintenance mode, gets the layer with one
// of its own, whose files are in a directory of the test.
func kubernetesImage(t *testing.T, s *Server) {
	t.Helper()
	write(t, s.Loader.KubernetesImage, testImage)
	if s.Kubernetes != nil {
		s.Kubernetes.imageFile = s.Loader.KubernetesImage
		return
	}
	root := t.TempDir()
	k := NewKubernetes(KubernetesOptions{
		Paths: knode.Paths{
			State: filepath.Join(root, "state"), Cluster: filepath.Join(root, "cluster.json"), NodeFile: filepath.Join(root, "node.json"),
			Run: filepath.Join(root, "run"), PKI: filepath.Join(root, "pki"), KubeletPKI: filepath.Join(root, "kubelet"), EtcdData: filepath.Join(root, "etcd"),
		},
		ImageFile: s.Loader.KubernetesImage,
	})
	reg, err := layer.NewRegistry(layers.Of(layers.Kubernetes), NewKubernetesLayer(s, k), NewRouterLayer(&Router{Run: s.Run}))
	if err != nil {
		t.Fatal(err)
	}
	s.Plugins = reg
}

// workerConfig is baseConfig for a Kubernetes worker with a fixed address.
var workerConfig = baseConfig + `---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec: {type: worker, nodeIP: {addresses: [192.168.100.21]}}
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: default}
spec: {endpoint: "https://192.168.100.11:6443"}
`

func TestInstallInPlaceWithConfig(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	writeSchemas(t, s.Loader.Schemas)
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request, _ []byte) error { got = req; return nil }
	h := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	h.Config = baseConfig

	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	var id identity.Identity
	if err := json.Unmarshal(got.Config.Identity, &id); err != nil || id.Hostname != "n1" || id.Platform != "metal" {
		t.Errorf("identity = %s, %v", got.Config.Identity, err)
	}
	if got.Section.Disks[storage.SystemDisk].Ref.Path != "/dev/vda" {
		t.Errorf("section = %+v", got.Section)
	}
	if got.Config.Version != hashOf(t, baseConfig) || len(got.Config.Documents) != 3 {
		t.Errorf("config = %+v", got.Config)
	}
}

// An install in place tried again reads every fact as null: what the attempt before it left on
// STATE, here a storage.json the rules could not read, is not the node's.
func TestInstallInPlaceReadsNoFacts(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	installed := false
	s.InPlace = func(context.Context, install.Request, []byte) error { installed = true; return nil }
	write(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), "{")
	h := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	h.Config = baseConfig
	if err := sendInstall(t, s, h, nil); err != nil || !installed {
		t.Fatalf("installed %v: %v", installed, err)
	}
}

func TestInstallFromPartsWithConfig(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	var got install.PartsRequest
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		_, err := io.ReadAll(req.Parts)
		return err
	}
	h := header(t, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	h.Config = workerConfig
	contractOf(h).Schemas = fixtureSchemas(t)
	contractOf(h).LayerFacts = factsLayers(t, testImage)
	h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal", Architecture: "x86-64"}

	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	var id identity.Identity
	if err := json.Unmarshal(got.Config.Identity, &id); err != nil || id.Platform != "metal" {
		t.Errorf("identity = %s, %v", got.Config.Identity, err)
	}
	if k := got.Kubernetes; k == nil || k.NodeName != "n1" || !slices.Equal(k.NodeIPs, []string{"192.168.100.21"}) {
		t.Errorf("Kubernetes identity = %+v", got.Kubernetes)
	}
	if got.Config.Version != hashOf(t, workerConfig) {
		t.Errorf("config = %+v", got.Config)
	}
}

func TestInstallRefusesConfig(t *testing.T) {
	schemas := fixtureSchemas(t)
	fromInstaller := func(h *nodev1.InstallHeader) {
		h.Config, contractOf(h).Schemas = workerConfig, schemas
		h.Target = &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Path: "/dev/vdb"}}
		h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal"}
	}
	for name, c := range map[string]struct {
		installer bool
		edit      func(*nodev1.InstallHeader)
		want      string
	}{
		"invalid": {false, func(h *nodev1.InstallHeader) {
			h.Config = strings.Replace(baseConfig, "{disk: {path: /dev/vda}}", "{disk: {path: vda}}", 1)
		}, `SystemDisk "system"`},
		"in place with schemas": {false, func(h *nodev1.InstallHeader) {
			h.Config, contractOf(h).Schemas = baseConfig, map[string][]byte{"x.json": []byte("{}")}
		}, "running image"},
		"from the installer without schemas": {true, func(h *nodev1.InstallHeader) {
			h.Config = baseConfig
			h.Target = &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Path: "/dev/vdb"}}
			h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal"}
		}, "schemas"},
		"without config": {false, func(h *nodev1.InstallHeader) { h.Config = "" }, "the header delivers no config"},
		"from the installer with an invalid image.json": {true, func(h *nodev1.InstallHeader) {
			fromInstaller(h)
			contractOf(h).LayerFacts = factsLayers(t, `{"version": "1.37.1"`)
		}, "image.json"},
		"from the installer with a schema outside schemas/": {true, func(h *nodev1.InstallHeader) {
			fromInstaller(h)
			contractOf(h).LayerFacts = factsLayers(t, testImage)
			contractOf(h).Schemas = map[string][]byte{"../x.json": []byte("{}")}
		}, `"../x.json" is not the name of a schema file`},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestServer(t, maintenance, vda)
			s.Installer = c.installer
			writeSchemas(t, s.Loader.Schemas)
			s.InPlace = func(context.Context, install.Request, []byte) error { t.Error("installed in place"); return nil }
			s.FromParts = func(context.Context, install.PartsRequest) error { t.Error("installed onto the disk"); return nil }
			h := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
			c.edit(h)
			if err := sendInstall(t, s, h, nil); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// TestInstallInPlaceChecksTheOutputs: an install in place renders and checks the running image's
// outputs before it installs anything, and leaves no output in place, which the boot that
// follows renders; a render that passes clears the failure the status recorded.
func TestInstallInPlaceChecksTheOutputs(t *testing.T) {
	for name, tc := range map[string]struct{ check, want string }{
		"a check that passes": {`grep -q first "$OUT/demo.json"`, ""},
		"a check that fails":  {`grep -q second "$OUT/demo.json"`, "output demo: its check refused the rendered files (exit 1)"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestServer(t, maintenance, vda)
			writeSchemas(t, s.Loader.Schemas)
			writeOutputs(t, s, map[string]outputs.Definition{"demo": demoOutput(t, s, `cat > "$OUT/demo.json"`, tc.check)})
			installed := false
			if err := outputs.RecordFailures(s.Loader.Render.Root, []outputs.Failure{{Output: "demo", Problem: "an earlier failure"}}); err != nil {
				t.Fatal(err)
			}
			s.InPlace = func(context.Context, install.Request, []byte) error { installed = true; return nil }
			h := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
			h.Config = baseConfig
			err := sendInstall(t, s, h, nil)
			if tc.want == "" && (err != nil || !installed) || tc.want != "" && (installed || connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("install: %v, installed %v; want %q", err, installed, tc.want)
			}
			if _, err := os.Lstat(filepath.Join(s.Loader.Render.Root, "demo")); !os.IsNotExist(err) {
				t.Errorf("the install swapped the output in: %v", err)
			}
			// A render that passes clears the failure an earlier one recorded.
			if st, _ := outputs.ReadStatus(s.Loader.Render.Root); (st["demo"].Error == "") != (tc.want == "") {
				t.Errorf("status = %+v", st)
			}
		})
	}
}

// contractOf is the image contract an install header carries, which it gives one first if it
// carries none.
func contractOf(h *nodev1.InstallHeader) *nodev1.ImageContract {
	if h.GetContract() == nil {
		h.Origin = &nodev1.InstallHeader_Contract{Contract: &nodev1.ImageContract{}}
	}
	return h.GetContract()
}
