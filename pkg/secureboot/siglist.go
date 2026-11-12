package secureboot

import (
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
