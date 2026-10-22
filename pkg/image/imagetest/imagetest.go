// Package imagetest builds small chalkos disk images for tests: an ESP holding systemd-boot and a
// UKI, the store's hash tree and the store, as repart-output.json describes them.
package imagetest

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
	"github.com/trevex/chalkos/pkg/verity"
)

// Image describes a test image. Its store repeats Seed, Variant and Platform over 300 blocks, so
// images of different seeds, variants or platforms differ in their root hashes.
type Image struct {
	Variant, Platform, Seed string
	// BlockSize is the store's dm-verity block size; zero is 512.
	BlockSize int
	// Edit changes the UKI's os-release, which names the image chalkos with an empty
	// IMAGE_VERSION, its variant, its platform and three boot tries.
	Edit func(osRelease map[string]string)
}

// Write writes the image's chalkos.raw and repart-output.json into dir, made when missing, and
// returns the root hash of its store. The ESP starts the raw image, at offset 0. It skips the
// test without mkfs.vfat and mtools.
func Write(t testing.TB, dir string, img Image) []byte {
	t.Helper()
	for _, tool := range []string{"mkfs.vfat", "mmd", "mcopy", "mdir"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	blockSize := img.BlockSize
	if blockSize == 0 {
		blockSize = 512
	}
	store := bytes.Repeat([]byte(img.Seed+img.Variant+img.Platform), 300*blockSize)[:300*blockSize]
	tree, root, err := verity.Tree(bytes.NewReader(store), verity.Superblock{DataBlockSize: uint32(blockSize), HashBlockSize: uint32(blockSize), DataBlocks: 300, Salt: []byte(img.Seed)})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	esp := filepath.Join(work, "esp.img")
	ukiPath := filepath.Join(work, "uki.efi")
	loader := filepath.Join(work, "systemd-boot.efi")
	write(t, loader, ukitest.Build(map[string][]byte{".text": []byte("systemd-boot")}))
	osRelease := map[string]string{
		"IMAGE_ID": "chalkos", "IMAGE_VERSION": "", "CHALKOS_VARIANT": img.Variant, "CHALKOS_PLATFORM": img.Platform, "CHALKOS_BOOT_TRIES": "3",
	}
	if img.Edit != nil {
		img.Edit(osRelease)
	}
	write(t, ukiPath, ukitest.UKI(osRelease, fmt.Sprintf("init=/x usrhash=%x", root)))
	for _, args := range [][]string{
		{"mkfs.vfat", "-C", esp, "4096"},
		{"mmd", "-i", esp, "::/EFI", "::/EFI/BOOT", "::/EFI/Linux"},
		{"mcopy", "-i", esp, ukiPath, "::/EFI/Linux/chalkos_1.efi"},
		{"mcopy", "-i", esp, loader, "::/EFI/BOOT/BOOTX64.EFI"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	espData, err := os.ReadFile(esp)
	if err != nil {
		t.Fatal(err)
	}
	const espSize, treeSize, storeSize = 4 << 20, 64 << 10, 2 << 20
	raw := make([]byte, espSize+treeSize+storeSize)
	copy(raw, espData)
	copy(raw[espSize:], tree)
	copy(raw[espSize+treeSize:], store)
	write(t, filepath.Join(dir, "chalkos.raw"), raw)
	id := hex.EncodeToString(root)[:16]
	parts, err := json.Marshal([]image.Partition{
		{Type: "esp", Label: "esp", Offset: 0, RawSize: espSize},
		{Type: "usr-x86-64-verity", Label: "store-verity_" + id, Offset: espSize, RawSize: treeSize, RootHash: hex.EncodeToString(root)},
		{Type: "usr-x86-64", Label: "store_" + id, Offset: espSize + treeSize, RawSize: storeSize, RootHash: hex.EncodeToString(root)},
	})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "repart-output.json"), parts)
	return root
}

// Signer writes a Secure Boot db key, name.key, and its self-signed certificate, name.crt, into
// dir and returns their paths.
func Signer(t testing.TB, dir, name string) (key, cert string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	key, cert = filepath.Join(dir, name+".key"), filepath.Join(dir, name+".crt")
	write(t, key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	write(t, cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return key, cert
}

// write writes a file, making its directory.
func write(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
