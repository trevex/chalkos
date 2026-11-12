package secureboot

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	sharedKeys     Keys
	sharedKeysDir  string
	sharedKeysErr  error
	sharedKeysOnce sync.Once
)

// testKeys are keys NewKeys made, shared by the tests, as RSA keys take a while to generate, and
// the directory that holds them.
func testKeys(t *testing.T) (Keys, string) {
	t.Helper()
	sharedKeysOnce.Do(func() {
		if sharedKeysDir, sharedKeysErr = os.MkdirTemp("", "secureboot-keys-"); sharedKeysErr != nil {
			return
		}
		if sharedKeysErr = NewKeys(sharedKeysDir, "test"); sharedKeysErr == nil {
			sharedKeys, sharedKeysErr = LoadKeys(sharedKeysDir)
		}
	})
	if sharedKeysErr != nil {
		t.Fatal(sharedKeysErr)
	}
	return sharedKeys, sharedKeysDir
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedKeysDir != "" {
		os.RemoveAll(sharedKeysDir)
	}
	os.Exit(code)
}

// TestSignUpdate signs a db update with the KEK, which parses back with its time and data, whose
// signature verifies with the KEK's certificate over the variable's name, vendor and attributes
// alone, and whose signature lists hold the db certificate.
func TestSignUpdate(t *testing.T) {
	k, _ := testKeys(t)
	data := X509SignatureLists(k.Owner, k.DB.Cert)
	u := Update{Name: "db", Vendor: ImageSecurityDatabase, Attributes: AttrAppend, Data: data}
	when := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	payload, err := SignUpdate(u, when, k.KEK.Cert, k.KEK.Key)
	if err != nil {
		t.Fatal(err)
	}
	// EFI_TIME, then WIN_CERTIFICATE_UEFI_GUID: its length, revision 2.0, type EFI_GUID and the
	// GUID of PKCS #7.
	wantTime := []byte{0xea, 0x07, 11, 12, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(payload[:16], wantTime) {
		t.Errorf("EFI_TIME %x, want %x", payload[:16], wantTime)
	}
	length := binary.LittleEndian.Uint32(payload[16:])
	if binary.LittleEndian.Uint16(payload[20:]) != 0x0200 || binary.LittleEndian.Uint16(payload[22:]) != 0x0ef1 || !bytes.Equal(payload[24:40], certTypePKCS7[:]) {
		t.Errorf("WIN_CERTIFICATE_UEFI_GUID header %x", payload[16:40])
	}
	if !bytes.Equal(payload[16+length:], data) {
		t.Error("the signature lists do not follow the descriptor")
	}
	// The signed data is bare SignedData, whose digest algorithm firmware reads at offset 13.
	if signed := payload[40 : 16+length]; signed[0] != 0x30 || signed[1] != 0x82 || !bytes.Equal(signed[13:22], []byte{0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x01}) {
		t.Errorf("the signed data starts with %x", signed[:24])
	}

	p, err := ParseUpdate(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Time.Equal(when) || !bytes.Equal(p.Data, data) {
		t.Fatalf("parsed %v, %x", p.Time, p.Data)
	}
	if err := p.Verify(u.Name, u.Vendor, u.Attributes, k.KEK.Cert); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		what   string
		name   string
		vendor [16]byte
		attrs  uint32
	}{
		{"another name", "dbx", ImageSecurityDatabase, AttrAppend},
		{"another vendor", "db", GlobalVariable, AttrAppend},
		{"other attributes", "db", ImageSecurityDatabase, AttrReplace},
	} {
		if err := p.Verify(tc.name, tc.vendor, tc.attrs, k.KEK.Cert); err == nil {
			t.Errorf("verified for %s", tc.what)
		}
	}
	if err := p.Verify(u.Name, u.Vendor, u.Attributes, k.PK.Cert); err == nil {
		t.Error("verified with the wrong certificate")
	}
	certs, err := p.X509Certificates()
	if err != nil || len(certs) != 1 || !certs[0].Equal(k.DB.Cert) {
		t.Fatalf("certificates %v %v", certs, err)
	}

	// A tampered value no longer verifies.
	tampered := bytes.Clone(payload)
	tampered[len(tampered)-1] ^= 1
	if p, err := ParseUpdate(tampered); err != nil || p.Verify(u.Name, u.Vendor, u.Attributes, k.KEK.Cert) == nil {
		t.Errorf("a tampered update: %v", err)
	}
}

func TestParseUpdateRefuses(t *testing.T) {
	k, _ := testKeys(t)
	payload, err := SignUpdate(Update{Name: "PK", Vendor: GlobalVariable, Attributes: AttrReplace, Data: X509SignatureLists(k.Owner, k.PK.Cert)}, time.Now(), k.PK.Cert, k.PK.Key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		what   string
		change func(b []byte) []byte
		want   string
	}{
		{"a short update", func(b []byte) []byte { return b[:30] }, "cut short"},
		{"a time with nanoseconds", func(b []byte) []byte { b[8] = 1; return b }, "EFI_TIME"},
		{"another revision", func(b []byte) []byte { b[21] = 1; return b }, "WIN_CERTIFICATE"},
		{"another certificate type", func(b []byte) []byte { b[24] ^= 1; return b }, "PKCS #7"},
		{"a length past the end", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[16:], uint32(len(b))); return b }, "length"},
	} {
		if _, err := ParseUpdate(tc.change(bytes.Clone(payload))); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error about %s", tc.what, err, tc.want)
		}
	}
	// Data that is not a signature list of certificates parses, but holds no certificates.
	p, err := ParseUpdate(append(bytes.Clone(payload[:len(payload)-len(k.PK.Cert.Raw)-44]), "garbage"...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.X509Certificates(); err == nil {
		t.Error("X509Certificates read garbage")
	}
	digest := make([]byte, 32)
	p.Data = signatureList(certSHA256, digest)
	if _, err := p.X509Certificates(); err == nil || !strings.Contains(err.Error(), "X.509") {
		t.Errorf("X509Certificates of a digest: %v", err)
	}
}

// TestSignUpdateAsSbvarsign signs a db update as sbvarsign does, bytes for bytes: sbvarsign
// writes the time of its run, which the update is signed with here too. sbvarsign writes the
// month from 0, so its time is copied as it is.
func TestSignUpdateAsSbvarsign(t *testing.T) {
	if _, err := exec.LookPath("sbvarsign"); err != nil {
		t.Skip("sbvarsign not in PATH")
	}
	k, dir := testKeys(t)
	data := X509SignatureLists(k.Owner, k.DB.Cert)
	work := t.TempDir()
	esl := filepath.Join(work, "db.esl")
	if err := os.WriteFile(esl, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(work, "db.auth")
	cmd := exec.Command("sbvarsign", "--key", filepath.Join(dir, "KEK.key"), "--cert", filepath.Join(dir, "KEK.crt"),
		"--attr", "NON_VOLATILE,BOOTSERVICE_ACCESS,RUNTIME_ACCESS,TIME_BASED_AUTHENTICATED_WRITE_ACCESS,APPEND_WRITE", "--output", out, "db", esl)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sbvarsign: %v\n%s", err, b)
	}
	theirs, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseUpdate(theirs)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Verify("db", ImageSecurityDatabase, AttrAppend, k.KEK.Cert); err != nil {
		t.Errorf("sbvarsign's update does not verify: %v", err)
	}
	var efiTime [16]byte
	copy(efiTime[:], theirs)
	ours, err := signUpdate(Update{Name: "db", Vendor: ImageSecurityDatabase, Attributes: AttrAppend, Data: data}, efiTime, k.KEK.Cert, k.KEK.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ours, theirs) {
		t.Errorf("the update differs from sbvarsign's:\nours   %x\ntheirs %x", ours, theirs)
	}
}
