package pki

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
)

// SecretsVersion is the version of the secrets file this package writes and reads.
const SecretsVersion = 1

// Secrets is the cluster's secrets file. It is written once by chalkctl gen secrets and only read
// afterwards, so it can live in any secret manager.
type Secrets struct {
	Version int `json:"version"`
	// OSCA issues node certificates and client certificates for chalkd.
	OSCA CertKey `json:"osCA"`
	// Admin is a client certificate with the admin role.
	Admin CertKey `json:"admin"`
	// RecoverySecret is what each node's recovery key is derived from.
	RecoverySecret []byte `json:"recoverySecret"`
}

// Public is the public half of the secrets file, secrets.pub.json, which the cluster definition
// references to bake the OS CA into images.
type Public struct {
	Version int     `json:"version"`
	OSCA    CertKey `json:"osCA"`
}

// GenerateSecrets creates the OS CA, an admin client certificate and the recovery secret.
func GenerateSecrets(now time.Time) (Secrets, error) {
	ca, err := NewCA("chalkos OS CA", now)
	if err != nil {
		return Secrets{}, err
	}
	admin, err := IssueClient(ca, "admin", RoleAdmin, now)
	if err != nil {
		return Secrets{}, err
	}
	secret := make([]byte, RecoverySecretSize)
	if _, err := rand.Read(secret); err != nil {
		return Secrets{}, err
	}
	return Secrets{Version: SecretsVersion, OSCA: ca, Admin: admin, RecoverySecret: secret}, nil
}

// Public returns the parts of the secrets that may be published.
func (s Secrets) Public() Public {
	return Public{Version: s.Version, OSCA: CertKey{Certificate: s.OSCA.Certificate}}
}

// Validate checks that every secret is present and that the certificates belong to their keys.
func (s Secrets) Validate() error {
	if s.Version != SecretsVersion {
		return fmt.Errorf("secrets file version %d is not supported (want %d)", s.Version, SecretsVersion)
	}
	if _, _, err := s.OSCA.Parse(); err != nil {
		return fmt.Errorf("osCA: %w", err)
	}
	if _, _, err := s.Admin.Parse(); err != nil {
		return fmt.Errorf("admin: %w", err)
	}
	if len(s.RecoverySecret) != RecoverySecretSize {
		return fmt.Errorf("recoverySecret must be %d bytes", RecoverySecretSize)
	}
	return nil
}

// Encode returns the plaintext secrets file.
func (s Secrets) Encode() ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Encrypt encodes the secrets and encrypts them to the recipients.
func (s Secrets) Encrypt(recipients ...age.Recipient) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, errors.New("no recipients")
	}
	plain, err := s.Encode()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	w, err := age.Encrypt(&out, recipients...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Format is how a secrets file is stored.
type Format int

const (
	FormatJSON Format = iota
	FormatAge
	FormatArmoredAge
)

// DetectFormat tells an age file, binary or armored, from plaintext JSON by its first bytes.
func DetectFormat(data []byte) (Format, error) {
	trimmed := bytes.TrimSpace(data)
	switch {
	case bytes.HasPrefix(data, []byte("age-encryption.org/")):
		return FormatAge, nil
	case bytes.HasPrefix(trimmed, []byte(armor.Header)):
		return FormatArmoredAge, nil
	case bytes.HasPrefix(trimmed, []byte("{")):
		return FormatJSON, nil
	}
	return 0, errors.New("the secrets file is neither age-encrypted nor JSON")
}

// ReadSecrets decodes a secrets file in any format. identities is called only for age files,
// so reading plaintext never asks for a key.
func ReadSecrets(data []byte, identities func() ([]age.Identity, error)) (Secrets, error) {
	format, err := DetectFormat(data)
	if err != nil {
		return Secrets{}, err
	}
	plain := data
	if format != FormatJSON {
		ids, err := identities()
		if err != nil {
			return Secrets{}, err
		}
		var r io.Reader = bytes.NewReader(data)
		if format == FormatArmoredAge {
			r = armor.NewReader(r)
		}
		dec, err := age.Decrypt(r, ids...)
		if err != nil {
			return Secrets{}, fmt.Errorf("decrypt the secrets file: %w", err)
		}
		if plain, err = io.ReadAll(dec); err != nil {
			return Secrets{}, fmt.Errorf("decrypt the secrets file: %w", err)
		}
	}
	var s Secrets
	if err := json.Unmarshal(plain, &s); err != nil {
		return Secrets{}, fmt.Errorf("parse the secrets file: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Secrets{}, fmt.Errorf("secrets file: %w", err)
	}
	return s, nil
}
