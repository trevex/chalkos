package uki

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"encoding/pem"
	"hash"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

// authenticode signs the PE image as sbsign does, with any key: an Authenticode signature of
// its digest appended as its certificate table, carrying the signer's certificate and those of bag.
func authenticode(t *testing.T, image []byte, cert *x509.Certificate, key crypto.Signer, bag ...*x509.Certificate) []byte {
	t.Helper()
	image = append(bytes.Clone(image), make([]byte, (8-len(image)%8)%8)...)
	l, err := readLayout(bytes.NewReader(image), int64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := l.digest(bytes.NewReader(image), sha256.New())
	if err != nil {
		t.Fatal(err)
	}
	must := func(b []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	sha256Alg := pkix.AlgorithmIdentifier{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}
	indirect := must(asn1.Marshal(struct {
		Data          asn1.RawValue
		MessageDigest struct {
			Algorithm pkix.AlgorithmIdentifier
			Digest    []byte
		}
	}{
		Data: asn1.RawValue{FullBytes: must(asn1.Marshal(struct{ Type asn1.ObjectIdentifier }{asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 1, 15}}))},
		MessageDigest: struct {
			Algorithm pkix.AlgorithmIdentifier
			Digest    []byte
		}{sha256Alg, digest},
	}))
	var inner asn1.RawValue
	if _, err := asn1.Unmarshal(indirect, &inner); err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(inner.Bytes)
	set := func(elements ...[]byte) []byte {
		var raw []asn1.RawValue
		for _, e := range elements {
			raw = append(raw, asn1.RawValue{FullBytes: e})
		}
		return must(asn1.MarshalWithParams(raw, "set"))
	}
	attrs := set(
		must(asn1.Marshal(attribute{Type: oidContentType, Values: asn1.RawValue{FullBytes: set(must(asn1.Marshal(oidSpcIndirectData)))}})),
		must(asn1.Marshal(attribute{Type: oidMessageDigest, Values: asn1.RawValue{FullBytes: set(must(asn1.Marshal(contentDigest[:])))}})),
	)
	attrsDigest := sha256.Sum256(attrs)
	signature := must(key.Sign(rand.Reader, attrsDigest[:], crypto.SHA256))
	encryption := oidSHA256WithRSA
	if cert.PublicKeyAlgorithm == x509.ECDSA {
		encryption = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	}
	certs := bytes.Clone(cert.Raw)
	for _, c := range bag {
		certs = append(certs, c.Raw...)
	}
	implicit := bytes.Clone(attrs)
	implicit[0] = 0xa0
	sd := must(asn1.Marshal(signedData{
		Version:          1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{sha256Alg},
		ContentInfo:      contentInfo{ContentType: oidSpcIndirectData, Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: indirect}},
		Certificates:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: certs},
		SignerInfos: []signerInfo{{
			Version:                   1,
			IssuerAndSerial:           issuerAndSerial{Issuer: asn1.RawValue{FullBytes: cert.RawIssuer}, Serial: cert.SerialNumber},
			DigestAlgorithm:           sha256Alg,
			AuthenticatedAttributes:   asn1.RawValue{FullBytes: implicit},
			DigestEncryptionAlgorithm: pkix.AlgorithmIdentifier{Algorithm: encryption},
			EncryptedDigest:           signature,
		}},
	}))
	der := must(asn1.Marshal(contentInfo{ContentType: oidSignedData, Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: sd}}))
	entry := binary.LittleEndian.AppendUint32(nil, uint32(8+len(der)))
	entry = binary.LittleEndian.AppendUint16(entry, 0x0200)
	entry = binary.LittleEndian.AppendUint16(entry, 0x0002)
	entry = append(entry, der...)
	entry = append(entry, make([]byte, (8-len(entry)%8)%8)...)
	binary.LittleEndian.PutUint32(image[l.securityDir:], uint32(len(image)))
	binary.LittleEndian.PutUint32(image[l.securityDir+4:], uint32(len(entry)))
	return append(image, entry...)
}

