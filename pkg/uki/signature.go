package uki

import (
	"bytes"
	"cmp"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
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
	"strings"
)

// Database is a Secure Boot signature database, db or dbx: certificates, digests of images, and
// digests of certificates' TBSCertificate, by which dbx revokes certificates.
type Database struct {
	Certificates []*x509.Certificate
	// SHA256, SHA384 and SHA512 are Authenticode digests of images.
	SHA256, SHA384, SHA512 [][]byte
	// TBSSHA256, TBSSHA384 and TBSSHA512 are digests of certificates' TBSCertificate.
	TBSSHA256, TBSSHA384, TBSSHA512 [][]byte
	// Unreadable are the GUIDs of the entry types the database holds that are none of these.
	Unreadable []string
}

// EFI_CERT_X509_GUID, EFI_CERT_SHA{256,384,512}_GUID and EFI_CERT_X509_SHA{256,384,512}_GUID, as
// they are stored.
var (
	certX509       = guid(0xa5c059a1, 0x94e4, 0x4aa7, [8]byte{0x87, 0xb5, 0xab, 0x15, 0x5c, 0x2b, 0xf0, 0x72})
	certSHA256     = guid(0xc1c41626, 0x504c, 0x4092, [8]byte{0xac, 0xa9, 0x41, 0xf9, 0x36, 0x93, 0x43, 0x28})
	certSHA384     = guid(0xff3e5307, 0x9fd0, 0x48c9, [8]byte{0x85, 0xf1, 0x8a, 0xd5, 0x6c, 0x70, 0x1e, 0x01})
	certSHA512     = guid(0x093e0fae, 0xa6c4, 0x4f50, [8]byte{0x9f, 0x1b, 0xd4, 0x1e, 0x2b, 0x89, 0xc1, 0x9a})
	certX509SHA256 = guid(0x3bd2a492, 0x96c0, 0x4079, [8]byte{0xb4, 0x20, 0xfc, 0xf9, 0x8e, 0xf1, 0x03, 0xed})
	certX509SHA384 = guid(0x7076876e, 0x80c2, 0x4ee6, [8]byte{0xaa, 0xd2, 0x28, 0xb3, 0x49, 0xa6, 0x86, 0x5b})
	certX509SHA512 = guid(0x446dbf63, 0x2502, 0x4cda, [8]byte{0xbc, 0xfa, 0x24, 0x65, 0xd2, 0xb0, 0xfe, 0x9d})
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

// guidString formats a stored EFI GUID as it is written.
func guidString(g [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x", binary.LittleEndian.Uint32(g[0:]), binary.LittleEndian.Uint16(g[4:]), binary.LittleEndian.Uint16(g[6:]), g[8:10], g[10:])
}

// ParseDatabase reads the EFI signature lists of a db or dbx variable's value. Entries of types
// other than X.509 certificates and SHA-256, SHA-384 and SHA-512 digests of images and
// certificates are not read; their types are kept in Unreadable.
func ParseDatabase(b []byte) (Database, error) {
	var db Database
	// digest reads an entry of a digest of the size, which the time of a revocation may follow.
	// Firmware revokes only signatures timestamped after that time; timestamps are not checked
	// here, so every signature is.
	digest := func(into *[][]byte, data []byte, size int, revocation bool, what string) error {
		want := size
		if revocation {
			want += 16
		}
		if len(data) != want {
			return fmt.Errorf("a %s entry of another size", what)
		}
		*into = append(*into, bytes.Clone(data[:size]))
		return nil
	}
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
			var err error
			switch typ {
			case certX509:
				cert, perr := x509.ParseCertificate(data)
				if perr != nil {
					return Database{}, fmt.Errorf("a certificate in a signature list: %w", perr)
				}
				db.Certificates = append(db.Certificates, cert)
			case certSHA256:
				err = digest(&db.SHA256, data, sha256.Size, false, "SHA-256")
			case certSHA384:
				err = digest(&db.SHA384, data, sha512.Size384, false, "SHA-384")
			case certSHA512:
				err = digest(&db.SHA512, data, sha512.Size, false, "SHA-512")
			case certX509SHA256:
				err = digest(&db.TBSSHA256, data, sha256.Size, true, "X.509 SHA-256")
			case certX509SHA384:
				err = digest(&db.TBSSHA384, data, sha512.Size384, true, "X.509 SHA-384")
			case certX509SHA512:
				err = digest(&db.TBSSHA512, data, sha512.Size, true, "X.509 SHA-512")
			default:
				if g := guidString(typ); !slices.Contains(db.Unreadable, g) {
					db.Unreadable = append(db.Unreadable, g)
				}
			}
			if err != nil {
				return Database{}, err
			}
		}
		b = b[listSize:]
	}
	return db, nil
}

