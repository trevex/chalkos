package uki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"slices"
)

// Database is a Secure Boot signature database, db or dbx: certificates and SHA-256 digests of
// images.
type Database struct {
	Certificates []*x509.Certificate
	SHA256       [][]byte
}

// EFI_CERT_X509_GUID and EFI_CERT_SHA256_GUID, as they are stored.
var (
	certX509   = guid(0xa5c059a1, 0x94e4, 0x4aa7, [8]byte{0x87, 0xb5, 0xab, 0x15, 0x5c, 0x2b, 0xf0, 0x72})
	certSHA256 = guid(0xc1c41626, 0x504c, 0x4092, [8]byte{0xac, 0xa9, 0x41, 0xf9, 0x36, 0x93, 0x43, 0x28})
)

// guid encodes an EFI GUID: its first three fields little-endian.
func guid(a uint32, b, c uint16, d [8]byte) [16]byte {
	var g [16]byte
	binary.LittleEndian.PutUint32(g[0:], a)
	binary.LittleEndian.PutUint16(g[4:], b)
	binary.LittleEndian.PutUint16(g[6:], c)
	copy(g[8:], d[:])
	return g
}

// ParseDatabase reads the EFI signature lists of a db or dbx variable's value. Entries of types
// other than X.509 certificates and SHA-256 digests are skipped.
func ParseDatabase(b []byte) (Database, error) {
	var db Database
	for len(b) > 0 {
		if len(b) < 28 {
			return Database{}, errors.New("a signature list is cut short")
		}
		var typ [16]byte
		copy(typ[:], b)
		listSize := binary.LittleEndian.Uint32(b[16:])
		headerSize := binary.LittleEndian.Uint32(b[20:])
		sigSize := binary.LittleEndian.Uint32(b[24:])
		if listSize < 28 || uint64(listSize) > uint64(len(b)) || uint64(headerSize) > uint64(listSize)-28 || sigSize < 16 {
			return Database{}, errors.New("a signature list has inconsistent sizes")
		}
		entries := b[28+headerSize : listSize]
		if uint32(len(entries))%sigSize != 0 {
			return Database{}, errors.New("a signature list does not hold whole entries")
		}
		for ; len(entries) > 0; entries = entries[sigSize:] {
			// Each entry starts with its owner's GUID.
			data := entries[16:sigSize]
			switch typ {
			case certX509:
				cert, err := x509.ParseCertificate(data)
				if err != nil {
					return Database{}, fmt.Errorf("a certificate in a signature list: %w", err)
				}
				db.Certificates = append(db.Certificates, cert)
			case certSHA256:
				if len(data) != sha256.Size {
					return Database{}, errors.New("a SHA-256 entry of another size")
				}
				db.SHA256 = append(db.SHA256, bytes.Clone(data))
			}
		}
		b = b[listSize:]
	}
	return db, nil
}

