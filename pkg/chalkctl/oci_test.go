package chalkctl

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/schema"
)

// pushTestImage pushes an image whose contract is that of the image directory dir, as
// writeImageDir writes it with a repart definition added, to a registry of the test's, and
// returns its reference by tag and by digest.
func pushTestImage(t *testing.T, dir string) (ref, pinned string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "repart.d", "00-esp.conf"), "[Partition]\nType=esp\n")
	c, err := ociimage.ReadContractDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	md, err := schema.ParseMetadata(c.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	ref = ocitest.Registry(t) + "/lab/test-metal:v1"
	return ref, ocitest.Push(t, ocitest.Image(t, ociimage.Header{ImageID: "chalkos", RootHash: md.RootHash, Architecture: "x86-64", Platform: md.Platform}, c), ref)
}

// useOCIImage makes the test inventory name the image test-metal by ref instead of its directory,
// which it removes, so that whatever chalkctl reads of the image comes from the registry.
func useOCIImage(t *testing.T, ta *testApp, ref string) {
	t.Helper()
	writeFile(t, ta.inventory, strings.Replace(testInventoryYAML, "{source: {directory: images/test-metal}}", `{source: {oci: "`+ref+`"}}`, 1))
	if err := os.RemoveAll(filepath.Join(ta.dir, "images")); err != nil {
		t.Fatal(err)
	}
}

// TestValidateOCIImage checks each node's config against the schemas of the contract layer of
// its image in a registry, as against an image directory's.
func TestValidateOCIImage(t *testing.T) {
	ta := newTestApp(t)
	ta.withoutSecrets(t)
	ref, pinned := pushTestImage(t, filepath.Join(ta.dir, "images", "test-metal"))
	useOCIImage(t, ta, ref)
	if err := ta.run(context.Background(), []string{"validate", "--inventory", ta.inventory}); err != nil {
		t.Fatal(err)
	}
	if want := "n1: valid for the image test-metal at " + pinned + " (test on metal, root hash abababababab), config "; !strings.Contains(ta.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", ta.stdout, want)
	}
}

// TestInstallPullsTheImageFromTheRegistry has an installer pull the node's image in a registry:
// chalkctl sends the header the artifact gives, the boot loader included, and the reference by
// digest. It sends no chunks, schemas or repart definitions; the installer takes those from the
// contract layer and authenticates with the Registry of the node's config.
func TestInstallPullsTheImageFromTheRegistry(t *testing.T) {
	ta := newTestApp(t)
	ref, pinned := pushTestImage(t, filepath.Join(ta.dir, "images", "test-metal"))
	useOCIImage(t, ta, ref)
	host, _, _ := strings.Cut(ref, "/")
	ta.writeNode(t, n1SystemDisk, n1Network, fmt.Sprintf("apiVersion: chalkos.dev/v1alpha1\nkind: Registry\nmetadata: {name: lab}\nspec: {host: %q, insecure: true}\n", host))
	s := maintenanceNode()
	s.Installer = true
	var got install.PartsRequest
	var pulled []byte
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		var err error
		pulled, err = io.ReadAll(req.Parts)
		return err
	}
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint}, addr)); err != nil {
		t.Fatal(err)
	}
	if string(pulled) != "storehash treeukiboot loader" || got.Image.BootLoaderSize != int64(len("boot loader")) {
		t.Errorf("pulled %q for %+v", pulled, got.Image)
	}
	if !reflect.DeepEqual(got.SystemDefinitions, map[string]string{"00-esp.conf": "[Partition]\nType=esp\n"}) {
		t.Errorf("definitions = %v", got.SystemDefinitions)
	}
	if got.Config.Version != ta.configVersion(t) {
		t.Errorf("config = %+v, want the inventory's", got.Config)
	}
	if want := ", which the installer pulls from " + pinned + "\n"; !strings.Contains(ta.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", ta.stdout, want)
	}
}

// TestOCIImageNeedsAnInstallerThatPulls refuses to install a node from its image in a registry
// onto an installer that cannot pull it, before chalkctl reads the registry, and accepts one that
// can.
func TestOCIImageNeedsAnInstallerThatPulls(t *testing.T) {
	ta := newTestApp(t)
	useOCIImage(t, ta, "registry.example/lab/test-metal:v1")
	c, err := loadInventory(context.Background(), ta.inventory)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := c.inventoryImage("n1")
	if err != nil {
		t.Fatal(err)
	}
	want := "n1 runs an installer that cannot pull an image from a registry, so it cannot install the image test-metal from registry.example/lab/test-metal:v1"
	if err := pullsInstall(nc, &nodev1.InfoResponse{Installer: true}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("pullsInstall = %v", err)
	}
	if err := pullsInstall(nc, &nodev1.InfoResponse{Installer: true, PullsInstallImages: true}); err != nil {
		t.Errorf("pullsInstall of an installer that pulls = %v", err)
	}
}

// TestOCIImageContractAndStatus reads the contract apply-config checks a node's config against,
// and the root hash status compares a node's store with, from the node's image in a registry.
func TestOCIImageContractAndStatus(t *testing.T) {
	ta := newTestApp(t)
	ref, _ := pushTestImage(t, filepath.Join(ta.dir, "images", "test-metal"))
	useOCIImage(t, ta, ref)
	c, err := loadInventory(context.Background(), ta.inventory)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := c.imageContractOf("n1")
	if err != nil || ct.platform != "metal" || len(ct.files) == 0 {
		t.Errorf("imageContractOf = %+v, %v", ct, err)
	}
	same, _ := hex.DecodeString(testRootHash)
	if got := c.imageLine("n1", &nodev1.BootStatus{Running: &nodev1.BootImage{RootHash: same}}); got != "" {
		t.Errorf("the line of the image the inventory names = %q", got)
	}
	got := c.imageLine("n1", &nodev1.BootStatus{Running: &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0xcd}, 32)}})
	if !strings.Contains(got, "not that of the image test-metal the inventory names, "+testRootHash) {
		t.Errorf("the line of another image = %q", got)
	}
}