// FuzzVerifySignature checks that no image makes VerifySignature panic.
func FuzzVerifySignature(f *testing.F) {
	f.Add(ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos"}, "usrhash="+rootHash))
	f.Add(shortOptionalHeader())
	f.Fuzz(func(t *testing.T, image []byte) {
		VerifySignature(bytes.NewReader(image), int64(len(image)), Database{}, Database{})
	})
}

// shortOptionalHeader is a PE32+ image whose optional header ends before the count of its data
// directories.
func shortOptionalHeader() []byte {
	img := ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos"}, "usrhash="+rootHash)
	binary.LittleEndian.PutUint16(img[0x44+16:], 100)
	return img
}

func TestShortOptionalHeader(t *testing.T) {
	img := shortOptionalHeader()
	if err := VerifySignature(bytes.NewReader(img), int64(len(img)), Database{}, Database{}); err == nil || !strings.Contains(err.Error(), "optional header") {
		t.Errorf("VerifySignature = %v", err)
	}
}

// TestVerifySignatureRefuses checks what firmware would refuse, or what a signature does not
// cover: a certificate table that is not the image's end, section headers outside the hashed
// headers, revoked certificates anywhere in the signer's chain and SHA-1 signatures.
func TestVerifySignatureRefuses(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	dir := t.TempDir()
	ca := newSigner(t, dir, "ca", nil, false)
	leaf := newSigner(t, dir, "leaf", ca, false)
	db := Database{Certificates: []*x509.Certificate{ca.cert}}
	uki := ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos"}, "usrhash="+rootHash)
	signed := sbsign(t, dir, uki, leaf)
	if err := verify(signed, db, Database{}); err != nil {
		t.Fatal(err)
	}
	l, err := readLayout(bytes.NewReader(signed), int64(len(signed)))
	if err != nil {
		t.Fatal(err)
	}
	tbs := func(c *x509.Certificate) []byte { s := sha256.Sum256(c.RawTBSCertificate); return s[:] }

	// The certificate table moved into the data of a section, and one followed by data.
	moved := bytes.Clone(signed)
	copy(moved[l.sections[0][0]:], signed[l.certOffset:])
	binary.LittleEndian.PutUint32(moved[l.securityDir:], uint32(l.sections[0][0]))
	trailing := append(bytes.Clone(signed), make([]byte, 8)...)

	// Section headers beyond SizeOfHeaders, which the signature does not cover: the .cmdline and
	// .osrel sections swap their names after signing.
	unhashed := bytes.Clone(uki)
	opt := 0x58
	binary.LittleEndian.PutUint32(unhashed[opt+60:], uint32(opt+112+5*8))
	swapped := sbsign(t, dir, unhashed, leaf)
	table := opt + 240
	first, second := bytes.Clone(swapped[table:table+8]), bytes.Clone(swapped[table+40:table+48])
	copy(swapped[table:], second)
	copy(swapped[table+40:], first)

	sha1Leaf := newSignerWith(t, dir, "sha1", ca, x509.SHA1WithRSA)

	for _, tc := range []struct {
		name  string
		image []byte
		dbx   Database
		want  string
	}{
		{"a certificate table in a section", moved, Database{}, "certificate table"},
		{"data after the certificate table", trailing, Database{}, "certificate table"},
		{"section headers outside the headers", swapped, Database{}, "section table"},
		{"a revoked issuer", signed, Database{Certificates: []*x509.Certificate{ca.cert}}, "dbx revokes the certificate \"CN=ca\""},
		{"a revoked signer by its TBS digest", signed, Database{TBSSHA256: [][]byte{tbs(leaf.cert)}}, "dbx revokes the certificate \"CN=leaf\""},
		{"a revoked issuer by its TBS digest", signed, Database{TBSSHA256: [][]byte{tbs(ca.cert)}}, "dbx revokes the certificate \"CN=ca\""},
		{"a signer certificate signed with SHA-1", sbsign(t, dir, uki, sha1Leaf), Database{}, "SHA-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := verify(tc.image, db, tc.dbx); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("VerifySignature = %v, want %q", err, tc.want)
			}
		})
	}
}

// newSignerWith issues a certificate whose issuer signs it with the algorithm.
func newSignerWith(t *testing.T, dir, name string, parent *signer, alg x509.SignatureAlgorithm) *signer {
	t.Helper()
	s := newSigner(t, dir, name, parent, false)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: s.cert.Subject, NotBefore: s.cert.NotBefore, NotAfter: s.cert.NotAfter, SignatureAlgorithm: alg}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent.cert, s.cert.PublicKey, parent.key)
	if err != nil {
		t.Fatal(err)
	}
	if s.cert, err = x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	writePEM(t, s.certPEM, "CERTIFICATE", der)
	return s
}

// TestECDSASignerRefused refuses Authenticode signatures of ECDSA keys, and chains through an
// ECDSA certificate, which many firmwares cannot verify.
func TestECDSASignerRefused(t *testing.T) {
	dir := t.TempDir()
	ec := newSigner(t, dir, "ec", nil, true)
	ecLeaf := newSigner(t, dir, "ec-leaf", ec, true)
	rsaLeaf := newSigner(t, dir, "rsa-leaf", ec, false)
	uki := ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos"}, "usrhash="+rootHash)
	for _, s := range []*signer{ec, ecLeaf, rsaLeaf} {
		signed := authenticode(t, uki, s.cert, s.key.(crypto.Signer))
		if err := verify(signed, Database{Certificates: []*x509.Certificate{ec.cert}}, Database{}); err == nil || !strings.Contains(err.Error(), "RSA") {
			t.Errorf("a signature of %s = %v", s.cert.Subject.CommonName, err)
		}
	}
}

