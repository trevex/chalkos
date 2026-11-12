package secureboot

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
	"unicode/utf16"
)

// Attributes of the Secure Boot variables an update writes: non-volatile, boot service and
// runtime access, and time-based authenticated writes, AttrAppend also appending to the value,
// as db and KEK are written, AttrReplace replacing it, as PK is.
const (
	AttrReplace uint32 = 0x27
	AttrAppend  uint32 = AttrReplace | 0x40
)

// EFI_CERT_TYPE_PKCS7_GUID as it is stored: the type of a WIN_CERTIFICATE_UEFI_GUID that holds a
// PKCS #7 signature.
var certTypePKCS7 = guid(0x4aafd29d, 0x68df, 0x49ee, [8]byte{0x8a, 0xa9, 0x34, 0x7d, 0x37, 0x56, 0x65, 0xa7})

// The sizes of an EFI_TIME and of the header of a WIN_CERTIFICATE_UEFI_GUID: its length, revision,
// certificate type and the GUID of the type of its data.
const (
	efiTimeSize = 16
	winCertSize = 24
)

// Update is a new value of an authenticated EFI variable, such as db.
type Update struct {
	Name       string
	Vendor     [16]byte
	Attributes uint32
	// Data is the value: for db, KEK and PK, EFI signature lists.
	Data []byte
}

// SignUpdate signs an update as a time-based authenticated write at the time t, as SetVariable
// takes it: an EFI_VARIABLE_AUTHENTICATION_2 descriptor followed by the data. The descriptor
// holds the time and a PKCS #7 signature by the certificate's key over the variable's name,
// vendor and attributes, the time and the data. The attributes are not part of what it returns;
// efivarfs takes them in front of it.
func SignUpdate(u Update, t time.Time, cert *x509.Certificate, key *rsa.PrivateKey) ([]byte, error) {
	t = t.UTC()
	var efiTime [efiTimeSize]byte
	binary.LittleEndian.PutUint16(efiTime[0:], uint16(t.Year()))
	efiTime[2], efiTime[3], efiTime[4], efiTime[5], efiTime[6] = byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second())
	return signUpdate(u, efiTime, cert, key)
}

// signUpdate is SignUpdate at the EFI_TIME given.
func signUpdate(u Update, efiTime [efiTimeSize]byte, cert *x509.Certificate, key *rsa.PrivateKey) ([]byte, error) {
	sig, err := signPKCS7(signedBytes(u.Name, u.Vendor, u.Attributes, efiTime, u.Data), cert, key)
	if err != nil {
		return nil, err
	}
	b := append([]byte{}, efiTime[:]...)
	b = binary.LittleEndian.AppendUint32(b, uint32(winCertSize+len(sig)))
	b = binary.LittleEndian.AppendUint16(b, 0x0200)
	b = binary.LittleEndian.AppendUint16(b, 0x0ef1)
	b = append(b, certTypePKCS7[:]...)
	b = append(b, sig...)
	return append(b, u.Data...), nil
}

// signedBytes are what an update's signature covers: the variable's name in UTF-16LE without
// its terminator, its vendor, its attributes, the time and the data.
func signedBytes(name string, vendor [16]byte, attrs uint32, efiTime [efiTimeSize]byte, data []byte) []byte {
	var b []byte
	for _, c := range utf16.Encode([]rune(name)) {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	b = append(b, vendor[:]...)
	b = binary.LittleEndian.AppendUint32(b, attrs)
	b = append(b, efiTime[:]...)
	return append(b, data...)
}

// ParsedUpdate is an update as SignUpdate writes it, read back.
type ParsedUpdate struct {
	Time    time.Time
	efiTime [efiTimeSize]byte
	// SignedData is the PKCS #7 signature, DER SignedData.
	SignedData []byte
	Data       []byte
}

// ParseUpdate reads an update SignUpdate wrote: its time, its signature and its data.
func ParseUpdate(b []byte) (ParsedUpdate, error) {
	if len(b) < efiTimeSize+winCertSize {
		return ParsedUpdate{}, errors.New("the update is cut short")
	}
	var p ParsedUpdate
	copy(p.efiTime[:], b)
	// Pad1, Nanosecond, TimeZone, Daylight and Pad2 are zero in an authenticated write.
	if !bytes.Equal(b[7:efiTimeSize], make([]byte, efiTimeSize-7)) {
		return ParsedUpdate{}, errors.New("the update's EFI_TIME sets more than the date and time")
	}
	p.Time = time.Date(int(binary.LittleEndian.Uint16(b)), time.Month(b[2]), int(b[3]), int(b[4]), int(b[5]), int(b[6]), 0, time.UTC)
	length := binary.LittleEndian.Uint32(b[efiTimeSize:])
	if binary.LittleEndian.Uint16(b[efiTimeSize+4:]) != 0x0200 || binary.LittleEndian.Uint16(b[efiTimeSize+6:]) != 0x0ef1 {
		return ParsedUpdate{}, errors.New("the update's WIN_CERTIFICATE is not of revision 2.0 and the type EFI_GUID")
	}
	if !bytes.Equal(b[efiTimeSize+8:efiTimeSize+winCertSize], certTypePKCS7[:]) {
		return ParsedUpdate{}, errors.New("the update's signature is not PKCS #7")
	}
	if length < winCertSize || uint64(length) > uint64(len(b)-efiTimeSize) {
		return ParsedUpdate{}, fmt.Errorf("the update's signature has the length %d, which does not fit", length)
	}
	p.SignedData = b[efiTimeSize+winCertSize : efiTimeSize+length]
	p.Data = b[efiTimeSize+length:]
	return p, nil
}

// Verify checks that the update is signed by the certificate for the variable of the name and
// vendor written with the attributes.
func (p ParsedUpdate) Verify(name string, vendor [16]byte, attrs uint32, cert *x509.Certificate) error {
	return verifyDetached(p.SignedData, signedBytes(name, vendor, attrs, p.efiTime, p.Data), cert)
}

// X509Certificates returns the certificates of the update's data, which must be EFI signature
// lists of X.509 certificates alone.
func (p ParsedUpdate) X509Certificates() ([]*x509.Certificate, error) {
	db, err := p.DBEntries()
	if err != nil {
		return nil, err
	}
	if len(db.SHA256) > 0 {
		return nil, errors.New("the signature lists hold entries other than X.509 certificates")
	}
	return db.Certificates, nil
}

// DBEntries returns the certificates and the SHA-256 digests of images of the update's data,
// which must be EFI signature lists of these alone, as an update of db may hold.
func (p ParsedUpdate) DBEntries() (Database, error) {
	db, err := ParseDatabase(p.Data)
	if err != nil {
		return Database{}, err
	}
	if len(db.Unreadable) > 0 || len(db.SHA384)+len(db.SHA512)+len(db.TBSSHA256)+len(db.TBSSHA384)+len(db.TBSSHA512) > 0 {
		return Database{}, errors.New("the signature lists hold entries other than X.509 certificates and SHA-256 digests of images")
	}
	return db, nil
}
