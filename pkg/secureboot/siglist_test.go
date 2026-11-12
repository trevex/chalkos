package secureboot

import (
	"bytes"
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