// VerifySignature checks the Secure Boot signature of the PE image as firmware would: one of
// its signatures must come from a certificate in db, or one that a certificate in db signed, while
// dbx must list neither the image's digest nor a certificate of a signature. Times are not
// checked, as firmware does not check them.
func VerifySignature(r io.ReaderAt, size int64, db, dbx Database) error {
	layout, err := readLayout(r, size)
	if err != nil {
		return err
	}
	if layout.certSize == 0 {
		return errors.New("the image is not signed")
	}
	table := make([]byte, layout.certSize)
	if _, err := r.ReadAt(table, layout.certOffset); err != nil {
		return fmt.Errorf("read the image's signatures: %w", err)
	}
	digest, err := layout.digest(r, sha256.New())
	if err != nil {
		return err
	}
	if slices.ContainsFunc(dbx.SHA256, func(d []byte) bool { return bytes.Equal(d, digest) }) {
		return errors.New("dbx revokes the image")
	}
	var problems []error
	trusted := false
	for len(table) > 0 {
		if len(table) < 8 {
			return errors.New("the image's signature table is cut short")
		}
		length := binary.LittleEndian.Uint32(table)
		revision := binary.LittleEndian.Uint16(table[4:])
		typ := binary.LittleEndian.Uint16(table[6:])
		if length < 8 || uint64(length) > uint64(len(table)) {
			return errors.New("the image's signature table has an entry of the wrong size")
		}
		// WIN_CERT_REVISION_2_0 and WIN_CERT_TYPE_PKCS_SIGNED_DATA: an Authenticode signature.
		if revision == 0x0200 && typ == 0x0002 {
			signers, err := verifyPKCS7(table[8:length], digest)
			switch {
			case err != nil:
				problems = append(problems, err)
			default:
				for _, signer := range signers {
					if listed(dbx.Certificates, signer) {
						return fmt.Errorf("dbx revokes the certificate %q that signed the image", signer.Subject)
					}
					if signedBy(signer, db.Certificates) {
						trusted = true
					} else {
						problems = append(problems, fmt.Errorf("the certificate %q that signed the image is not in db and no certificate in db signed it", signer.Subject))
					}
				}
			}
		}
		// Entries are aligned to eight bytes.
		next := (uint64(length) + 7) &^ 7
		if next >= uint64(len(table)) {
			break
		}
		table = table[next:]
	}
	if trusted {
		return nil
	}
	if len(problems) == 0 {
		return errors.New("the image has no Authenticode signature")
	}
	return errors.Join(problems...)
}

func listed(certs []*x509.Certificate, cert *x509.Certificate) bool {
	return slices.ContainsFunc(certs, cert.Equal)
}

// signedBy reports whether cert is one of roots or was signed by one. Constraints on the signing
// certificate are not checked, as firmware does not check them.
func signedBy(cert *x509.Certificate, roots []*x509.Certificate) bool {
	for _, root := range roots {
		if cert.Equal(root) || root.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil {
			return true
		}
	}
	return false
}

// layout is where a PE image keeps what Authenticode hashes and skips.
type layout struct {
	size                  int64
	checksum, securityDir int64
	headers               int64
	sections              [][2]int64
	certOffset, certSize  int64
}

// readLayout finds the image's checksum, its certificate table entry, its headers and sections
// and its certificate table.
func readLayout(r io.ReaderAt, size int64) (layout, error) {
	head := make([]byte, 64)
	if _, err := r.ReadAt(head, 0); err != nil || head[0] != 'M' || head[1] != 'Z' {
		return layout{}, errors.New("not a PE image")
	}
	peOff := int64(binary.LittleEndian.Uint32(head[0x3c:]))
	coff := make([]byte, 24)
	if _, err := r.ReadAt(coff, peOff); err != nil || !bytes.Equal(coff[:4], []byte("PE\x00\x00")) {
		return layout{}, errors.New("not a PE image")
	}
	sections := int(binary.LittleEndian.Uint16(coff[6:]))
	optSize := int64(binary.LittleEndian.Uint16(coff[20:]))
	opt := peOff + 24
	optHeader := make([]byte, optSize)
	if _, err := r.ReadAt(optHeader, opt); err != nil || optSize < 96 {
		return layout{}, errors.New("the image's optional header is cut short")
	}
	var dirs, count int64
	switch binary.LittleEndian.Uint16(optHeader) {
	case 0x10b:
		dirs, count = 96, int64(binary.LittleEndian.Uint32(optHeader[92:]))
	case 0x20b:
		dirs, count = 112, int64(binary.LittleEndian.Uint32(optHeader[108:]))
	default:
		return layout{}, errors.New("the image has an unknown optional header")
	}
	// The certificate table is the fifth data directory.
	if count < 5 || dirs+5*8 > optSize {
		return layout{}, errors.New("the image has no certificate table entry")
	}
	l := layout{
		size:        size,
		checksum:    opt + 64,
		securityDir: opt + dirs + 4*8,
		headers:     int64(binary.LittleEndian.Uint32(optHeader[60:])),
		certOffset:  int64(binary.LittleEndian.Uint32(optHeader[dirs+32:])),
		certSize:    int64(binary.LittleEndian.Uint32(optHeader[dirs+36:])),
	}
	if l.headers < l.securityDir+8 || l.headers > size || l.certOffset+l.certSize > size || l.certSize > 0 && l.certOffset < l.headers {
		return layout{}, errors.New("the image's headers are inconsistent")
	}
	table := make([]byte, 40*sections)
	if _, err := r.ReadAt(table, opt+optSize); err != nil {
		return layout{}, errors.New("the image's section table is cut short")
	}
	for i := range sections {
		s := table[40*i:]
		rawSize := int64(binary.LittleEndian.Uint32(s[16:]))
		rawOff := int64(binary.LittleEndian.Uint32(s[20:]))
		if rawSize == 0 {
			continue
		}
		if rawOff+rawSize > size {
			return layout{}, errors.New("a section of the image lies beyond its end")
		}
		l.sections = append(l.sections, [2]int64{rawOff, rawSize})
	}
	slices.SortFunc(l.sections, func(a, b [2]int64) int { return int(a[0] - b[0]) })
	return l, nil
}

