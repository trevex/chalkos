package image_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

// open opens an image's parts and, when that succeeds, registers their removal on the test's
// cleanup so a later failure in the test cannot leak the temporary directory OpenParts made.
func open(t *testing.T, dir string, bootLoader bool) (*image.Parts, error) {
	t.Helper()
	parts, err := image.ReadPartitions(filepath.Join(dir, "repart-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := image.OpenParts(filepath.Join(dir, "chalkos.raw"), parts, bootLoader)
	if p != nil {
		t.Cleanup(func() { p.Close() })
	}
	return p, err
}

// TestOpenParts reads an image's store, its UKI and boot loader and the architecture both are
// built for, and removes the copies on Close; without the boot loader asked for, it copies none.
func TestOpenParts(t *testing.T) {
	dir := t.TempDir()
	root := imagetest.Write(t, dir, imagetest.Image{Name: "w-metal", Platform: "metal", Seed: "1"})
	p, err := open(t, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Store.RootHash, root) || p.Architecture != "x86-64" || p.Info.Name() != "w-metal" || p.Info.Platform() != "metal" {
		t.Errorf("root hash %x, architecture %q, name %q, platform %q", p.Store.RootHash, p.Architecture, p.Info.Name(), p.Info.Platform())
	}
	if _, err := os.Stat(p.BootLoader); err != nil {
		t.Errorf("the boot loader: %v", err)
	}
	copies := filepath.Dir(p.UKI)
	p.Close()
	if _, err := os.Stat(copies); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Close left %s: %v", copies, err)
	}
	upgrade, err := open(t, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if upgrade.BootLoader != "" {
		t.Error("the boot loader was copied without being asked for")
	}
}

// TestOpenPartsRefusesABootLoaderOfAnotherArchitecture refuses an image whose boot loader is built
// for another architecture than its UKI, when the boot loader is asked for.
func TestOpenPartsRefusesABootLoaderOfAnotherArchitecture(t *testing.T) {
	dir := t.TempDir()
	imagetest.Write(t, dir, imagetest.Image{Name: "w-metal", Platform: "metal", Seed: "1"})
	arm := ukitest.WithMachine(ukitest.Build(map[string][]byte{".text": []byte("systemd-boot")}), 0xaa64)
	imagetest.WriteESP(t, filepath.Join(dir, "chalkos.raw"), 0, "/EFI/BOOT/BOOTX64.EFI", arm)
	if _, err := open(t, dir, true); err == nil || !strings.Contains(err.Error(), "the image's boot loader is built for arm64, its UKI for x86-64") {
		t.Fatalf("OpenParts = %v, want the boot loader refused", err)
	}
	if _, err := open(t, dir, false); err != nil {
		t.Fatal(err)
	}
}

// TestOpenPartsRefusesAStoreOfAnotherArchitecture refuses an image whose store partitions are of
// another architecture than its UKI is built for.
func TestOpenPartsRefusesAStoreOfAnotherArchitecture(t *testing.T) {
	dir := t.TempDir()
	imagetest.Write(t, dir, imagetest.Image{Name: "w-metal", Platform: "metal", Seed: "1"})
	parts, err := image.ReadPartitions(filepath.Join(dir, "repart-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range parts {
		parts[i].Type = strings.Replace(parts[i].Type, "usr-x86-64", "usr-arm64", 1)
	}
	p, err := image.OpenParts(filepath.Join(dir, "chalkos.raw"), parts, false)
	if p != nil {
		p.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "the image's store is of arm64, its UKI is built for x86-64") {
		t.Fatalf("OpenParts = %v, want the store refused", err)
	}
}

// TestOpenPartsRefusesAUKIOfAnotherStore refuses an image whose UKI's usrhash names another store.
func TestOpenPartsRefusesAUKIOfAnotherStore(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	imagetest.Write(t, dir, imagetest.Image{Name: "w-metal", Platform: "metal", Seed: "1"})
	imagetest.Write(t, other, imagetest.Image{Name: "w-metal", Platform: "metal", Seed: "2"})
	uki := imagetest.ReadESP(t, filepath.Join(other, "chalkos.raw"), 0, "/EFI/Linux/chalkos_1.efi")
	imagetest.WriteESP(t, filepath.Join(dir, "chalkos.raw"), 0, "/EFI/Linux/chalkos_1.efi", uki)
	if _, err := open(t, dir, false); err == nil || !strings.Contains(err.Error(), "the image's UKI boots another store than the image holds") {
		t.Fatalf("OpenParts = %v, want the UKI refused", err)
	}
}
