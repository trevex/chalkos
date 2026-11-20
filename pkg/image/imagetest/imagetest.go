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

	"github.com/trevex/chalkos/pkg/fatfs"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/secureboot"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
	"github.com/trevex/chalkos/pkg/verity"
)

// Image describes a test image. Its store repeats Seed, Name and Platform over 300 blocks, so
// images of different seeds, names or platforms differ in their root hashes.
type Image struct {
	Name, Platform, Seed string
	// BlockSize is the store's dm-verity block size; zero is 512.
	BlockSize int
	// Edit changes the UKI's os-release, which names the image chalkos with an empty
	// IMAGE_VERSION, its name, its platform and three boot tries.
	Edit func(osRelease map[string]string)
}

// NeedTools skips the test unless every tool is in PATH.
func NeedTools(t testing.TB, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
}

// Write writes the image's chalkos.raw and repart-output.json into dir, made when missing, and
// returns the root hash of its store. The ESP starts the raw image, at offset 0.
func Write(t testing.TB, dir string, img Image) []byte {
	t.Helper()
	blockSize := img.BlockSize
	if blockSize == 0 {
		blockSize = 512
	}
	store := bytes.Repeat([]byte(img.Seed+img.Name+img.Platform), 300*blockSize)[:300*blockSize]
	tree, root, err := verity.Tree(bytes.NewReader(store), verity.Superblock{DataBlockSize: uint32(blockSize), HashBlockSize: uint32(blockSize), DataBlocks: 300, Salt: []byte(img.Seed)})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	ukiPath := filepath.Join(work, "uki.efi")
	loader := filepath.Join(work, "systemd-boot.efi")
	write(t, loader, ukitest.Build(map[string][]byte{".text": []byte("systemd-boot")}))
	osRelease := map[string]string{
		"IMAGE_ID": "chalkos", "IMAGE_VERSION": "", "CHALKOS_IMAGE": img.Name, "CHALKOS_PLATFORM": img.Platform, "CHALKOS_BOOT_TRIES": "3",
	}
	if img.Edit != nil {
		img.Edit(osRelease)
	}
	write(t, ukiPath, ukitest.UKI(osRelease, fmt.Sprintf("init=/x usrhash=%x", root)))
	const espSize, treeSize, storeSize = 4 << 20, 64 << 10, 2 << 20
	raw := make([]byte, espSize+treeSize+storeSize)
	copy(raw[espSize:], tree)
	copy(raw[espSize+treeSize:], store)
	write(t, filepath.Join(dir, "chalkos.raw"), raw)
	if err := image.WriteESP(filepath.Join(dir, "chalkos.raw"), 0, espSize, 0x1234abcd, []image.ESPFile{
		{From: ukiPath, To: "/EFI/Linux/chalkos_1.efi"},
		{From: loader, To: "/EFI/BOOT/BOOTX64.EFI"},
	}); err != nil {
		t.Fatal(err)
	}
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

// ReadESP returns the file name, such as /EFI/Linux/chalkos_1.efi, of the ESP at offset in the
// raw disk image.
func ReadESP(t testing.TB, raw string, offset int64, name string) []byte {
	t.Helper()
	esp, err := fatfs.Open(raw, offset, false)
	if err != nil {
		t.Fatal(err)
	}
	defer esp.Close()
	data, err := esp.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// WriteESP writes data to the file name of the ESP at offset in the raw disk image, replacing it.
func WriteESP(t testing.TB, raw string, offset int64, name string, data []byte) {
	t.Helper()
	esp, err := fatfs.Open(raw, offset, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := esp.WriteFile(name, data); err != nil {
		t.Fatal(err)
	}
	if err := esp.Close(); err != nil {
		t.Fatal(err)
	}
}

// Signer writes a Secure Boot db key, name.key, and its self-signed certificate, name.crt, into
// dir and returns their paths and the certificate's DER.
func Signer(t testing.TB, dir, name string) (key, cert string, der []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	key, cert = filepath.Join(dir, name+".key"), filepath.Join(dir, name+".crt")
	write(t, key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	write(t, cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return key, cert, der
}

// Sign signs an EFI binary for Secure Boot with the key and certificate files, as chalkbuild sign
// does, and returns the signed binary.
func Sign(t testing.TB, binary []byte, key, cert string) []byte {
	t.Helper()
	signer, err := secureboot.LoadSigner(key, cert)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secureboot.Sign(binary, signer)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// WriteDir writes a whole image directory into dir, the image's Write and its WriteContract, and
// returns the root hash of its store.
func WriteDir(t testing.TB, dir string, img Image) []byte {
	t.Helper()
	root := Write(t, dir, img)
	WriteContract(t, dir, img, root)
	return root
}

// WriteContract writes the contract files of an image directory into dir: metadata.json of the
// image's name and platform and the root hash, built from the revision abc123 and the tag v1,
// which its annotations name too, one schema, one repart definition and the facts of a layer
// named test.
func WriteContract(t testing.TB, dir string, img Image, root []byte) {
	t.Helper()
	md := fmt.Sprintf(`{"schemaVersion": 0, "id": "chalkos", "revision": "abc123", "tag": "v1", "name": %q, "platform": %q, "rootHash": %q, "kinds": [],
		"annotations": {"org.opencontainers.image.revision": "abc123", "org.opencontainers.image.version": "v1"}}`, img.Name, img.Platform, hex.EncodeToString(root))
	write(t, filepath.Join(dir, "metadata.json"), []byte(md))
	write(t, filepath.Join(dir, "schemas", "test.example.org_v1alpha1_Test.json"), []byte(`{"x-chalkos-kind": {"group": "test.example.org", "version": "v1alpha1", "kind": "Test", "multiple": false}}`))
	write(t, filepath.Join(dir, "repart.d", "00-esp.conf"), []byte("[Partition]\nType=esp\n"))
	write(t, filepath.Join(dir, "facts", "test.json"), []byte(`{"answer": 42}`))
}

// WriteDefinitions writes the repart definitions of a whole system region into dir's repart.d, in
// place of WriteContract's one, as an install and a pull read them: an ESP of 4 MiB, two store
// slots of x86-64 whose hash tree partitions take 64 KiB and whose store partitions are sized to
// their content, and STATE.
func WriteDefinitions(t testing.TB, dir string) {
	t.Helper()
	defs := filepath.Join(dir, "repart.d")
	if err := os.RemoveAll(defs); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"00-esp.conf":            "[Partition]\nType=esp\nFormat=vfat\nSizeMinBytes=4M\nSizeMaxBytes=4M\n",
		"10-store-verity-a.conf": "[Partition]\nType=usr-x86-64-verity\nSizeMinBytes=64K\nSizeMaxBytes=64K\n",
		"20-store-a.conf":        "[Partition]\nType=usr-x86-64\n",
		"30-store-verity-b.conf": "[Partition]\nType=usr-x86-64-verity\nLabel=_empty\nSizeMinBytes=64K\nSizeMaxBytes=64K\n",
		"40-store-b.conf":        "[Partition]\nType=usr-x86-64\nLabel=_empty\n",
		"50-state.conf":          "[Partition]\nType=" + storage.PartitionType("state") + "\nLabel=state\nFormat=ext4\nEncrypt=tpm2\n",
	} {
		write(t, filepath.Join(defs, name), []byte(text))
	}
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
