package chalkctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

func TestInstallInPlace(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request, _ []byte) error { got = req; return nil }
	addr := ta.startNode(t, s)

	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--insecure"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stderr.String(), "fingerprint is "+s.Fingerprint) {
		t.Errorf("stderr = %q, want the observed fingerprint", ta.stderr)
	}
	if !strings.Contains(ta.stdout.String(), "n1 is installed and reboots") {
		t.Errorf("stdout = %q", ta.stdout)
	}
	want := ta.configVersion(t)
	if got.Config.Version != want || got.Section.Disks[storage.SystemDisk].Ref.Selector.Serial != "chalk-target" {
		t.Errorf("install request config = %+v, section = %+v; want the config of version %s", got.Config, got.Section, want)
	}
	key, _ := clustersecrets.RecoveryKey(ta.secrets.RecoverySecret, "n1")
	if got.FallbackSecret != key {
		t.Errorf("fallback secret = %q, want the node's recovery key", got.FallbackSecret)
	}
	nodeCert, err := pki.VerifyNode(string(got.NodeCertificate), string(got.NodeKey), ta.secrets.OSCA.Certificate, time.Now())
	if err != nil {
		t.Fatalf("node certificate: %v", err)
	}
	cert := nodeCert.Leaf
	if err := cert.VerifyHostname("n1"); err != nil {
		t.Errorf("node certificate: %v", err)
	}
	if issuer, _ := pki.ParseCertificate([]byte(ta.secrets.NodeCA.Certificate)); cert.CheckSignatureFrom(issuer) != nil {
		t.Error("the node certificate is not the node CA's")
	}
	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("10.0.0.11")) {
		t.Errorf("node certificate addresses = %v, want the static address", cert.IPAddresses)
	}
	if string(got.CA) != ta.secrets.OSCA.Certificate {
		t.Error("the OS CA was not sent")
	}
}

// TestInstallInPlaceRefusesAnotherPlatform refuses to install a node in place that runs an image
// of another platform than its image's.
func TestInstallInPlaceRefusesAnotherPlatform(t *testing.T) {
	ta := newTestApp(t)
	writeImageDir(t, filepath.Join(ta.dir, "images", "test-metal"), "test", "kvm", testRootHash, false)
	s := maintenanceNode()
	called := false
	s.InPlace = func(context.Context, install.Request, []byte) error { called = true; return nil }
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint}, addr))
	if want := "n1 runs an image built for metal, but its image test-metal is built for kvm; changing a node's platform is a reinstall"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("install = %v, want %q", err, want)
	}
	if called {
		t.Error("installed a node of another platform")
	}
}

func TestInstallRefusesWrongFingerprint(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	called := false
	s.InPlace = func(context.Context, install.Request, []byte) error { called = true; return nil }
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", strings.Repeat("00", 32)}, addr))
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Error("installed a node whose fingerprint did not match")
	}
	if err := ta.run(context.Background(), ta.args([]string{"install", "n1"}, addr)); err == nil || !strings.Contains(err.Error(), "--fingerprint") {
		t.Errorf("err = %v, want a demand for --fingerprint or --insecure", err)
	}
}

