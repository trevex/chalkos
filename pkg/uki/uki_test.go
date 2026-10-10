package uki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

const rootHash = "a334de1f08f79f925c9c65187238eb212b5f408c378f0f3a361ee8d395c94ddd"

func TestRead(t *testing.T) {
	img := ukitest.UKI(map[string]string{
		"IMAGE_ID":           "chalkos",
		"IMAGE_VERSION":      "0.2.0",
		"CHALKOS_CLUSTER":    "lab",
		"CHALKOS_ROLE":       "worker",
		"CHALKOS_BOOT_TRIES": "3",
		"PRETTY_NAME":        `NixOS "26.05"`,
	}, "init=/nix/store/x/init console=ttyS0 usrhash="+rootHash+"\n")
	got, err := Read(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != "chalkos" || got.Version() != "0.2.0" || got.Cluster() != "lab" || got.Role() != "worker" || got.BootTries() != "3" {
		t.Errorf("os-release %v", got.OSRelease)
	}
	if got.OSRelease["PRETTY_NAME"] != `NixOS "26.05"` {
		t.Errorf("PRETTY_NAME = %q", got.OSRelease["PRETTY_NAME"])
	}
	if h, err := got.UsrHash(); err != nil || hex.EncodeToString(h) != rootHash {
		t.Errorf("UsrHash = %x, %v", h, err)
	}

	if _, err := Read(bytes.NewReader(ukitest.Build(map[string][]byte{".osrel": []byte("ID=x\n")}))); err == nil || !strings.Contains(err.Error(), ".cmdline") {
		t.Errorf("a UKI without a command line: %v", err)
	}
	if _, err := Read(bytes.NewReader([]byte("not a PE"))); err == nil {
		t.Error("read something that is not a PE")
	}
}

func TestUsrHash(t *testing.T) {
	for _, cmdline := range []string{"", "usrhash=abc", "usrhash=" + rootHash + " usrhash=" + rootHash, "usrhash=" + rootHash[:62]} {
		if _, err := UsrHash(cmdline); err == nil {
			t.Errorf("UsrHash(%q) accepted", cmdline)
		}
	}
}

// signer is a certificate and its key, written as PEM files for sbsign.
type signer struct {
	cert            *x509.Certificate
	key             any
	certPEM, keyPEM string
}

func newSigner(t *testing.T, dir, name string, parent *signer, ec bool) *signer {
	t.Helper()
	var key any
	var pub any
	if ec {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	} else {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  parent == nil,
	}
	issuer, signKey := tmpl, key
	if parent != nil {
		issuer, signKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, pub, signKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	s := &signer{cert: cert, key: key, certPEM: filepath.Join(dir, name+".crt"), keyPEM: filepath.Join(dir, name+".key")}
	if err := os.WriteFile(s.certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.keyPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

// sbsign signs the image with sbsign and returns the signed image.
func sbsign(t *testing.T, dir string, image []byte, s *signer) []byte {
	t.Helper()
	in, out := filepath.Join(dir, "in.efi"), filepath.Join(dir, "out.efi")
	os.Remove(out)
	if err := os.WriteFile(in, image, 0o644); err != nil {
		t.Fatal(err)
	}
	if o, err := exec.Command("sbsign", "--key", s.keyPEM, "--cert", s.certPEM, "--output", out, in).CombinedOutput(); err != nil {
		t.Fatalf("sbsign: %v: %s", err, o)
	}
	signed, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func verify(img []byte, db, dbx Database) error {
	return VerifySignature(bytes.NewReader(img), int64(len(img)), db, dbx)
}

// TestVerifySignature signs a UKI and systemd-boot with sbsign, as chalkctl does, and checks
// them against db and dbx as firmware would.
func TestVerifySignature(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	efi := os.Getenv("CHALKOS_TEST_EFI")
	if efi == "" {
		t.Skip("CHALKOS_TEST_EFI not set")
	}
	boot, err := os.ReadFile(efi)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	db := newSigner(t, dir, "db", nil, false)
	other := newSigner(t, dir, "other", nil, false)
	// A signing certificate the db certificate issued, as a db CA would.
	leaf := newSigner(t, dir, "leaf", db, false)
	uki := ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos"}, "usrhash="+rootHash)
	// An image whose size is not a multiple of eight, which sbsign pads.
	odd := append(bytes.Clone(uki), 1, 2, 3)
	trusted := Database{Certificates: []*x509.Certificate{db.cert}}

	for _, tc := range []struct {
		name  string
		image []byte
		by    *signer
	}{
		{"uki", uki, db},
		{"systemd-boot", boot, db},
		{"trailing data", odd, db},
		{"signing certificate of db", uki, leaf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signed := sbsign(t, dir, tc.image, tc.by)
			if err := verify(signed, trusted, Database{}); err != nil {
				t.Fatalf("a signature of %s was refused: %v", tc.by.cert.Subject.CommonName, err)
			}
			if err := exec.Command("sbverify", "--cert", db.certPEM, filepath.Join(dir, "out.efi")).Run(); err != nil && tc.by == db {
				t.Errorf("sbverify refuses what VerifySignature accepted: %v", err)
			}
			if err := verify(signed, Database{Certificates: []*x509.Certificate{other.cert}}, Database{}); err == nil || !strings.Contains(err.Error(), "not in db") {
				t.Errorf("a signature of a certificate db does not hold: %v", err)
			}
			// A byte changed in a section, in the headers and after the sections.
			for _, at := range []int{len(tc.image) / 2, 0x50} {
				changed := bytes.Clone(signed)
				changed[at] ^= 1
				if err := verify(changed, trusted, Database{}); err == nil {
					t.Errorf("an image changed at %d after signing was accepted", at)
				}
			}
			digest := sha256.New()
			l, err := readLayout(bytes.NewReader(signed), int64(len(signed)))
			if err != nil {
				t.Fatal(err)
			}
			sum, err := l.digest(bytes.NewReader(signed), digest)
			if err != nil {
				t.Fatal(err)
			}
			if err := verify(signed, trusted, Database{SHA256: [][]byte{sum}}); err == nil || !strings.Contains(err.Error(), "dbx revokes the image") {
				t.Errorf("an image whose digest dbx lists: %v", err)
			}
			if err := verify(signed, trusted, Database{Certificates: []*x509.Certificate{tc.by.cert}}); err == nil || !strings.Contains(err.Error(), "dbx revokes the certificate") {
				t.Errorf("an image whose signer dbx lists: %v", err)
			}
		})
	}

	if err := verify(uki, trusted, Database{}); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Errorf("an unsigned image: %v", err)
	}
	// sbsign signs with RSA keys alone; an ECDSA certificate in db trusts nothing it signed.
	ec := newSigner(t, dir, "ec", nil, true)
	if err := verify(sbsign(t, dir, uki, db), Database{Certificates: []*x509.Certificate{ec.cert}}, Database{}); err == nil {
		t.Error("accepted a signature that the ECDSA certificate in db did not make")
	}
}

// signatureList encodes an EFI signature list of entries of the type.
func signatureList(typ [16]byte, entries ...[]byte) []byte {
	size := 16 + len(entries[0])
	b := make([]byte, 28)
	copy(b, typ[:])
	binary.LittleEndian.PutUint32(b[16:], uint32(28+size*len(entries)))
	binary.LittleEndian.PutUint32(b[24:], uint32(size))
	for _, e := range entries {
		b = append(b, make([]byte, 16)...)
		b = append(b, e...)
	}
	return b
}

func TestParseDatabase(t *testing.T) {
	dir := t.TempDir()
	a, b := newSigner(t, dir, "a", nil, false), newSigner(t, dir, "b", nil, true)
	digest := sha256.Sum256([]byte("image"))
	unknown := guid(0x3c5766e8, 0x269c, 0x4e34, [8]byte{0xaa, 0x14, 0xed, 0x77, 0x6e, 0x85, 0xb3, 0xb6})
	value := bytes.Join([][]byte{
		signatureList(certX509, a.cert.Raw),
		signatureList(unknown, []byte("rsa2048 key")),
		signatureList(certSHA256, digest[:], digest[:]),
		signatureList(certX509, b.cert.Raw),
		signatureList(certX509SHA256, append(digest[:], make([]byte, 16)...)),
	}, nil)
	db, err := ParseDatabase(value)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Certificates) != 2 || !db.Certificates[0].Equal(a.cert) || !db.Certificates[1].Equal(b.cert) || len(db.SHA256) != 2 || !bytes.Equal(db.SHA256[0], digest[:]) || len(db.TBSSHA256) != 1 || !bytes.Equal(db.TBSSHA256[0], digest[:]) {
		t.Errorf("parsed %d certificates and %d digests", len(db.Certificates), len(db.SHA256))
	}
	if want := []string{"3c5766e8-269c-4e34-aa14-ed776e85b3b6"}; !slices.Equal(db.Unreadable, want) {
		t.Errorf("unreadable types %v, want %v", db.Unreadable, want)
	}
	for name, broken := range map[string][]byte{
		"cut short":        value[:len(value)-1],
		"short header":     value[:20],
		"short digest":     signatureList(certSHA256, digest[:31]),
		"short TBS digest": signatureList(certX509SHA256, digest[:]),
		"bad certificate":  signatureList(certX509, []byte("not DER")),
	} {
		if _, err := ParseDatabase(broken); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestArchitecture(t *testing.T) {
	img := ukitest.Build(map[string][]byte{".text": []byte("x")})
	for _, tc := range []struct {
		machine uint16
		want    string
	}{{0x8664, "x86-64"}, {0xaa64, "arm64"}, {0x14c, ""}} {
		arch, err := Architecture(bytes.NewReader(ukitest.WithMachine(img, tc.machine)))
		if arch != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("machine %#x: %q, %v; want %q", tc.machine, arch, err, tc.want)
		}
	}
	if _, err := Architecture(strings.NewReader("MZ")); err == nil {
		t.Error("took a file that is no PE image")
	}
	for goarch, want := range map[string]string{"amd64": "x86-64", "arm64": "arm64", "riscv64": ""} {
		if got := GoArchitecture(goarch); got != want {
			t.Errorf("GoArchitecture(%s) = %q, want %q", goarch, got, want)
		}
	}
	for arch, want := range map[string]string{"x86-64": "BOOTX64.EFI", "arm64": "BOOTAA64.EFI", "amd64": ""} {
		if got := BootLoaderName(arch); got != want {
			t.Errorf("BootLoaderName(%s) = %q, want %q", arch, got, want)
		}
	}
}