// digest computes the Authenticode digest: the headers without the checksum and the certificate
// table entry, the sections in file order, then what follows them but the certificate table.
func (l layout) digest(r io.ReaderAt, h hash.Hash) ([]byte, error) {
	add := func(from, to int64) error {
		if to <= from {
			return nil
		}
		_, err := io.Copy(h, io.NewSectionReader(r, from, to-from))
		return err
	}
	hashed := l.headers
	regions := [][2]int64{{0, l.checksum}, {l.checksum + 4, l.securityDir}, {l.securityDir + 8, l.headers}}
	for _, s := range l.sections {
		regions = append(regions, [2]int64{s[0], s[0] + s[1]})
		hashed += s[1]
	}
	end := l.size
	if l.certSize > 0 && l.certOffset+l.certSize == l.size {
		end = l.certOffset
	}
	regions = append(regions, [2]int64{hashed, end})
	for _, region := range regions {
		if err := add(region[0], region[1]); err != nil {
			return nil, fmt.Errorf("hash the image: %w", err)
		}
	}
	return h.Sum(nil), nil
}

var (
	oidSignedData      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidSpcIndirectData = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 1, 4}
	oidContentType     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSHA256          = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidRSA             = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidSHA256WithRSA   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	oidECDSAWithSHA256 = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
)

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,optional,tag:0"`
}

type signedData struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	ContentInfo      contentInfo
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue `asn1:"optional,tag:1"`
	SignerInfos      []signerInfo  `asn1:"set"`
}

type issuerAndSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

