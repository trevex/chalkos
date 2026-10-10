package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestOpenImageForInstall reads an image with its boot loader, as an install sends it, signs
// both binaries, and checks both signatures; an upgrade's image brings no boot loader.
func TestOpenImageForInstall(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	dir := t.TempDir()
	key, cert := testSigner(t, dir, "db")
	_, otherCert := testSigner(t, dir, "other")
	certPEM, _ := os.ReadFile(cert)
	otherPEM, _ := os.ReadFile(otherCert)
	path := testUpgradeImage(t, "lab", "w", "0.2.0")

	img, err := openImage(context.Background(), path, key, cert, true)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()
	loader, err := os.ReadFile(img.bootLoader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(loader)
	if b := img.header.BootLoader; b == nil || b.Size != uint64(len(loader)) || !bytes.Equal(b.Sha256, sum[:]) {
		t.Errorf("the header names the boot loader %v, want %d bytes", b, len(loader))
	}
	if err := img.checkSignature(string(certPEM)); err != nil {
		t.Errorf("the signed image: %v", err)
	}
	if err := img.checkSignature(string(otherPEM)); err == nil || !strings.Contains(err.Error(), "UKI") {
		t.Errorf("another db certificate: %v", err)
	}
	parts, done, err := img.parts()
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

	// A boot loader that is not signed is refused like an unsigned UKI.
	unsigned, err := openImage(context.Background(), path, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unsigned.Close()
	ukiData, _ := os.ReadFile(img.uki)
	if err := os.WriteFile(unsigned.uki, ukiData, 0o644); err != nil {
		t.Fatal(err)
	}
	unsigned.header.Uki = img.header.Uki
	if err := unsigned.checkSignature(string(certPEM)); err == nil || !strings.Contains(err.Error(), "refuse the image's boot loader") {
		t.Errorf("an unsigned boot loader: %v", err)
	}

	upgrade, err := openImage(context.Background(), path, key, cert, false)
	if err != nil {
		t.Fatal(err)
	}
	defer upgrade.Close()
	if upgrade.header.BootLoader != nil || upgrade.bootLoader != "" {
		t.Error("an upgrade's image brings a boot loader")
	}
}
