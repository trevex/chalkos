package secureboot

import (
	"bytes"
	"fmt"

	pecoff "github.com/foxboron/go-uefi/authenticode"
)

// LoadSigner reads a Secure Boot key that signs images, such as db, from the PEM files keyPath,
// PKCS #8 or PKCS #1, and certPath, and refuses a key that is not the certificate's.
func LoadSigner(keyPath, certPath string) (KeyPair, error) {
	return loadPair(keyPath, certPath, true)
}

// Sign signs the PE image, a UKI or a boot loader, for Secure Boot with the key pair: it appends
// an Authenticode signature of the image's SHA-256 digest that carries the certificate, as sbsign
// does. An image whose size is not a multiple of eight is padded with zeros first, as sbsign pads
// it, so that the certificate table that follows the image is aligned.
func Sign(image []byte, signer KeyPair) ([]byte, error) {
	image = append(bytes.Clone(image), make([]byte, (8-len(image)%8)%8)...)
	pe, err := pecoff.Parse(bytes.NewReader(image))
	if err != nil {
		return nil, fmt.Errorf("read the PE image: %w", err)
	}
	if _, err := pe.Sign(signer.Key, signer.Cert); err != nil {
		return nil, err
	}
	return pe.Bytes(), nil
}
