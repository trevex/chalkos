package pki

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
)

// SecretsVersion is the version of the secrets file this package writes. It reads version 1
// too, which lacks the Kubernetes secrets, so chalkctl secrets upgrade can add them and commands
// that do not need them keep working.
const SecretsVersion = 2

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
	// Kubernetes holds the secrets of the Kubernetes control plane; version 1 files lack them.
	Kubernetes *KubernetesSecrets `json:"kubernetes,omitempty"`
}

// Public is the public half of the secrets file, secrets.pub.json, which the cluster definition
// references to bake the OS CA into images.
type Public struct {
	Version    int               `json:"version"`
	OSCA       CertKey           `json:"osCA"`
	Kubernetes *KubernetesPublic `json:"kubernetes,omitempty"`
}

// GenerateSecrets creates the OS CA, an admin client certificate, the recovery secret and the
// Kubernetes secrets.
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
	k, err := NewKubernetesSecrets(now)
	if err != nil {
		return Secrets{}, err
	}
	return Secrets{Version: SecretsVersion, OSCA: ca, Admin: admin, RecoverySecret: secret, Kubernetes: k}, nil
}

// String returns a redacted summary, so logging or an error wrapping a Secrets never leaks the
// recovery secret, a private key or the encryption key.
func (s Secrets) String() string {
	subject := "invalid"
	if ca, _, err := s.OSCA.Parse(); err == nil {
		subject = ca.Subject.CommonName
	}
	return fmt.Sprintf("pki.Secrets{version: %d, osCA: %s, redacted}", s.Version, subject)
}

// GoString redacts a Secrets the same way String does, so %#v in a log or test failure never
// prints a private key or the recovery secret either.
func (s Secrets) GoString() string {
	return s.String()
}

// Public returns the parts of the secrets that may be published.
func (s Secrets) Public() Public {
	p := Public{Version: s.Version, OSCA: CertKey{Certificate: s.OSCA.Certificate}}
	if s.Kubernetes != nil {
		p.Kubernetes = s.Kubernetes.Public()
	}
	return p
}

// Validate checks that every secret is present, that the certificates belong to their keys, that
// osCA is a CA certificate, that admin verifies against it and grants the admin role, and that a
// version 2 file has valid Kubernetes secrets whose CAs differ from each other and from osCA.
func (s Secrets) Validate() error {
	switch {
	case s.Version != 1 && s.Version != SecretsVersion:
		return fmt.Errorf("secrets file version %d is not supported (want 1 or %d)", s.Version, SecretsVersion)
	case s.Version == 1 && s.Kubernetes != nil:
		return errors.New("a version 1 secrets file has no kubernetes section")
	case s.Version == SecretsVersion && s.Kubernetes == nil:
		return errors.New("kubernetes: missing")
	}
	ca, _, err := s.OSCA.Parse()
	if err != nil {
		return fmt.Errorf("osCA: %w", err)
	}
	if !ca.IsCA || !ca.BasicConstraintsValid {
		return errors.New("osCA: not a CA certificate")
	}
	admin, _, err := s.Admin.Parse()
	if err != nil {
		return fmt.Errorf("admin: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := admin.Verify(x509.VerifyOptions{
		Roots:       roots,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		CurrentTime: admin.NotBefore,
	}); err != nil {
		return fmt.Errorf("admin: does not verify against osCA: %w", err)
	}
	if role, ok := Role(admin); !ok || role != RoleAdmin {
		return errors.New("admin: certificate does not grant the admin role")
	}
	if len(s.RecoverySecret) != RecoverySecretSize {
		return fmt.Errorf("recoverySecret must be %d bytes", RecoverySecretSize)
	}
	if s.Kubernetes != nil {
		if err := s.Kubernetes.Validate(); err != nil {
			return err
		}
		if err := RequireDistinctCAs(append([]NamedCA{{"osCA", s.OSCA}}, s.Kubernetes.cas()...)...); err != nil {
			return err
		}
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
			// DetectFormat trims whitespace to find the armor header, so the reader must see the
			// same trimmed input or it rejects a leading blank line that DetectFormat ignored.
			r = armor.NewReader(bytes.NewReader(bytes.TrimSpace(data)))
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
