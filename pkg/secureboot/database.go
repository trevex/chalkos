// Package secureboot checks EFI binaries as UEFI Secure Boot does: it reads the firmware's
// signature databases, db and dbx, and verifies Authenticode signatures against them.
package secureboot

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
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

// sigListHeaderSize is the size of an EFI_SIGNATURE_LIST's header: its type and three sizes.
const sigListHeaderSize = 28

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
		if len(b) < sigListHeaderSize {
			return Database{}, errors.New("a signature list is cut short")
		}
		var typ [16]byte
		copy(typ[:], b)
		listSize := binary.LittleEndian.Uint32(b[16:])
		headerSize := binary.LittleEndian.Uint32(b[20:])
		sigSize := binary.LittleEndian.Uint32(b[24:])
		if listSize < sigListHeaderSize || uint64(listSize) > uint64(len(b)) || uint64(headerSize) > uint64(listSize)-sigListHeaderSize || sigSize < 16 {
			return Database{}, errors.New("a signature list has inconsistent sizes")
		}
		entries := b[sigListHeaderSize+headerSize : listSize]
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

// Vendors of the EFI variables Databases reads: the global one of SecureBoot and SetupMode, and
// that of the signature databases.
const (
	GlobalVendor   = "8be4df61-93ca-11d2-aa0d-00e098032b8c"
	SecurityVendor = "d719b2cb-3d3a-4596-a3bc-dad00e67656f"
)

// Databases returns db and dbx when Secure Boot is enforced, and enforced false when it is not:
// disabled, or in setup mode. read returns the value of the EFI variable of the name and vendor,
// nil when it is not set.
func Databases(read func(name, vendor string) ([]byte, error)) (db, dbx Database, enforced bool, err error) {
	enabled, err := read("SecureBoot", GlobalVendor)
	if err != nil {
		return Database{}, Database{}, false, err
	}
	setup, err := read("SetupMode", GlobalVendor)
	if err != nil {
		return Database{}, Database{}, false, err
	}
	if len(enabled) != 1 || enabled[0] != 1 || len(setup) == 1 && setup[0] == 1 {
		return Database{}, Database{}, false, nil
	}
	for _, v := range []struct {
		name string
		into *Database
	}{{"db", &db}, {"dbx", &dbx}} {
		data, err := read(v.name, SecurityVendor)
		if err != nil {
			return Database{}, Database{}, false, err
		}
		if *v.into, err = ParseDatabase(data); err != nil {
			return Database{}, Database{}, false, fmt.Errorf("the firmware's %s: %w", v.name, err)
		}
	}
	return db, dbx, true, nil
}
