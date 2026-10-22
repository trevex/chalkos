package chalkctl

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
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

// TestOCIImageIsNotInstalled refuses to install a node from its image in a registry, which
// install cannot send, before it reaches the node or the registry.
func TestOCIImageIsNotInstalled(t *testing.T) {
	ta := newTestApp(t)
	useOCIImage(t, ta, "registry.example/lab/test-metal:v1")
	c, err := loadInventory(context.Background(), ta.inventory)
	if err != nil {
		t.Fatal(err)
	}
	want := "the image test-metal of n1 comes from the registry, registry.example/lab/test-metal:v1, but chalkctl install sends only an image from a directory"
	if _, err := c.imageDir("n1"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("imageDir = %v", err)
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