// TestInstallFromPartsStreamsTheImage sends the installer the image's store, hash tree, UKI and
// boot loader with the header describing them, and the image's definitions. The image may be
// another than the node's image in the inventory: the installer checks the config against the
// image's schemas.
func TestInstallFromPartsStreamsTheImage(t *testing.T) {
	ta := newTestApp(t)
	imageDir := testUpgradeImage(t, "gpu", "0.1.0")
	copySchemas(t, filepath.Join(imageDir, "schemas"))
	writeFile(t, filepath.Join(imageDir, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
	// The build named its revision and tag, which the node records.
	md, err := os.ReadFile(filepath.Join(imageDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(imageDir, "metadata.json"), strings.Replace(string(md), `"kinds"`, `"revision": "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "tag": "v1.5.0", "kinds"`, 1))
	s := maintenanceNode()
	s.Installer = true
	var got install.PartsRequest
	var streamed []byte
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		var err error
		streamed, err = io.ReadAll(req.Parts)
		return err
	}
	addr := ta.startNode(t, s)
	fp := s.Fingerprint

	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", fp, "--image", imageDir}, addr)); err != nil {
		t.Fatal(err)
	}
	h := got.Image
	if h.ImageID != "chalkos" || len(h.RootHash) != 32 || h.Platform != "metal" || h.BootLoaderSize == 0 ||
		h.Provenance != (upgrade.Provenance{Revision: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Tag: "v1.5.0"}) {
		t.Errorf("image = %+v", h)
	}
	if want := "chalkos " + imageName(h.RootHash, h.Provenance.Revision, h.Provenance.Tag) + ", sending"; !strings.Contains(ta.stdout.String(), want) || !strings.Contains(want, "(v1.5.0, a1b2c3d4e5f6)") {
		t.Errorf("output lacks %q:\n%s", want, ta.stdout)
	}
	size := h.StoreSize + h.VeritySize + h.UKISize + h.BootLoaderSize
	loader := sha256.Sum256(streamed[max(len(streamed)-int(h.BootLoaderSize), 0):])
	if int64(len(streamed)) != size || !bytes.Equal(loader[:], h.BootLoaderSHA256) {
		t.Errorf("streamed %d bytes, want %d ending in the boot loader", len(streamed), size)
	}
	if !strings.Contains(ta.stdout.String(), fmt.Sprintf("sending %d bytes", size)) {
		t.Errorf("chalkctl printed %q, not the bytes it sends", ta.stdout)
	}
	if got.Target != (storage.Ref{Selector: storage.Selector{Serial: "chalk-target"}}) || got.WipeDisk {
		t.Errorf("target = %+v, wipe = %v", got.Target, got.WipeDisk)
	}
	if !reflect.DeepEqual(got.SystemDefinitions, map[string]string{"50-state.conf": "[Partition]\nLabel=state\n"}) {
		t.Errorf("definitions = %v", got.SystemDefinitions)
	}
}

// TestInstallChecksThePlatform refuses an image of another platform than the node's image in
// the inventory before the installer is sent anything.
func TestInstallChecksThePlatform(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	s.Installer = true
	sent := false
	s.FromParts = func(context.Context, install.PartsRequest) error {
		sent = true
		return nil
	}
	addr := ta.startNode(t, s)
	img := testUpgradeImageOfBlocks(t, "test", "kvm", "0.1.0", 512)
	writeFile(t, filepath.Join(img, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
	copySchemas(t, filepath.Join(img, "schemas"))
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint, "--image", img}, addr))
	if err == nil || !strings.Contains(err.Error(), "n1 runs on metal; the image is built for kvm") {
		t.Errorf("install = %v, want the platform refused", err)
	}
	if sent {
		t.Error("the installer was sent the image")
	}
}

func TestInstallReportsNodeRefusal(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	s.InPlace = func(context.Context, install.Request, []byte) error {
		return errors.New("the identity places the system on /dev/vdb, but the node runs from /dev/vda")
	}
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--insecure"}, addr))
	if err == nil || !strings.Contains(err.Error(), "runs from /dev/vda") {
		t.Fatalf("err = %v", err)
	}
}

// TestInstallInPlaceOnAnImageOfAnotherVariant installs a node in place that runs an image of
// another variant than its image's: the node checks the config against its image's schemas.
func TestInstallInPlaceOnAnImageOfAnotherVariant(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	called := false
	s.InPlace = func(context.Context, install.Request, []byte) error { called = true; return nil }
	s.Paths.OSRelease = filepath.Join(t.TempDir(), "os-release")
	writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=w\nCHALKOS_PLATFORM=metal\n")
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint}, addr)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("the node was not installed in place")
	}
}

// TestInstallFromPartsSendsTheImageContract sends the installer the schemas and image.json
// of the image it installs, which it translates the config for.
func TestInstallFromPartsSendsTheImageContract(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, kubernetes.KindWorker)
	imageDir := testUpgradeImage(t, "gpu", "0.1.0")
	copySchemas(t, filepath.Join(imageDir, "schemas"))
	writeFile(t, filepath.Join(imageDir, "image.json"), testImageJSON)
	writeFile(t, filepath.Join(imageDir, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
	s := maintenanceNode()
	s.Installer = true
	// The installer's image carries no layer; its compiled plugins read the sent image's facts.
	withPlugins(t, s, layers.Of(), nil)
	// The installer's own image has no schemas: the config checks against those sent alone.
	s.Loader.Schemas = filepath.Join(t.TempDir(), "none")
	var got install.PartsRequest
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		_, err := io.ReadAll(req.Parts)
		return err
	}
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint, "--image", imageDir}, addr)); err != nil {
		t.Fatal(err)
	}
	if got.Config.Version == "" || got.Kubernetes == nil || got.Kubernetes.NodeName != "n1" {
		t.Errorf("install request config = %+v, kubernetes = %+v; want both translated for the image sent", got.Config, got.Kubernetes)
	}
}
