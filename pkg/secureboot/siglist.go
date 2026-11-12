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
		b = append(b, certX509[:]...)
		b = binary.LittleEndian.AppendUint32(b, uint32(sigListHeaderSize+size))
		// No signature header.
		b = binary.LittleEndian.AppendUint32(b, 0)
		b = binary.LittleEndian.AppendUint32(b, uint32(size))
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
	b := append([]byte{}, certSHA256[:]...)
	b = binary.LittleEndian.AppendUint32(b, uint32(sigListHeaderSize+size*len(digests)))
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, uint32(size))
	for _, d := range digests {
		b = append(b, owner[:]...)
		b = append(b, d...)
	}
	return b
}
