package imagesign

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func writeTestSigner(t *testing.T, dir string) (keyPath, certPath string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "imagesign test db"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(dir, "db.key")
	certPath = filepath.Join(dir, "db.crt")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	return keyPath, certPath
}

func TestSignImageSignsOnlyBootLoaderAndUKIs(t *testing.T) {
	for _, tool := range []string{"mkfs.vfat", "mmd", "mcopy", "mdir", "sbsign", "sbverify"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	efi := os.Getenv("CHALKOS_TEST_EFI")
	if efi == "" {
		t.Skip("CHALKOS_TEST_EFI not set")
	}
	dir := t.TempDir()
	key, cert := writeTestSigner(t, dir)

	// 1 MiB of padding stands in for the partition table, followed by a 32 MiB FAT filesystem.
	const offset = 1 << 20
	image := filepath.Join(dir, "disk.raw")
	if err := os.WriteFile(image, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(image, offset+32<<20); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "mkfs.vfat", "--offset", strconv.Itoa(offset/512), image, strconv.Itoa(32<<10))
	fat := fmt.Sprintf("%s@@%d", image, offset)
	mustRun(t, "mmd", "-i", fat, "::/EFI", "::/EFI/BOOT", "::/EFI/Linux", "::/EFI/tools")
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/BOOT/BOOTX64.EFI")
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/Linux/chalkos_0.1.0.efi")
	mustRun(t, "mcopy", "-i", fat, efi, "::/EFI/tools/shell.efi")

	if err := SignImage(context.Background(), image, Partition{Type: "esp", Offset: offset}, key, cert); err != nil {
		t.Fatal(err)
	}

	verify := func(f string) error {
		out := filepath.Join(dir, "check.efi")
		os.Remove(out)
		mustRun(t, "mcopy", "-n", "-i", fat, f, out)
		return exec.Command("sbverify", "--cert", cert, out).Run()
	}
	for _, f := range []string{"::/EFI/BOOT/BOOTX64.EFI", "::/EFI/Linux/chalkos_0.1.0.efi"} {
		if err := verify(f); err != nil {
			t.Errorf("%s is not signed: %v", f, err)
		}
	}
	if verify("::/EFI/tools/shell.efi") == nil {
		t.Error("::/EFI/tools/shell.efi was signed")
	}
}
