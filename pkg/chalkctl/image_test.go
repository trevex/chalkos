package chalkctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenImageForInstall reads an image with its boot loader, as an install sends it; an
// upgrade's image brings no boot loader.
func TestOpenImageForInstall(t *testing.T) {
	path := testUpgradeImage(t, "w", "0.2.0")

	img, err := openImage(context.Background(), path, true, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()
	loader, err := os.ReadFile(img.BootLoader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(loader)
	if b := img.header.BootLoader; b == nil || b.Size != uint64(len(loader)) || !bytes.Equal(b.Sha256, sum[:]) {
		t.Errorf("the header names the boot loader %v, want %d bytes", b, len(loader))
	}
	if img.header.Architecture != "x86-64" {
		t.Errorf("the header names the architecture %q, want x86-64", img.header.Architecture)
	}
	parts, done, err := img.stream()
	if err != nil {
		t.Fatal(err)
	}
	var sent bytes.Buffer
	if _, err := sent.ReadFrom(parts); err != nil {
		t.Fatal(err)
	}
	done()
	if uint64(sent.Len()) != img.size() || !bytes.HasSuffix(sent.Bytes(), loader) {
		t.Errorf("sent %d bytes, want %d ending in the boot loader", sent.Len(), img.size())
	}

	upgrade, err := openImage(context.Background(), path, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer upgrade.Close()
	if upgrade.header.BootLoader != nil || upgrade.BootLoader != "" {
		t.Error("an upgrade's image brings a boot loader")
	}
}

// TestOpenImageTrustsOnlyItsOwnMetadata names the revision and tag of the metadata.json next to
// the raw image only when it names the image's root hash: a raw image copied next to another
// image's metadata, of whatever schema version, is opened without provenance and with a warning
// naming the file. Without metadata.json it is opened silently.
func TestOpenImageTrustsOnlyItsOwnMetadata(t *testing.T) {
	path := testUpgradeImage(t, "w", "0.2.0")
	root := hex.EncodeToString(rootOf(t, path))
	for _, tc := range []struct {
		name, metadata string
		want           bool
	}{
		{"its root hash", `{"schemaVersion": 0, "rootHash": "` + root + `", "revision": "a1b2c3", "tag": "v1.5.0"}`, true},
		{"its root hash in upper case", `{"schemaVersion": 0, "rootHash": "` + strings.ToUpper(root) + `", "revision": "a1b2c3", "tag": "v1.5.0"}`, true},
		{"another root hash", `{"schemaVersion": 0, "rootHash": "` + strings.Repeat("ab", 32) + `", "revision": "a1b2c3", "tag": "v1.5.0"}`, false},
		{"another root hash and schema version", `{"schemaVersion": 9, "rootHash": "` + strings.Repeat("ab", 32) + `", "revision": "a1b2c3", "tag": "v1.5.0"}`, false},
		{"no root hash", `{"schemaVersion": 0, "revision": "a1b2c3", "tag": "v1.5.0"}`, false},
		{"no JSON", `revision=a1b2c3`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, filepath.Join(path, "metadata.json"), tc.metadata)
			var stderr bytes.Buffer
			img, err := openImage(context.Background(), path, false, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			defer img.Close()
			if got := img.header.Revision == "a1b2c3" && img.header.Tag == "v1.5.0"; got != tc.want || !tc.want && (img.header.Revision != "" || img.header.Tag != "") {
				t.Errorf("revision %q and tag %q; want them named: %v", img.header.Revision, img.header.Tag, tc.want)
			}
			if warned := strings.Contains(stderr.String(), "warning: ignoring "+filepath.Join(path, "metadata.json")) && strings.Count(stderr.String(), "\n") == 1; warned == tc.want || tc.want && stderr.Len() != 0 {
				t.Errorf("stderr = %q; want one warning line: %v", stderr.String(), !tc.want)
			}
		})
	}
	if err := os.Remove(filepath.Join(path, "metadata.json")); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	img, err := openImage(context.Background(), path, false, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()
	if img.header.Revision != "" || img.header.Tag != "" {
		t.Errorf("without metadata.json: revision %q and tag %q", img.header.Revision, img.header.Tag)
	}
	if stderr.Len() != 0 {
		t.Errorf("without metadata.json: stderr = %q", stderr.String())
	}
}

func TestImageContractNeedsSchemas(t *testing.T) {
	dir := t.TempDir()
	if _, err := imageContract(dir); err == nil || !strings.Contains(err.Error(), dir+" has no schemas/") {
		t.Errorf("err = %v", err)
	}
}
