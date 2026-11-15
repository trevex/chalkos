package secureboot

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
)

// X509SignatureLists encodes the certificates as EFI signature lists of X.509 certificates, one
// list of one entry for each, as their sizes differ, each owned by the owner GUID.
func X509SignatureLists(owner [16]byte, certs ...*x509.Certificate) []byte {
	var b []byte
	for _, c := range certs {
		size := len(owner) + len(c.Raw)
		b = appendListHeader(b, certX509, size, 1)
		b = append(b, owner[:]...)
		b = append(b, c.Raw...)
	}
	return b
}

// SHA256SignatureList encodes the SHA-256 Authenticode digests of images, each of 32 bytes, as
// one EFI signature list whose entries the owner GUID owns; nothing for no digests. A digest in
// db allows its image without a signature.
func SHA256SignatureList(owner [16]byte, digests ...[]byte) []byte {
	if len(digests) == 0 {
		return nil
	}
	size := len(owner) + sha256.Size
	b := appendListHeader(nil, certSHA256, size, len(digests))
	for _, d := range digests {
		b = append(b, owner[:]...)
		b = append(b, d...)
	}
	return b
}

// appendListHeader appends the header of an EFI signature list of the type, without a signature
// header, of n entries of size bytes each.
func appendListHeader(b []byte, typ [16]byte, size, n int) []byte {
	b = append(b, typ[:]...)
	b = binary.LittleEndian.AppendUint32(b, uint32(sigListHeaderSize+size*n))
	b = binary.LittleEndian.AppendUint32(b, 0)
	return binary.LittleEndian.AppendUint32(b, uint32(size))
}
