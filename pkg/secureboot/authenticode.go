package secureboot

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"slices"
	"strings"
)

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

// VerifyAgainst checks the Secure Boot signature of the PE image as VerifySignature does, against
// a db of the certificates and an empty dbx: as firmware with them enrolled would.
func VerifyAgainst(r io.ReaderAt, size int64, certs []*x509.Certificate) error {
	return VerifySignature(r, size, Database{Certificates: certs}, Database{})
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

// Offsets and sizes of the PE format that Authenticode depends on.
const (
	// peOffsetField holds the offset of the PE signature in the MZ header (e_lfanew).
	peOffsetField = 0x3c
	// coffHeaderSize is the size of the PE signature and the COFF file header after it.
	coffHeaderSize = 24
	// pe32Magic and pe32PlusMagic start the optional header of a PE32 and a PE32+ image.
	pe32Magic     = 0x10b
	pe32PlusMagic = 0x20b
	// sizeOfHeadersOffset and checksumOffset are offsets in the optional header, the same in
	// PE32 and PE32+. Authenticode skips the checksum.
	sizeOfHeadersOffset = 60
	checksumOffset      = 64
	checksumSize        = 4
	// pe32DataDirs and pe32PlusDataDirs are where the data directories start in the optional
	// header; their count is the four bytes before them.
	pe32DataDirs     = 96
	pe32PlusDataDirs = 112
	// dataDirSize is the size of a data directory: an address and a size.
	dataDirSize = 8
	// certTableIndex is the certificate table's data directory, which Authenticode skips.
	certTableIndex = 4
	// sectionHeaderSize is the size of an entry of the section table.
	sectionHeaderSize = 40
)

// layout is where a PE image keeps what Authenticode hashes and skips.
type layout struct {
	size                  int64
	checksum, securityDir int64
	headers               int64
	sections              [][2]int64
	certOffset, certSize  int64
}

// readLayout finds the image's checksum, its certificate table entry, its headers and sections
// and its certificate table. It parses the headers itself because debug/pe exposes neither
// e_lfanew nor the offsets Authenticode skips, and a second parser could read an image otherwise.
func readLayout(r io.ReaderAt, size int64) (layout, error) {
	head := make([]byte, 64)
	if _, err := r.ReadAt(head, 0); err != nil || head[0] != 'M' || head[1] != 'Z' {
		return layout{}, errors.New("not a PE image")
	}
	peOff := int64(binary.LittleEndian.Uint32(head[peOffsetField:]))
	coff := make([]byte, coffHeaderSize)
	if _, err := r.ReadAt(coff, peOff); err != nil || !bytes.Equal(coff[:4], []byte("PE\x00\x00")) {
		return layout{}, errors.New("not a PE image")
	}
	sections := int(binary.LittleEndian.Uint16(coff[6:]))
	optSize := int64(binary.LittleEndian.Uint16(coff[20:]))
	opt := peOff + coffHeaderSize
	optHeader := make([]byte, optSize)
	if _, err := r.ReadAt(optHeader, opt); err != nil || optSize < 2 {
		return layout{}, errors.New("the image's optional header is cut short")
	}
	var dirs int64
	switch binary.LittleEndian.Uint16(optHeader) {
	case pe32Magic:
		dirs = pe32DataDirs
	case pe32PlusMagic:
		dirs = pe32PlusDataDirs
	default:
		return layout{}, errors.New("the image has an unknown optional header")
	}
	certDir := dirs + certTableIndex*dataDirSize
	if optSize < certDir+dataDirSize {
		return layout{}, errors.New("the image's optional header is cut short")
	}
	if count := binary.LittleEndian.Uint32(optHeader[dirs-4:]); count <= certTableIndex {
		return layout{}, errors.New("the image has no certificate table entry")
	}
	l := layout{
		size:        size,
		checksum:    opt + checksumOffset,
		securityDir: opt + certDir,
		headers:     int64(binary.LittleEndian.Uint32(optHeader[sizeOfHeadersOffset:])),
		certOffset:  int64(binary.LittleEndian.Uint32(optHeader[certDir:])),
		certSize:    int64(binary.LittleEndian.Uint32(optHeader[certDir+4:])),
	}
	if l.headers < l.securityDir+dataDirSize || l.headers > size {
		return layout{}, errors.New("the image's headers are inconsistent")
	}
	// The signature covers the headers up to SizeOfHeaders: section headers beyond it could be
	// changed without breaking it.
	if opt+optSize+sectionHeaderSize*int64(sections) > l.headers {
		return layout{}, errors.New("the image's section table lies beyond its headers, which its signature covers")
	}
	table := make([]byte, sectionHeaderSize*sections)
	if _, err := r.ReadAt(table, opt+optSize); err != nil {
		return layout{}, errors.New("the image's section table is cut short")
	}
	hashed := l.headers
	for i := range sections {
		s := table[sectionHeaderSize*i:]
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
	regions := [][2]int64{{0, l.checksum}, {l.checksum + checksumSize, l.securityDir}, {l.securityDir + dataDirSize, l.headers}}
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
