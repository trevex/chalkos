package chalkd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/anypb"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/chalkdtest"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/imagecheck"
	"github.com/trevex/chalkos/pkg/imagepart"
	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// installConfig is baseConfig for a node whose system disk has the serial chalk-target.
var installConfig = strings.Replace(baseConfig, "spec: {disk: {path: /dev/vda}}", "spec: {disk: {serial: chalk-target}}", 1)

// nodeChain is a node certificate for name from a node CA of the test OS CA, followed by the node
// CA's, as chalkctl issues it into an install header.
func nodeChain(t *testing.T, name string) []byte {
	t.Helper()
	nodeCA := kubernetestest.NodeCA(t)
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
	msgs := []*nodev1.InstallRequest{{Message: &nodev1.InstallRequest_Header{Header: h}}}
	for chunk := range slices.Chunk(image, 1000) {
		msgs = append(msgs, &nodev1.InstallRequest{Message: &nodev1.InstallRequest_Chunk{Chunk: &nodev1.ImageChunk{Data: chunk}}})
	}
	_, err := sendAll(t, openInstall(t, conn), msgs...)
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
	s.RebootNode = func() error { close(rebooted); return nil }

	if err := sendInstall(t, s, header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}), nil); err != nil {
		t.Fatal(err)
	}
	if got.Config.Version != hashOf(t, installConfig) || !bytes.Equal(gotHash, bytes.Repeat([]byte{0xab}, 32)) || string(got.NodeKey) != "key" || string(got.CA) != "ca" || got.FallbackSecret != "recovery" || len(got.Layers) != 0 ||
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
	nodeCA := kubernetestest.NodeCA(t)
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
		ImageID: "chalkos", Platform: "metal", Architecture: "x86-64",
		Image:      storeslot.Image{RootHash: []byte{1}, Store: imagepart.Part{Size: 1, SHA256: []byte{2}}, Verity: imagepart.Part{Size: 3, SHA256: []byte{4}}},
		UKI:        imagepart.Part{Size: 5, SHA256: []byte{6}},
		BootLoader: imagepart.Part{Size: 7, SHA256: []byte{8}},
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
	s.RebootNode = func() error { rebooted.Store(true); return nil }
	in := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	if err := sendInstall(t, s, in, nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v", err)
	}
	if rebooted.Load() {
		t.Fatal("rebooted after a failed install")
	}
	if err := sendInstall(t, s, in, nil); err != nil {
		t.Fatalf("the install could not be repeated: %v", err)
	}
}

