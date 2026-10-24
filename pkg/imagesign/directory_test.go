package imagesign

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/image/imagetest"
)

func needSigningTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"sbsign", "sbverify"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// verified is sbverify's verdict on a file of the disk image's ESP, which imagetest puts at offset 0.
func verified(t *testing.T, disk, file, cert string) error {
	t.Helper()
	copied := filepath.Join(t.TempDir(), filepath.Base(file))
	mustRun(t, "mcopy", "-n", "-i", disk+"@@0", file, copied)
	return exec.Command("sbverify", "--cert", cert, copied).Run()
}

// TestSignDirectory signs a copy of an image directory whose entries link into another directory,
// as a build's links into the Nix store, with the installer's ISO next to its raw image. The copy
// holds no links, and the image directory stays as it is.
func TestSignDirectory(t *testing.T) {
	needSigningTools(t)
	dir := t.TempDir()
	key, cert := imagetest.Signer(t, dir, "db")
	in := filepath.Join(dir, "in")
	imagetest.Write(t, in, imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"})
	store := filepath.Join(dir, "store")
	write(t, filepath.Join(store, "schemas", "a.json"), "{}")
	write(t, filepath.Join(store, "image.json"), "{}")
	for _, name := range []string{"schemas", "image.json"} {
		if err := os.Symlink(filepath.Join(store, name), filepath.Join(in, name)); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(in, "metadata.json"), "{}")
	raw, err := os.ReadFile(filepath.Join(in, "chalkos.raw"))
	if err != nil {
		t.Fatal(err)
	}
	// The test's ISO is a copy of the raw image, described as the raw image is.
	partitions, err := os.ReadFile(filepath.Join(in, "repart-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(in, "installer.iso"), string(raw))
	write(t, filepath.Join(in, "installer.iso.json"), string(partitions))
	// A store's files are read-only, which proves copySparse's chmod is needed: without it,
	// signing a disk image copied this way would fail to open it for writing.
	for _, name := range []string{"chalkos.raw", "installer.iso"} {
		if err := os.Chmod(filepath.Join(in, name), 0o444); err != nil {
			t.Fatal(err)
		}
	}

	out := filepath.Join(dir, "signed", "out")
	if err := os.Mkdir(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	signed, err := SignDirectory(context.Background(), in, out, key, cert)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(signed, []string{"chalkos.raw", "installer.iso"}) {
		t.Errorf("signed %v", signed)
	}
	for _, disk := range signed {
		for _, f := range []string{"::/EFI/Linux/chalkos_1.efi", "::/EFI/BOOT/BOOTX64.EFI"} {
			if err := verified(t, filepath.Join(out, disk), f, cert); err != nil {
				t.Errorf("%s of %s is not signed: %v", f, disk, err)
			}
		}
	}
	for _, name := range []string{"repart-output.json", "installer.iso.json", "metadata.json", "image.json", "schemas/a.json"} {
		if info, err := os.Lstat(filepath.Join(out, name)); err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s is no copied file: %v", name, err)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(in, "chalkos.raw")); !bytes.Equal(after, raw) {
		t.Error("signing changed the image directory's raw image")
	}
	if after, _ := os.ReadFile(filepath.Join(in, "installer.iso")); !bytes.Equal(after, raw) {
		t.Error("signing changed the image directory's ISO")
	}
}

// TestSignDirectoryRefuses leaves an output directory that exists as it is, and removes the copy
// again when signing fails, here with the certificate of another key.
func TestSignDirectoryRefuses(t *testing.T) {
	needSigningTools(t)
	dir := t.TempDir()
	key, _ := imagetest.Signer(t, dir, "db")
	_, other := imagetest.Signer(t, dir, "other")
	in := filepath.Join(dir, "in")
	imagetest.Write(t, in, imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"})
	existing := filepath.Join(dir, "existing")
	write(t, filepath.Join(existing, "kept"), "")
	if _, err := SignDirectory(context.Background(), in, existing, key, other); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("SignDirectory into a directory that exists = %v", err)
	}
	if entries, _ := os.ReadDir(existing); len(entries) != 1 {
		t.Errorf("the directory that exists holds %v", entries)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := SignDirectory(context.Background(), in, dangling, key, other); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("SignDirectory onto a dangling symlink = %v", err)
	}
	out := filepath.Join(dir, "out")
	if _, err := SignDirectory(context.Background(), in, out, key, other); err == nil {
		t.Error("SignDirectory with the certificate of another key succeeded")
	}
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a failed signing left %s: %v", out, err)
	}
	missing := filepath.Join(dir, "missing", "out")
	if _, err := SignDirectory(context.Background(), in, missing, key, other); err == nil {
		t.Error("SignDirectory with a missing parent directory succeeded")
	}
}

// TestSignDirectoryBracketInPath signs an image directory whose path holds characters that are
// wildcards to filepath.Glob, which diskImages must not use to find a directory's disk images.
func TestSignDirectoryBracketInPath(t *testing.T) {
	needSigningTools(t)
	dir := t.TempDir()
	key, cert := imagetest.Signer(t, dir, "db")
	in := filepath.Join(dir, "in[1]")
	imagetest.Write(t, in, imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"})
	out := filepath.Join(dir, "out[1]")
	if _, err := SignDirectory(context.Background(), in, out, key, cert); err != nil {
		t.Fatal(err)
	}
}
