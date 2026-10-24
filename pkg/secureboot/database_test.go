package secureboot

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"testing"
)

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