// EFI signature types, as efivar lists them.
var (
	efiSHA1       = guid(0x826ca512, 0xcf10, 0x4ac9, [8]byte{0xb1, 0x87, 0xbe, 0x01, 0x49, 0x66, 0x31, 0xbd})
	efiSHA384     = guid(0xff3e5307, 0x9fd0, 0x48c9, [8]byte{0x85, 0xf1, 0x8a, 0xd5, 0x6c, 0x70, 0x1e, 0x01})
	efiSHA512     = guid(0x093e0fae, 0xa6c4, 0x4f50, [8]byte{0x9f, 0x1b, 0xd4, 0x1e, 0x2b, 0x89, 0xc1, 0x9a})
	efiX509SHA384 = guid(0x7076876e, 0x80c2, 0x4ee6, [8]byte{0xaa, 0xd2, 0x28, 0xb3, 0x49, 0xa6, 0x86, 0x5b})
	efiX509SHA512 = guid(0x446dbf63, 0x2502, 0x4cda, [8]byte{0xbc, 0xfa, 0x24, 0x65, 0xd2, 0xb0, 0xfe, 0x9d})
)

// TestDbx refuses an image when dbx lists any certificate its signature carries, a certificate
// that issued one of its chain, or its digest of any hash dbx holds, and when dbx holds entries
// it cannot read.
func TestDbx(t *testing.T) {
	dir := t.TempDir()
	root := newSigner(t, dir, "root", nil, false)
	mid := newSigner(t, dir, "mid", root, false)
	leaf := newSigner(t, dir, "leaf", mid, false)
	carried := newSigner(t, dir, "carried", nil, false)
	db := Database{Certificates: []*x509.Certificate{mid.cert}}
	uki := ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos"}, "usrhash="+rootHash)
	signed := authenticode(t, uki, leaf.cert, leaf.key.(crypto.Signer), carried.cert)
	if err := verify(signed, db, Database{}); err != nil {
		t.Fatal(err)
	}
	l, err := readLayout(bytes.NewReader(signed), int64(len(signed)))
	if err != nil {
		t.Fatal(err)
	}
	digest := func(h hash.Hash) []byte {
		d, err := l.digest(bytes.NewReader(signed), h)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	tbs := func(h hash.Hash, c *x509.Certificate) []byte {
		h.Write(c.RawTBSCertificate)
		return append(h.Sum(nil), make([]byte, 16)...)
	}
	for _, tc := range []struct {
		name string
		dbx  []byte
		want string
	}{
		{"a carried certificate", signatureList(certX509, carried.cert.Raw), "dbx revokes the certificate \"CN=carried\""},
		{"a carried certificate by its TBS SHA-256", signatureList(certX509SHA256, tbs(sha256.New(), carried.cert)), "dbx revokes the certificate \"CN=carried\""},
		{"a carried certificate by its TBS SHA-384", signatureList(efiX509SHA384, tbs(sha512.New384(), carried.cert)), "dbx revokes the certificate \"CN=carried\""},
		{"the signer by its TBS SHA-512", signatureList(efiX509SHA512, tbs(sha512.New(), leaf.cert)), "dbx revokes the certificate \"CN=leaf\""},
		{"the issuer of a db certificate", signatureList(certX509, root.cert.Raw), "dbx revokes the certificate \"CN=root\""},
		{"the image's SHA-384", signatureList(efiSHA384, digest(sha512.New384())), "dbx revokes the image"},
		{"the image's SHA-512", signatureList(efiSHA512, digest(sha512.New())), "dbx revokes the image"},
		{"an entry it cannot read", signatureList(efiSHA1, make([]byte, 20)), "cannot read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbx, err := ParseDatabase(tc.dbx)
			if err != nil {
				t.Fatal(err)
			}
			if err := verify(signed, db, dbx); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("VerifySignature = %v, want %q", err, tc.want)
			}
		})
	}
	// Digests of other images and certificates revoke nothing.
	other := sha512.Sum512([]byte("other"))
	dbx, err := ParseDatabase(bytes.Join([][]byte{
		signatureList(efiSHA384, other[:48]),
		signatureList(efiSHA512, other[:]),
		signatureList(efiX509SHA384, append(other[:48:48], make([]byte, 16)...)),
		signatureList(efiX509SHA512, append(other[:64:64], make([]byte, 16)...)),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(signed, db, dbx); err != nil {
		t.Errorf("VerifySignature with unrelated dbx entries = %v", err)
	}
	for name, broken := range map[string][]byte{
		"short SHA-384":     signatureList(efiSHA384, other[:47]),
		"short SHA-512":     signatureList(efiSHA512, other[:63]),
		"short TBS SHA-384": signatureList(efiX509SHA384, other[:48]),
		"short TBS SHA-512": signatureList(efiX509SHA512, other[:64]),
	} {
		if _, err := ParseDatabase(broken); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}
