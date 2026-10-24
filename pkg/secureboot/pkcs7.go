package secureboot

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
)

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