type signerInfo struct {
	Version                   int
	IssuerAndSerial           issuerAndSerial
	DigestAlgorithm           pkix.AlgorithmIdentifier
	AuthenticatedAttributes   asn1.RawValue `asn1:"optional,tag:0"`
	DigestEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedDigest           []byte
	UnauthenticatedAttributes asn1.RawValue `asn1:"optional,tag:1"`
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

type spcIndirectData struct {
	Data          asn1.RawValue
	MessageDigest struct {
		Algorithm pkix.AlgorithmIdentifier
		Digest    []byte
	}
}

// verifyPKCS7 checks an Authenticode signature over the image's digest and returns the
// certificates of its signers whose signatures verify.
func verifyPKCS7(der []byte, digest []byte) ([]*x509.Certificate, error) {
	var ci contentInfo
	if rest, err := asn1.Unmarshal(der, &ci); err != nil || !ci.ContentType.Equal(oidSignedData) {
		return nil, errors.New("a signature is not PKCS #7 signed data")
	} else if len(bytes.TrimRight(rest, "\x00")) > 0 {
		return nil, errors.New("a signature has trailing data")
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil, fmt.Errorf("a signature's signed data: %w", err)
	}
	if !sd.ContentInfo.ContentType.Equal(oidSpcIndirectData) {
		return nil, errors.New("a signature is not an Authenticode signature")
	}
	// The signature covers the content's DER value, without its tag and length.
	content := sd.ContentInfo.Content
	var indirect spcIndirectData
	if _, err := asn1.Unmarshal(content.Bytes, &indirect); err != nil {
		return nil, fmt.Errorf("a signature's indirect data: %w", err)
	}
	if !indirect.MessageDigest.Algorithm.Algorithm.Equal(oidSHA256) {
		return nil, errors.New("a signature's image digest is not SHA-256")
	}
	if !bytes.Equal(indirect.MessageDigest.Digest, digest) {
		return nil, errors.New("a signature was made for another image")
	}
	var innerContent asn1.RawValue
	if _, err := asn1.Unmarshal(content.Bytes, &innerContent); err != nil {
		return nil, err
	}
	contentDigest := sha256.Sum256(innerContent.Bytes)
	certs, err := x509.ParseCertificates(sd.Certificates.Bytes)
	if err != nil {
		return nil, fmt.Errorf("a signature's certificates: %w", err)
	}
	var signers []*x509.Certificate
	var problems []error
	for _, si := range sd.SignerInfos {
		signer, err := verifySigner(si, certs, contentDigest[:])
		if err != nil {
			problems = append(problems, err)
			continue
		}
		signers = append(signers, signer)
	}
	if len(signers) == 0 {
		if len(problems) == 0 {
			return nil, errors.New("a signature has no signer")
		}
		return nil, errors.Join(problems...)
	}
	return signers, nil
}

// verifySigner checks one signer: its authenticated attributes name the Authenticode content and
// its digest, and its certificate signed them.
func verifySigner(si signerInfo, certs []*x509.Certificate, contentDigest []byte) (*x509.Certificate, error) {
	if !si.DigestAlgorithm.Algorithm.Equal(oidSHA256) {
		return nil, errors.New("a signer's digest is not SHA-256")
	}
	if len(si.AuthenticatedAttributes.FullBytes) == 0 {
		return nil, errors.New("a signer has no authenticated attributes")
	}
	var attrs []attribute
	if _, err := asn1.UnmarshalWithParams(si.AuthenticatedAttributes.FullBytes, &attrs, "set,tag:0"); err != nil {
		return nil, fmt.Errorf("a signer's attributes: %w", err)
	}
	var typeOK, digestOK bool
	for _, a := range attrs {
		switch {
		case a.Type.Equal(oidContentType):
			var oid asn1.ObjectIdentifier
			_, err := asn1.Unmarshal(a.Values.Bytes, &oid)
			typeOK = err == nil && oid.Equal(oidSpcIndirectData)
		case a.Type.Equal(oidMessageDigest):
			var d []byte
			_, err := asn1.Unmarshal(a.Values.Bytes, &d)
			digestOK = err == nil && bytes.Equal(d, contentDigest)
		}
	}
	if !typeOK || !digestOK {
		return nil, errors.New("a signer's attributes do not cover the image's digest")
	}
	var signer *x509.Certificate
	for _, c := range certs {
		if bytes.Equal(c.RawIssuer, si.IssuerAndSerial.Issuer.FullBytes) && c.SerialNumber.Cmp(si.IssuerAndSerial.Serial) == 0 {
			signer = c
			break
		}
	}
	if signer == nil {
		return nil, errors.New("a signature does not carry its signer's certificate")
	}
	var alg x509.SignatureAlgorithm
	switch signer.PublicKey.(type) {
	case *rsa.PublicKey:
		alg = x509.SHA256WithRSA
	case *ecdsa.PublicKey:
		alg = x509.ECDSAWithSHA256
	default:
		return nil, errors.New("a signer's key is neither RSA nor ECDSA")
	}
	if enc := si.DigestEncryptionAlgorithm.Algorithm; !enc.Equal(oidRSA) && !enc.Equal(oidSHA256WithRSA) && !enc.Equal(oidECDSAWithSHA256) {
		return nil, fmt.Errorf("a signer's signature algorithm %v is not supported", enc)
	}
	// The signature covers the attributes encoded as a SET, not with their implicit tag.
	signed := bytes.Clone(si.AuthenticatedAttributes.FullBytes)
	signed[0] = 0x31
	if err := signer.CheckSignature(alg, signed, si.EncryptedDigest); err != nil {
		return nil, fmt.Errorf("the signature of %q does not verify: %w", signer.Subject, err)
	}
	return signer, nil
}