// VerifySignature checks the Secure Boot signature of the PE image as firmware would: one of
// its signatures must come from a certificate in db, or one that a certificate in db signed, with
// RSA keys alone and no certificate of that chain signed with SHA-1 or MD5, while dbx must list
// neither the image's digest, nor a certificate of the chain or any other the signature carries,
// nor one that issued any of them. A dbx with entries that cannot be read refuses every image, as
// firmware might revoke it by them. Times are not checked, as firmware does not check them. The
// signature must cover the section table, and the certificate table must end the image.
func VerifySignature(r io.ReaderAt, size int64, db, dbx Database) error {
	if len(dbx.Unreadable) > 0 {
		return fmt.Errorf("dbx holds entries of the types %s, which this check cannot read; firmware might revoke the image by them", strings.Join(dbx.Unreadable, ", "))
	}
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
	for _, d := range []struct {
		revoked [][]byte
		hash    func() hash.Hash
	}{{dbx.SHA256, nil}, {dbx.SHA384, sha512.New384}, {dbx.SHA512, sha512.New}} {
		if len(d.revoked) == 0 {
			continue
		}
		sum := digest
		if d.hash != nil {
			if sum, err = layout.digest(r, d.hash()); err != nil {
				return err
			}
		}
		if slices.ContainsFunc(d.revoked, func(r []byte) bool { return bytes.Equal(r, sum) }) {
			return errors.New("dbx revokes the image")
		}
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
			signers, carried, err := verifyPKCS7(table[8:length], digest)
			// dbx revokes a signature by any certificate it carries, whether it verifies or not.
			for _, c := range carried {
				if name, ok := revoked(dbx, c); ok {
					return fmt.Errorf("dbx revokes the certificate %q, which the image's signature carries", name)
				}
			}
			switch {
			case err != nil:
				problems = append(problems, err)
			default:
				for _, signer := range signers {
					// The chain is the signer and the db certificates it is or that signed it; dbx
					// revokes any certificate in it, and any that issued one.
					chain := append([]*x509.Certificate{signer}, issuers(signer, db.Certificates)...)
					for _, c := range chain {
						if name, ok := revoked(dbx, c); ok {
							return fmt.Errorf("dbx revokes the certificate %q, which the image's signature of %q chains to", name, signer.Subject)
						}
					}
					switch {
					case len(chain) == 1:
						problems = append(problems, fmt.Errorf("the certificate %q that signed the image is not in db and no certificate in db signed it", signer.Subject))
					case slices.ContainsFunc(chain, func(c *x509.Certificate) bool { return insecure[c.SignatureAlgorithm] }):
						problems = append(problems, fmt.Errorf("the chain of the certificate %q that signed the image has a certificate signed with SHA-1 or MD5", signer.Subject))
					case slices.ContainsFunc(chain, func(c *x509.Certificate) bool { return c.PublicKeyAlgorithm != x509.RSA }):
						problems = append(problems, fmt.Errorf("the chain of the certificate %q that signed the image has a key other than RSA, which many firmwares do not verify", signer.Subject))
					default:
						trusted = true
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

// revoked returns the certificate by which dbx revokes cert: cert itself, listed or by a digest
// of its TBSCertificate, or a listed certificate that issued it.
func revoked(dbx Database, cert *x509.Certificate) (pkix.Name, bool) {
	for _, d := range []struct {
		revoked [][]byte
		hash    hash.Hash
	}{{dbx.TBSSHA256, sha256.New()}, {dbx.TBSSHA384, sha512.New384()}, {dbx.TBSSHA512, sha512.New()}} {
		if len(d.revoked) == 0 {
			continue
		}
		d.hash.Write(cert.RawTBSCertificate)
		sum := d.hash.Sum(nil)
		if slices.ContainsFunc(d.revoked, func(r []byte) bool { return bytes.Equal(r, sum) }) {
			return cert.Subject, true
		}
	}
	for _, c := range dbx.Certificates {
		if cert.Equal(c) {
			return cert.Subject, true
		}
		if c.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil {
			return c.Subject, true
		}
	}
	return pkix.Name{}, false
}

// insecure are the signature algorithms a chain must not use. x509.Certificate.CheckSignature
// accepts SHA-1.
var insecure = map[x509.SignatureAlgorithm]bool{
	x509.MD2WithRSA: true, x509.MD5WithRSA: true, x509.SHA1WithRSA: true, x509.DSAWithSHA1: true, x509.ECDSAWithSHA1: true,
}

// issuers returns the roots that cert is or that signed it. Constraints on the signing
// certificate are not checked, as firmware does not check them.
func issuers(cert *x509.Certificate, roots []*x509.Certificate) []*x509.Certificate {
	var found []*x509.Certificate
	for _, root := range roots {
		if cert.Equal(root) || root.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil {
			found = append(found, root)
		}
	}
	return found
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
	if _, err := r.ReadAt(optHeader, opt); err != nil || optSize < 2 {
		return layout{}, errors.New("the image's optional header is cut short")
	}
	var dirs int64
	switch binary.LittleEndian.Uint16(optHeader) {
	case 0x10b:
		dirs = 96
	case 0x20b:
		dirs = 112
	default:
		return layout{}, errors.New("the image has an unknown optional header")
	}
	// The certificate table is the fifth data directory; their count precedes them.
	if optSize < dirs+5*8 {
		return layout{}, errors.New("the image's optional header is cut short")
	}
	if count := binary.LittleEndian.Uint32(optHeader[dirs-4:]); count < 5 {
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
	if l.headers < l.securityDir+8 || l.headers > size {
		return layout{}, errors.New("the image's headers are inconsistent")
	}
	// The signature covers the headers up to SizeOfHeaders: section headers beyond it could be
	// changed without breaking it.
	if opt+optSize+40*int64(sections) > l.headers {
		return layout{}, errors.New("the image's section table lies beyond its headers, which its signature covers")
	}
	table := make([]byte, 40*sections)
	if _, err := r.ReadAt(table, opt+optSize); err != nil {
		return layout{}, errors.New("the image's section table is cut short")
	}
	hashed := l.headers
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
		hashed += rawSize
	}
	slices.SortStableFunc(l.sections, func(a, b [2]int64) int { return cmp.Compare(a[0], b[0]) })
	// The certificate table, which the signature leaves out, must be the image's end and lie past
	// all it covers: anything after it would not be covered.
	if l.certSize > 0 && (l.certOffset < hashed || l.certOffset+l.certSize != size) {
		return layout{}, errors.New("the image's certificate table does not follow its headers and sections at its end")
	}
	return l, nil
}

// digest computes the Authenticode digest: the headers without the checksum and the certificate
// table entry, the sections in file order, then what follows them up to the certificate table.
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
	if l.certSize > 0 {
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
// certificates of its signers whose signatures verify, and every certificate it carries, as far
// as it could read them.
func verifyPKCS7(der []byte, digest []byte) (signers, carried []*x509.Certificate, err error) {
	var ci contentInfo
	if rest, err := asn1.Unmarshal(der, &ci); err != nil || !ci.ContentType.Equal(oidSignedData) {
		return nil, nil, errors.New("a signature is not PKCS #7 signed data")
	} else if len(bytes.TrimRight(rest, "\x00")) > 0 {
		return nil, nil, errors.New("a signature has trailing data")
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil, nil, fmt.Errorf("a signature's signed data: %w", err)
	}
	certs, err := x509.ParseCertificates(sd.Certificates.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("a signature's certificates: %w", err)
	}
	if !sd.ContentInfo.ContentType.Equal(oidSpcIndirectData) {
		return nil, certs, errors.New("a signature is not an Authenticode signature")
	}
	// The signature covers the content's DER value, without its tag and length.
	content := sd.ContentInfo.Content
	var indirect spcIndirectData
	if _, err := asn1.Unmarshal(content.Bytes, &indirect); err != nil {
		return nil, certs, fmt.Errorf("a signature's indirect data: %w", err)
	}
	if !indirect.MessageDigest.Algorithm.Algorithm.Equal(oidSHA256) {
		return nil, certs, errors.New("a signature's image digest is not SHA-256")
	}
	if !bytes.Equal(indirect.MessageDigest.Digest, digest) {
		return nil, certs, errors.New("a signature was made for another image")
	}
	var innerContent asn1.RawValue
	if _, err := asn1.Unmarshal(content.Bytes, &innerContent); err != nil {
		return nil, certs, err
	}
	contentDigest := sha256.Sum256(innerContent.Bytes)
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
			return nil, certs, errors.New("a signature has no signer")
		}
		return nil, certs, errors.Join(problems...)
	}
	return signers, certs, nil
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
	// Many firmwares verify RSA signatures alone.
	if _, ok := signer.PublicKey.(*rsa.PublicKey); !ok {
		return nil, fmt.Errorf("the certificate %q that signed the image has a key other than RSA, which many firmwares do not verify", signer.Subject)
	}
	if enc := si.DigestEncryptionAlgorithm.Algorithm; !enc.Equal(oidRSA) && !enc.Equal(oidSHA256WithRSA) {
		return nil, fmt.Errorf("a signer's signature algorithm %v is not supported", enc)
	}
	// The signature covers the attributes encoded as a SET, not with their implicit tag.
	signed := bytes.Clone(si.AuthenticatedAttributes.FullBytes)
	signed[0] = 0x31
	if err := signer.CheckSignature(x509.SHA256WithRSA, signed, si.EncryptedDigest); err != nil {
		return nil, fmt.Errorf("the signature of %q does not verify: %w", signer.Subject, err)
	}
	return signer, nil
}
