package secureboot

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"path/filepath"
	"testing"
)

// TestX509SignatureLists writes one list per certificate, each of one entry owned by the owner,
// which ParseDatabase reads back.
func TestX509SignatureLists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := NewKeys(dir, "t"); err != nil {
		t.Fatal(err)
	}
	k, err := LoadKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := X509SignatureLists(k.Owner, k.DB.Cert, k.KEK.Cert)
	rest := b
	for i, raw := range [][]byte{k.DB.Cert.Raw, k.KEK.Cert.Raw} {
		if len(rest) < sigListHeaderSize {
			t.Fatalf("list %d is cut short", i)
		}
		listSize := binary.LittleEndian.Uint32(rest[16:])
		if !bytes.Equal(rest[:16], certX509[:]) || listSize != uint32(sigListHeaderSize+16+len(raw)) ||
			binary.LittleEndian.Uint32(rest[20:]) != 0 || binary.LittleEndian.Uint32(rest[24:]) != uint32(16+len(raw)) {
			t.Errorf("list %d has the header %x", i, rest[:sigListHeaderSize])
		}
		if !bytes.Equal(rest[28:44], k.Owner[:]) || !bytes.Equal(rest[44:listSize], raw) {
			t.Errorf("list %d does not hold the owner and the certificate", i)
		}
		rest = rest[listSize:]
	}
	if len(rest) != 0 {
		t.Errorf("%d bytes after the lists", len(rest))
	}
	db, err := ParseDatabase(b)
	if err != nil || len(db.Certificates) != 2 || !db.Certificates[0].Equal(k.DB.Cert) || !db.Certificates[1].Equal(k.KEK.Cert) {
		t.Errorf("ParseDatabase = %+v, %v", db, err)
	}
}

// TestSHA256SignatureList writes the digests as one list of entries of the owner and 32 bytes,
// which ParseDatabase reads back, and nothing for no digests.
func TestSHA256SignatureList(t *testing.T) {
	owner := [16]byte{1, 2, 3}
	a, b := sha256.Sum256([]byte("a")), sha256.Sum256([]byte("b"))
	list := SHA256SignatureList(owner, a[:], b[:])
	want := append(bytes.Clone(certSHA256[:]), 28+2*48, 0, 0, 0, 0, 0, 0, 0, 48, 0, 0, 0)
	want = append(append(append(want, owner[:]...), a[:]...), owner[:]...)
	if want = append(want, b[:]...); !bytes.Equal(list, want) {
		t.Errorf("SHA256SignatureList = %x, want %x", list, want)
	}
	db, err := ParseDatabase(list)
	if err != nil || len(db.SHA256) != 2 || !bytes.Equal(db.SHA256[0], a[:]) || !bytes.Equal(db.SHA256[1], b[:]) {
		t.Errorf("ParseDatabase = %+v, %v", db, err)
	}
	if list := SHA256SignatureList(owner); len(list) != 0 {
		t.Errorf("SHA256SignatureList of no digests = %x", list)
	}
}
