package secureboot

import (
	"bytes"
	"crypto/x509"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// certTable returns the revision, type and length of each entry of the signed image's
// certificate table, which follows the image at certOffset.
func certTable(t *testing.T, signed []byte) (offset int64, entries [][3]uint32) {
	t.Helper()
	l, err := readLayout(bytes.NewReader(signed), int64(len(signed)))
	if err != nil {
		t.Fatal(err)
	}
	for at := l.certOffset; at < l.certOffset+l.certSize; {
		e := signed[at:]
		length := binary.LittleEndian.Uint32(e)
		entries = append(entries, [3]uint32{uint32(binary.LittleEndian.Uint16(e[4:])), uint32(binary.LittleEndian.Uint16(e[6:])), length})
		at += int64((length + 7) &^ 7)
	}
	return l.certOffset, entries
}

// TestSign signs a UKI, systemd-boot and an image of a size that is not a multiple of eight with
// a db key and checks the signatures as firmware with the db certificate would, as sbverify does
// when it is on PATH, and against the certificate table sbsign writes when it is.
func TestSign(t *testing.T) {
	dir := t.TempDir()
	db := newSigner(t, dir, "db", nil, false)
	other := newSigner(t, dir, "other", nil, false)
	signer, err := LoadSigner(db.keyPEM, db.certPEM)
	if err != nil {
		t.Fatal(err)
	}
	images := map[string][]byte{"uki": testUKI(), "unaligned": append(testUKI(), 1, 2, 3)}
	if efi := os.Getenv("CHALKOS_TEST_EFI"); efi != "" {
		boot, err := os.ReadFile(efi)
		if err != nil {
			t.Fatal(err)
		}
		images["systemd-boot"] = boot
	}
	_, noSBVerify := exec.LookPath("sbverify")
	_, noSBSign := exec.LookPath("sbsign")
	for name, image := range images {
		t.Run(name, func(t *testing.T) {
			signed, err := Sign(image, signer)
			if err != nil {
				t.Fatal(err)
			}
			if err := verify(signed, Database{Certificates: []*x509.Certificate{db.cert}}, Database{}); err != nil {
				t.Errorf("the signature was refused: %v", err)
			}
			if err := verify(signed, Database{Certificates: []*x509.Certificate{other.cert}}, Database{}); err == nil || !strings.Contains(err.Error(), "not in db") {
				t.Errorf("a signature of a certificate db does not hold: %v", err)
			}
			padded := (len(image) + 7) &^ 7
			offset, entries := certTable(t, signed)
			if offset != int64(padded) || len(entries) != 1 || entries[0][0] != 0x0200 || entries[0][1] != 0x0002 {
				t.Errorf("the certificate table at %d holds %v, want one PKCS #7 entry of revision 2.0 at %d", offset, entries, padded)
			}
			path := filepath.Join(dir, name+".efi")
			if err := os.WriteFile(path, signed, 0o644); err != nil {
				t.Fatal(err)
			}
			if noSBVerify == nil {
				if out, err := exec.Command("sbverify", "--cert", db.certPEM, path).CombinedOutput(); err != nil {
					t.Errorf("sbverify: %v: %s", err, out)
				}
			}
			if noSBSign == nil {
				sbOffset, sbEntries := certTable(t, sbsign(t, dir, image, db))
				if sbOffset != offset || len(sbEntries) != 1 || sbEntries[0][0] != entries[0][0] || sbEntries[0][1] != entries[0][1] {
					t.Errorf("sbsign writes the certificate table %v at %d, Sign %v at %d", sbEntries, sbOffset, entries, offset)
				}
			}
		})
	}
}
