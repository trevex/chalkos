package chalkctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/schema"
)

// pushTestImage pushes an image whose contract is that of the image directory dir, as
// writeImageDir writes it with a repart definition added, to a registry of the test's, and
// returns its reference by tag.
func pushTestImage(t *testing.T, dir string) string {
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
	ref := ocitest.Registry(t) + "/lab/test-metal:v1"
	ocitest.Push(t, ocitest.Image(t, ociimage.Header{ImageID: "chalkos", RootHash: md.RootHash, Architecture: "x86-64", Platform: md.Platform}, c), ref)
	return ref
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
	useOCIImage(t, ta, pushTestImage(t, filepath.Join(ta.dir, "images", "test-metal")))
	if err := ta.run(context.Background(), []string{"validate", "--inventory", ta.inventory}); err != nil {
		t.Fatal(err)
	}
	if want := "n1: valid for the image test-metal (test on metal, root hash abababababab), config "; !strings.Contains(ta.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", ta.stdout, want)
	}
}

// TestOCIImageIsNotSent refuses to install or upgrade a node from its image in a registry, which
// chalkctl cannot send, before it reaches the node or the registry.
func TestOCIImageIsNotSent(t *testing.T) {
	ta := newTestApp(t)
	useOCIImage(t, ta, "registry.example/lab/test-metal:v1")
	c, err := loadInventory(ta.inventory)
	if err != nil {
		t.Fatal(err)
	}
	want := "the image test-metal of n1 comes from the registry, registry.example/lab/test-metal:v1, but chalkctl %s sends only an image from a directory"
	if _, err := c.imageDir("n1"); err == nil || !strings.Contains(err.Error(), fmt.Sprintf(want, "install")) {
		t.Errorf("imageDir = %v", err)
	}
	r := &upgradeRun{cluster: c}
	if _, err := r.groupOf("n1", nil); err == nil || !strings.Contains(err.Error(), fmt.Sprintf(want, "upgrade")) {
		t.Errorf("groupOf = %v", err)
	}
}