// kubernetesImage gives s's running image Kubernetes: its image.json, which the Kubernetes
// layer reads. A node whose layer has chalkd's own paths, as one without the layer has, gets the
// layer with files in a directory of the test.
func kubernetesImage(t *testing.T, s *Server) {
	t.Helper()
	if kubernetesState(s).Paths == knode.DefaultPaths() {
		root := t.TempDir()
		paths := kubernetestest.Paths(root, root)
		k := kubernetesplugin.New(s, kubernetesplugin.Options{Paths: paths, ImageFile: kubernetestest.ImageFile(paths), ClusterFile: paths.Cluster})
		s.Plugins = chalkdtest.Plugins(t, s, layers.Of(layers.Kubernetes), k)
		s.Loader.Layers = []nodeconfig.Layer{k}
	}
	write(t, imageFileOf(s), kubernetestest.Image)
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
spec: {endpoint: "https://192.168.100.11:6443", images: ` + kubernetestest.Images + `}
`

func TestInstallInPlaceWithConfig(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	writeSchemas(t, s)
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

// checkedFromParts is a FromParts that has the image whose store is st check the config, as
// FromParts does once it wrote slot A, and keeps the request and what the check gave.
func checkedFromParts(st dirStore, got *install.PartsRequest, checked *install.Checked) func(context.Context, install.PartsRequest) error {
	return func(ctx context.Context, req install.PartsRequest) error {
		*got = req
		if _, err := io.ReadAll(req.Parts); err != nil {
			return err
		}
		c, err := req.Check(ctx, st, "/nix/store/x")
		*checked = c
		return err
	}
}

// An install from the installer lays the target out by its own translation of the config and has
// the image's own checker check the config once its store is written: the config slot written is
// the checker's, and the share is checked against the identity it gave, by the Kubernetes layer
// compiled into the installer, which carries no layer.
func TestInstallFromPartsWithConfig(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	var got install.PartsRequest
	var checked install.Checked
	s.FromParts = checkedFromParts(imageWith(t, s, fixtureSchemas(t), kubernetestest.Image), &got, &checked)
	h := header(t, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	h.Config = workerConfig
	contractOf(h).Schemas = fixtureSchemas(t)
	contractOf(h).LayerFacts = factsLayers(t, kubernetestest.Image)
	share := kubernetestest.Share(t, k8s.KindWorker, "n1")
	h.Layers = shareLayers(t, share)
	h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: newImage}

	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	if got.Section.Disks[storage.SystemDisk].Ref.Path != "/dev/vda" || len(got.Config.Identity) != 0 || len(got.Layers) != 0 {
		t.Errorf("parts request = %+v, want the section of the installer's translation and no config of its own", got.Request)
	}
	var id identity.Identity
	if err := json.Unmarshal(checked.Config.Identity, &id); err != nil || id.Platform != "metal" {
		t.Errorf("identity = %s, %v", checked.Config.Identity, err)
	}
	if k, ok, err := k8s.NodeIdentityOf(id); !ok || err != nil || k.NodeName != "n1" || !slices.Equal(k.NodeIPs, []string{"192.168.100.21"}) {
		t.Errorf("Kubernetes identity = %+v, %v, %v", k, ok, err)
	}
	if checked.Config.Version != hashOf(t, workerConfig) || len(checked.Config.Documents) == 0 {
		t.Errorf("config = %+v", checked.Config)
	}
	if len(checked.Layers) != 1 {
		t.Fatalf("the check gave %d layer payloads, want the share", len(checked.Layers))
	}
	state := t.TempDir()
	if err := checked.Layers[0](state); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(state, "kubernetes", "share.json")); got != string(share) {
		t.Errorf("share.json = %s, want the share sent", got)
	}
}

// factProbe runs the checker as run does, after reading the fact name as the checker would.
type factProbe struct {
	run  imagecheck.Runner
	name string
	got  *any
}

func (p factProbe) Run(ctx context.Context, img imagecheck.Image, in *checkv1.Input, f imagecheck.Facts) (*checkv1.Result, error) {
	v, err := f.Fact(p.name, nil)
	if err != nil {
		return nil, err
	}
	*p.got = v
	return p.run.Run(ctx, img, in, f)
}

// An install from the installer gives the image's checker the facts of this machine that the
// image declares, which the node reads once it runs there: unlike an install in place, which reads
// every fact as null, as the node it installs on may hold what an earlier attempt left.
func TestInstallFromPartsGivesTheMachinesFacts(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	st := imageWith(t, s, fixtureSchemas(t), "")
	probe := filepath.Join(t.TempDir(), "probe.json")
	write(t, probe, `"machine"`)
	write(t, filepath.Join(string(st), "nix/store/x/etc/chalkos/facts.json"), `{"probe": {"file": "`+probe+`"}}`)
	var probed any
	s.CheckImage = factProbe{run: s.CheckImage, name: "probe", got: &probed}
	var got install.PartsRequest
	var checked install.Checked
	s.FromParts = checkedFromParts(st, &got, &checked)
	h := header(t, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	contractOf(h).Schemas = fixtureSchemas(t)
	h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: newImage}
	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	if probed != "machine" {
		t.Errorf("the checker's fact probe = %v, want the machine's", probed)
	}
}

// The image's own renderers and validators check the config an install from the installer
// delivers, which the installer cannot run: a refusal refuses the install with the image's
// message, and the node is not installed.
func TestInstallFromPartsRefusedByTheImage(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	st := imageWith(t, s, fixtureSchemas(t), "")
	demo := demoOutput(t, s, `grep -q first && exit 5; cat > "$OUT/demo.json"`, "")
	demo.Dir = "/run/chalkos/outputs/demo"
	withOutputs(t, st, map[string]outputs.Definition{"demo": demo})
	var got install.PartsRequest
	var checked install.Checked
	s.FromParts = checkedFromParts(st, &got, &checked)
	h := header(t, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	contractOf(h).Schemas = fixtureSchemas(t)
	h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: newImage}
	err := sendInstall(t, s, h, nil)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.HasSuffix(err.Error(), "install: output demo: its renderer failed (exit 5); see the node's journal") {
		t.Fatalf("install = %v, want the image's refusal", err)
	}
	if s.installed {
		t.Error("the node counts as installed")
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
			contractOf(h).LayerFacts = factsLayers(t, kubernetestest.Image)
			contractOf(h).Schemas = map[string][]byte{"../x.json": []byte("{}")}
		}, `"../x.json" is not the name of a schema file`},
		// The payloads are checked against the installer's own translation before the disk changes.
		"from the installer with a share for another node": {true, func(h *nodev1.InstallHeader) {
			fromInstaller(h)
			contractOf(h).LayerFacts = factsLayers(t, kubernetestest.Image)
			h.Layers = shareLayers(t, kubernetestest.Share(t, k8s.KindWorker, "other"))
		}, "Kubernetes share"},
		"from the installer with a payload no layer takes": {true, func(h *nodev1.InstallHeader) {
			fromInstaller(h)
			contractOf(h).LayerFacts = factsLayers(t, kubernetestest.Image)
			h.Layers = []*anypb.Any{{TypeUrl: "type.googleapis.com/chalkos.unknown.v1.Payload"}}
		}, "which no layer of this image takes"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestServer(t, maintenance, vda)
			s.Installer = c.installer
			writeSchemas(t, s)
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
			writeSchemas(t, s)
			writeOutputs(t, s, map[string]outputs.Definition{"demo": demoOutput(t, s, `cat > "$OUT/demo.json"`, tc.check)})
			installed := false
			if err := outputs.RecordFailures(s.Loader.Render.Root, []outputs.Failure{{Name: "demo", Role: outputs.RoleOutput, Problem: "an earlier failure"}}); err != nil {
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

// anonymousRegistry is a Registry of the host without credentials, which allows plain HTTP, as
// the test registries serve it.
func anonymousRegistry(host string) string {
	return fmt.Sprintf("apiVersion: oci.chalkos.dev/v1alpha1\nkind: Registry\nmetadata: {name: lab}\nspec: {host: %q, insecure: true}\n", host)
}

// inPlaceFromRegistry pushes a small chalkos image with the root hash newImage, which the node does
// not run, and a contract of the fixture schemas to the registry at host. It returns the header of
// an install in place that pulls the image, as the SDK sends it: baseConfig with a Registry of the
// host, and the image's store, hash tree and UKI, without the boot loader the node keeps.
func inPlaceFromRegistry(t *testing.T, host string) *nodev1.InstallHeader {
	t.Helper()
	root := hex.EncodeToString(newImage)
	md := `{"schemaVersion": 0, "id": "chalkos", "name": "node-metal", "platform": "metal", "rootHash": "` + root + `", "kinds": []}`
	img := ocitest.Image(t, ociimage.Header{ImageID: "chalkos", RootHash: root, Architecture: "x86-64", Platform: "metal", Revision: "a1b2c3d4e5f6", Tag: "v2"},
		ociimage.Contract{Metadata: []byte(md), Schemas: fixtureSchemas(t), Definitions: map[string][]byte{"50-state.conf": []byte("[Partition]\nLabel=state\n")}})
	ref := ocitest.Push(t, img, host+"/lab/node-metal:v2")
	h := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	h.Config = baseConfig + "---\n" + anonymousRegistry(host)
	h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: newImage, Revision: "a1b2c3d4e5f6", Tag: "v2",
		Store: imagePart("store"), HashTree: imagePart("hash tree"), Uki: imagePart("uki")}
	h.Origin = &nodev1.InstallHeader_Source{Source: &nodev1.ImageSource{Source: &nodev1.ImageSource_Oci{Oci: &nodev1.RegistryImage{Reference: ref}}}}
	return h
}

// placement is what an install in place did to the fake inactive slot placeOn gives s: the header
// of the image it placed there, the bytes it read for it, and the image it activated.
type placement struct {
	header    upgrade.Header
	data      []byte
	activated []byte
}

// placeOn gives s an inactive slot whose image's store is st: Place reads the whole image, as the
// slot writer does, and has the image's checker check the config there, and ActivatePlaced
// activates the image once STATE keeps its config slot, as the boot disk's do. Its InPlace does
// on STATE what install.Installer.InPlace does there: it has the request place the image, writes
// the config slot the placed image's checker gave for the root hash, has the request activate the
// image and marks the node installed last.
func placeOn(t *testing.T, s *Server, st dirStore) *placement {
	t.Helper()
	p := &placement{}
	f := fakeImageSlots(t, s)
	f.place = func(_ context.Context, h upgrade.Header, check func(imageStore, string) error, image io.Reader) (upgrade.Result, error) {
		data, err := io.ReadAll(image)
		if err != nil {
			return upgrade.Result{}, err
		}
		if err := check(st, "/nix/store/x"); err != nil {
			return upgrade.Result{}, &upgrade.CheckError{Err: err}
		}
		p.header, p.data = h, data
		return upgrade.Result{Entry: ".staged-chalkos_2.efi", Wrote: true}, nil
	}
	f.activateIn = func(_ context.Context, rootHash []byte) (upgrade.Result, error) {
		if !s.keepsSlot(rootHash) {
			return upgrade.Result{}, errors.New("STATE holds no config for the image")
		}
		p.activated = rootHash
		return upgrade.Result{Entry: "chalkos_2+3.efi", Active: true}, nil
	}
	s.InPlace = func(ctx context.Context, req install.Request, rootHash []byte) error {
		if req.Place != nil {
			checked, err := req.Place(ctx)
			if err != nil {
				return err
			}
			req.Config = checked.Config
		}
		if err := slot.Write(slot.Dir(s.Loader.Slots, rootHash), req.Config); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord), req.Images, 0o644); err != nil {
			return err
		}
		if req.ActivateImage != nil {
			if err := req.ActivateImage(ctx); err != nil {
				return err
			}
		}
		return os.WriteFile(filepath.Join(s.Paths.StateDir, "installed"), []byte("now\n"), 0o644)
	}
	return p
}

// TestInstallInPlaceFromRegistryBootsThePulledImage installs in place from an image in a registry
// that the node does not run: the image goes into the inactive slot through the slot writer,
// its config slot holds the delivered config with the identity its checker gave, STATE records
// its provenance, and it boots next once STATE is written, before the node counts as installed.
func TestInstallInPlaceFromRegistryBootsThePulledImage(t *testing.T) {
	host := ocitest.Registry(t)
	s, _ := newTestServer(t, maintenance, vda)
	p := placeOn(t, s, imageWith(t, s, fixtureSchemas(t), ""))
	rebooted := make(chan struct{})
	s.RebootNode = func() error { close(rebooted); return nil }
	h := inPlaceFromRegistry(t, host)

	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	if string(p.data) != "storehash treeuki" || !bytes.Equal(p.header.RootHash, newImage) || p.header.BootLoader.Size != 0 {
		t.Errorf("placed %q for %+v", p.data, p.header)
	}
	if !bytes.Equal(p.activated, newImage) {
		t.Errorf("activated %x, want the pulled image", p.activated)
	}
	stored, found, err := slot.Read(slot.Dir(s.Loader.Slots, newImage))
	if err != nil || !found || stored.Version != hashOf(t, h.Config) {
		t.Errorf("the pulled image's config slot = %+v, %v, %v", stored, found, err)
	}
	var id identity.Identity
	if err := json.Unmarshal(stored.Identity, &id); err != nil || id.Hostname != "n1" || id.Platform != "metal" {
		t.Errorf("identity = %s, %v", stored.Identity, err)
	}
	if prov := upgrade.ReadImages(filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord)).Of(newImage); prov != (upgrade.Provenance{Revision: "a1b2c3d4e5f6", Tag: "v2"}) {
		t.Errorf("the record of installed images names %+v", prov)
	}
	if installed, err := install.Installed(s.Paths.StateDir); err != nil || !installed {
		t.Errorf("installed %v, %v", installed, err)
	}
	select {
	case <-rebooted:
	case <-time.After(time.Minute):
		t.Fatal("the node did not reboot after installing")
	}
}

// TestInstallInPlaceFromRegistryCheckedByThePulledImage has the pulled image's checker check the
// config, not the running image's: the running image has no schemas, so it would refuse every
// document, while the pulled image's take the config. A config the pulled image's renderer
// refuses fails with its refusal and leaves the node uninstalled and the image inactive.
func TestInstallInPlaceFromRegistryCheckedByThePulledImage(t *testing.T) {
	host := ocitest.Registry(t)
	t.Run("accepted", func(t *testing.T) {
		s, _ := newTestServer(t, maintenance, vda)
		p := placeOn(t, s, imageWith(t, s, fixtureSchemas(t), ""))
		if err := sendInstall(t, s, inPlaceFromRegistry(t, host), nil); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(p.activated, newImage) {
			t.Errorf("activated %x, want the pulled image", p.activated)
		}
	})
	t.Run("refused", func(t *testing.T) {
		s, _ := newTestServer(t, maintenance, vda)
		st := imageWith(t, s, fixtureSchemas(t), "")
		demo := demoOutput(t, s, `grep -q first && exit 5; cat > "$OUT/demo.json"`, "")
		demo.Dir = "/run/chalkos/outputs/demo"
		withOutputs(t, st, map[string]outputs.Definition{"demo": demo})
		p := placeOn(t, s, st)
		err := sendInstall(t, s, inPlaceFromRegistry(t, host), nil)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "output demo: its renderer failed (exit 5)") {
			t.Fatalf("install = %v, want the pulled image's refusal", err)
		}
		if installed, err := install.Installed(s.Paths.StateDir); err != nil || installed || s.installed {
			t.Errorf("installed %v, %v", installed, err)
		}
		if p.activated != nil {
			t.Errorf("activated %x", p.activated)
		}
	})
}

// TestInstallInPlaceFromRegistryResumes repeats an install whose pull failed part of the way, as
// the registry did not serve the UKI: the node is not installed and no image is activated, and
// the install repeated once the registry serves it installs the node.
func TestInstallInPlaceFromRegistryResumes(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	uki := fmt.Sprintf("/blobs/sha256:%x", sha256.Sum256([]byte("uki")))
	host := ocitest.RegistryWith(t, func(w http.ResponseWriter, r *http.Request) bool {
		if failing.Load() && strings.HasSuffix(r.URL.Path, uki) {
			http.Error(w, "gone", http.StatusNotFound)
			return true
		}
		return false
	})
	s, _ := newTestServer(t, maintenance, vda)
	p := placeOn(t, s, imageWith(t, s, fixtureSchemas(t), ""))
	h := inPlaceFromRegistry(t, host)
	if err := sendInstall(t, s, h, nil); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), h.GetSource().GetOci().GetReference()) {
		t.Fatalf("install with the UKI missing = %v, want a failure naming the reference", err)
	}
	if installed, err := install.Installed(s.Paths.StateDir); err != nil || installed || s.installed || p.activated != nil {
		t.Fatalf("after the failed pull: installed %v, %v; activated %x", installed, err, p.activated)
	}
	failing.Store(false)
	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	if installed, err := install.Installed(s.Paths.StateDir); err != nil || !installed || !bytes.Equal(p.activated, newImage) {
		t.Errorf("after the repeated install: installed %v, %v; activated %x", installed, err, p.activated)
	}
}

// TestInstallInPlaceFromRegistryOfTheRunningImage installs in place as without an image when the
// registry's image is the one the node runs: the running image checks the config, and nothing is
// written into the inactive slot.
func TestInstallInPlaceFromRegistryOfTheRunningImage(t *testing.T) {
	host := ocitest.Registry(t)
	s, _ := newTestServer(t, maintenance, vda)
	writeSchemas(t, s)
	fakeImageSlots(t, s).place = func(context.Context, upgrade.Header, func(imageStore, string) error, io.Reader) (upgrade.Result, error) {
		t.Error("placed the running image")
		return upgrade.Result{}, nil
	}
	var gotHash []byte
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request, rootHash []byte) error {
		got, gotHash = req, rootHash
		return nil
	}
	h := pulledInstall(t, host, anonymousRegistry(host))
	h.Config = baseConfig + "---\n" + anonymousRegistry(host)
	h.Target = &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}
	h.Image.BootLoader = nil
	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotHash, bytes.Repeat([]byte{0xab}, 32)) || got.Place != nil || got.ActivateImage != nil || got.Config.Version != hashOf(t, h.Config) {
		t.Errorf("installed %x with %+v", gotHash, got)
	}
}

// TestInstallInPlaceFromRegistryRepeatedAfterActivation repeats an install in place that lost
// power after it activated the pulled image and before it marked the node installed: the node
// boots the pulled image in maintenance mode, so the repeated install names the image the node
// runs. The running image checks the config, nothing goes into the inactive slot again, and the
// install writes the image's config slot and marks the node installed.
func TestInstallInPlaceFromRegistryRepeatedAfterActivation(t *testing.T) {
	host := ocitest.Registry(t)
	s, _ := newTestServer(t, maintenance, vda)
	p := placeOn(t, s, imageWith(t, s, fixtureSchemas(t), ""))
	s.RebootNode = func() error { return nil }
	h := inPlaceFromRegistry(t, host)
	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.activated, newImage) {
		t.Fatalf("activated %x, want the pulled image", p.activated)
	}

	// The power fails before the node is marked installed; it boots the pulled image.
	if err := os.Remove(filepath.Join(s.Paths.StateDir, "installed")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(slot.Dir(s.Loader.Slots, newImage)); err != nil {
		t.Fatal(err)
	}
	s.installed = false
	write(t, s.Paths.Cmdline, "usrhash="+hex.EncodeToString(newImage)+"\n")
	writeSchemas(t, s)
	fakeImageSlots(t, s).place = func(context.Context, upgrade.Header, func(imageStore, string) error, io.Reader) (upgrade.Result, error) {
		t.Error("placed the running image again")
		return upgrade.Result{}, errors.New("placed")
	}
	p.activated = nil

	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatalf("the repeated install = %v", err)
	}
	if p.activated != nil {
		t.Errorf("activated %x again", p.activated)
	}
	stored, found, err := slot.Read(slot.Dir(s.Loader.Slots, newImage))
	if err != nil || !found || stored.Version != hashOf(t, h.Config) {
		t.Errorf("the running image's config slot = %+v, %v, %v", stored, found, err)
	}
	if installed, err := install.Installed(s.Paths.StateDir); err != nil || !installed {
		t.Errorf("installed %v, %v", installed, err)
	}
}

// TestInstallInPlaceStillRefusesAStreamedImage refuses an install in place that describes an
// image it does not name in a registry: the node pulls an image it installs in place, and takes
// no chunks of one.
func TestInstallInPlaceStillRefusesAStreamedImage(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.InPlace = func(context.Context, install.Request, []byte) error { t.Error("installed in place"); return nil }
	h := header(t, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: newImage,
		Store: imagePart("store"), HashTree: imagePart("hash tree"), Uki: imagePart("uki")}
	if err := sendInstall(t, s, h, []byte("storehash treeuki")); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "an install in place takes no streamed image") {
		t.Errorf("install = %v, want the streamed image refused", err)
	}
}

// TestInstallInPlaceFromRegistryReadsNoFacts has the pulled image's checker read every fact as
// null, as the running image's check of an install in place does: a fact of a file this machine
// holds reads null, as what an earlier attempt left on STATE is not the node's.
func TestInstallInPlaceFromRegistryReadsNoFacts(t *testing.T) {
	host := ocitest.Registry(t)
	s, _ := newTestServer(t, maintenance, vda)
	st := imageWith(t, s, fixtureSchemas(t), "")
	probe := filepath.Join(t.TempDir(), "probe.json")
	write(t, probe, `"machine"`)
	write(t, filepath.Join(string(st), "nix/store/x/etc/chalkos/facts.json"), `{"probe": {"file": "`+probe+`"}}`)
	probed := any("unread")
	s.CheckImage = factProbe{run: s.CheckImage, name: "probe", got: &probed}
	placeOn(t, s, st)
	if err := sendInstall(t, s, inPlaceFromRegistry(t, host), nil); err != nil {
		t.Fatal(err)
	}
	if probed != nil {
		t.Errorf("the pulled image's checker read the fact probe as %v, want null", probed)
	}
}

// TestPlaceTakesNoLockItsCallerHolds places and activates an image on the boot disk while the
// caller holds s.mu, as Install does: the partition table is read under that lock without taking
// it again, and the fake sfdisk, which prints no table, ends both with an error.
func TestPlaceTakesNoLockItsCallerHolds(t *testing.T) {
	s, r := newTestServer(t, maintenance, vda)
	write(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_IMAGE=worker-metal\nCHALKOS_PLATFORM=metal\n")
	write(t, s.Paths.Cmdline, "usrhash="+strings.Repeat("ab", 32)+"\n")
	s.Run = lockWatcher{r, func(args []string) {
		if s.mu.TryLock() {
			s.mu.Unlock()
			t.Errorf("sfdisk %v ran without the storage lock", args)
		}
	}}
	part := imagepart.Part{Size: 1, SHA256: bytes.Repeat([]byte{1}, 32)}
	h := upgrade.Header{ImageID: "chalkos", Platform: "metal", Architecture: "x86-64",
		Image: storeslot.Image{RootHash: newImage, Store: part, Verity: part}, UKI: part}
	s.mu.Lock()
	defer s.mu.Unlock()
	check := func(imageStore, string) error { t.Error("checked an image never written"); return nil }
	if _, err := (bootDisk{s}).Place(context.Background(), h, check, bytes.NewReader(nil)); err == nil {
		t.Error("the image was placed without a partition table")
	}
	if _, err := (bootDisk{s}).ActivatePlaced(context.Background(), newImage); err == nil {
		t.Error("an image was activated without one staged")
	}
}
