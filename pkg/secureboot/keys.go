package secureboot

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/pki"
)

// KeyPair is a Secure Boot key and its self-signed certificate.
type KeyPair struct {
	Cert *x509.Certificate
	Key  *rsa.PrivateKey
}

// Keys are the Secure Boot keys of a machine's owner: the platform key PK, the key exchange key
// KEK, the db key that signs images, and the GUID that owns their entries in the firmware's
// signature lists.
type Keys struct {
	PK, KEK, DB KeyPair
	Owner       [16]byte
}

// keyFiles are the names of the keys in a key directory, without .key and .crt, as the firmware
// names its variables.
var keyFiles = []string{"PK", "KEK", "db"}

// ownerFile holds the owner GUID in a key directory.
const ownerFile = "owner.txt"

// NewKeys creates PK, KEK and db in dir: RSA 2048 keys, which firmware commonly supports alone,
// in name.key, readable by their owner alone, and self-signed SHA-256 certificates in name.crt,
// valid for 20 years and named "<name> Secure Boot PK", "... KEK" and "... db". owner.txt holds a
// random GUID, the owner of the entries enrolled. dir is created, or must be empty, and is made
// readable by its owner alone. When NewKeys fails, it removes the files it wrote.
func NewKeys(dir, name string) (err error) {
	if err := os.Mkdir(dir, 0o700); errors.Is(err, fs.ErrExist) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("%s is not empty", dir)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			for _, file := range keyFiles {
				os.Remove(filepath.Join(dir, file+".key"))
				os.Remove(filepath.Join(dir, file+".crt"))
			}
		}
	}()
	now := time.Now()
	for _, file := range keyFiles {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
		if err != nil {
			return err
		}
		tmpl := &x509.Certificate{
			SerialNumber:          serial,
			Subject:               pkix.Name{CommonName: name + " Secure Boot " + file},
			NotBefore:             now.Add(-time.Hour),
			NotAfter:              now.AddDate(20, 0, 0),
			KeyUsage:              x509.KeyUsageDigitalSignature,
			BasicConstraintsValid: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			return err
		}
		keyPEM, err := pki.EncodeKey(key)
		if err != nil {
			return err
		}
		if err := atomicfile.WriteNew(filepath.Join(dir, file+".key"), []byte(keyPEM), 0o600); err != nil {
			return err
		}
		if err := atomicfile.WriteNew(filepath.Join(dir, file+".crt"), []byte(pki.EncodeCertificate(der)), 0o644); err != nil {
			return err
		}
	}
	owner, err := NewGUID()
	if err != nil {
		return err
	}
	return atomicfile.WriteNew(filepath.Join(dir, ownerFile), []byte(GUIDString(owner)+"\n"), 0o644)
}

// LoadKeys reads the keys NewKeys writes from dir. A key may be PKCS #8 or PKCS #1 PEM, as
// openssl writes them.
func LoadKeys(dir string) (Keys, error) {
	return loadKeys(dir, true)
}

// LoadEnrolmentKeys reads what an enrolment needs from dir: PK and KEK with their keys, which sign
// the updates, the db certificate without its key, which stays with whoever signs images, and the
// owner GUID.
func LoadEnrolmentKeys(dir string) (Keys, error) {
	return loadKeys(dir, false)
}

func loadKeys(dir string, dbKey bool) (Keys, error) {
	var k Keys
	for i, into := range []*KeyPair{&k.PK, &k.KEK, &k.DB} {
		var err error
		if *into, err = loadKeyPair(dir, keyFiles[i], i < 2 || dbKey); err != nil {
			return Keys{}, err
		}
	}
	path := filepath.Join(dir, ownerFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return Keys{}, err
	}
	if k.Owner, err = ParseGUID(strings.TrimSpace(string(data))); err != nil {
		return Keys{}, fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

// loadKeyPair reads the certificate of the name in dir, and its key when withKey is set.
func loadKeyPair(dir, name string, withKey bool) (KeyPair, error) {
	certPath, keyPath := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	data, err := os.ReadFile(certPath)
	if err != nil {
		return KeyPair{}, err
	}
	cert, err := pki.ParseCertificate(data)
	if err != nil {
		return KeyPair{}, fmt.Errorf("%s: %w", certPath, err)
	}
	if !withKey {
		return KeyPair{Cert: cert}, nil
	}
	if data, err = os.ReadFile(keyPath); err != nil {
		return KeyPair{}, err
	}
	key, err := parseRSAKey(data)
	if err != nil {
		return KeyPair{}, fmt.Errorf("%s: %w", keyPath, err)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return KeyPair{}, fmt.Errorf("%s is not the key of %s", keyPath, certPath)
	}
	return KeyPair{Cert: cert, Key: key}, nil
}

// parseRSAKey decodes a PEM RSA private key, PKCS #8 or PKCS #1.
func parseRSAKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM private key")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("the private key is not RSA, which firmware verifies Secure Boot signatures with")
		}
		return key, nil
	}
	return nil, fmt.Errorf("a PEM block of the type %q, not a private key", block.Type)
}

// NewGUID returns a random GUID, of version 4, as it is stored.
func NewGUID() ([16]byte, error) {
	var g [16]byte
	if _, err := rand.Read(g[:]); err != nil {
		return g, err
	}
	// The version is the top nibble of the third field, which is stored little-endian.
	g[7] = g[7]&0x0f | 0x40
	g[8] = g[8]&0x3f | 0x80
	return g, nil
}

// ParseGUID reads a GUID as it is written, such as 8be4df61-93ca-11d2-aa0d-00e098032b8c, into the
// form it is stored in.
func ParseGUID(s string) ([16]byte, error) {
	parts := strings.Split(s, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		return [16]byte{}, fmt.Errorf("%q is not a GUID", s)
	}
	b, err := hex.DecodeString(strings.Join(parts, ""))
	if err != nil {
		return [16]byte{}, fmt.Errorf("%q is not a GUID", s)
	}
	var d [8]byte
	copy(d[:], b[8:])
	return guid(binary.BigEndian.Uint32(b[0:]), binary.BigEndian.Uint16(b[4:]), binary.BigEndian.Uint16(b[6:]), d), nil
}
