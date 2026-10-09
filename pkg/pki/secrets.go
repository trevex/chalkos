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

// SecretsVersion is the only version of the secrets file this package reads and writes.
const SecretsVersion = 3

// Secrets is the cluster's secrets file. chalkctl gen secrets writes it; chalkctl node-ca rotate
// and chalkctl rotate change it.
type Secrets struct {
	Version int `json:"version"`
	// OSCA is the root every node and client of chalkd trusts. It issues client certificates and
	// the node CA, and never leaves the secrets file with its key.
	OSCA CertKey `json:"osCA"`
	// NodeCA issues node certificates; control-plane nodes hold it to renew them.
	NodeCA CertKey `json:"nodeCA"`
	// RecoverySecret is what each node's recovery key is derived from.
	RecoverySecret []byte `json:"recoverySecret"`
	// Kubernetes holds the secrets of the Kubernetes control plane.
	Kubernetes KubernetesSecrets `json:"kubernetes"`
	// Accepted holds the OS CAs still trusted besides OSCA while it rotates.
	Accepted Accepted `json:"accepted,omitzero"`
	// Rotation records the rotation that runs, if one does.
	Rotation *Rotation `json:"rotation,omitempty"`
	// Recipients are the age recipients an encrypted secrets file is encrypted to. They are kept
	// inside it, where nobody without its key can change them, so a command that changes the
	// file encrypts it to them again.
	Recipients []string `json:"recipients,omitempty"`
}

// Public is the public half of the secrets file, secrets.pub.json, which the cluster definition
// references to bake the OS CA into images.
type Public struct {
	Version    int              `json:"version"`
	OSCA       CertKey          `json:"osCA"`
	NodeCA     CertKey          `json:"nodeCA"`
	Kubernetes KubernetesPublic `json:"kubernetes"`
}

// ReadPublic decodes secrets.pub.json.
func ReadPublic(data []byte) (Public, error) {
	var p Public
	if err := json.Unmarshal(data, &p); err != nil {
		return Public{}, fmt.Errorf("parse the public secrets file: %w", err)
	}
	if p.Version != SecretsVersion {
		return Public{}, fmt.Errorf("the public secrets file is version %d; chalkos reads version %d only", p.Version, SecretsVersion)
	}
	if _, err := ParseCertificate([]byte(p.OSCA.Certificate)); err != nil {
		return Public{}, fmt.Errorf("the public secrets file's OS CA: %w", err)
	}
	return p, nil
}

// GenerateSecrets creates the OS CA, the node CA, the recovery secret and the Kubernetes
// secrets.
func GenerateSecrets(now time.Time) (Secrets, error) {
	ca, err := NewOSCA(now)
	if err != nil {
		return Secrets{}, err
	}
	nodeCA, err := NewNodeCA(ca, now)
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
	return Secrets{Version: SecretsVersion, OSCA: ca, NodeCA: nodeCA, RecoverySecret: secret, Kubernetes: *k}, nil
}

// String returns a redacted summary, so logging or an error wrapping a Secrets never leaks the
// recovery secret, a private key or the encryption key.
func (s Secrets) String() string {
	subject := "invalid"
	if ca, _, err := s.OSCA.Parse(); err == nil {
		subject = ca.Subject.CommonName
	}
	rotation := ""
	if s.Rotation != nil {
		rotation = ", rotation: " + s.Rotation.String()
	}
	return fmt.Sprintf("pki.Secrets{version: %d, osCA: %s%s, redacted}", s.Version, subject, rotation)
}

// GoString redacts a Secrets the same way String does, so %#v in a log or test failure never
// prints a private key or the recovery secret either.
func (s Secrets) GoString() string {
	return s.String()
}

// Public returns the parts of the secrets that may be published.
func (s Secrets) Public() Public {
	return Public{
		Version:    s.Version,
		OSCA:       CertKey{Certificate: s.OSCA.Certificate},
		NodeCA:     CertKey{Certificate: s.NodeCA.Certificate},
		Kubernetes: *s.Kubernetes.Public(),
	}
}

// Validate checks the version, that every secret is present, that the certificates belong to
// their keys, that osCA is a CA that may issue the node CA, that nodeCA is a node CA osCA
// issued, that the Kubernetes secrets and the accepted values are valid, that no two CAs share a
// key, and that a running rotation is consistent.
func (s Secrets) Validate() error {
	if s.Version != SecretsVersion {
		return fmt.Errorf("version %d is not supported; chalkos reads version %d only, so generate new secrets with chalkctl gen secrets", s.Version, SecretsVersion)
	}
	if err := ValidateCA(s.OSCA); err != nil {
		return fmt.Errorf("osCA: %w", err)
	}
	if err := ValidateNodeCA(s.NodeCA, s.OSCA.Certificate); err != nil {
		return fmt.Errorf("nodeCA: %w", err)
	}
	if len(s.RecoverySecret) != RecoverySecretSize {
		return fmt.Errorf("recoverySecret must be %d bytes", RecoverySecretSize)
	}
	if err := s.Kubernetes.Validate(); err != nil {
		return err
	}
	if err := s.validateAccepted(); err != nil {
		return err
	}
	if err := RequireDistinctCAs(s.cas()...); err != nil {
		return err
	}
	return s.validateRotation()
}

// ValidateNodeCA checks that nodeCA, with its key, is a node CA that an OS CA of the bundle osCA
// issued, and that this OS CA may issue it. Dates are not checked: an expired node CA is
// replaced, not refused.
func ValidateNodeCA(nodeCA CertKey, osCA string) error {
	if err := ValidateCA(nodeCA); err != nil {
		return err
	}
	return verifyNodeCA(nodeCA.Certificate, osCA)
}

// verifyNodeCA checks the certificate of a node CA against the bundle of OS CAs.
func verifyNodeCA(nodeCA, osCA string) error {
	cert, err := ParseCertificate([]byte(nodeCA))
	if err != nil {
		return err
	}
	roots, err := ParseBundle(osCA)
	if err != nil {
		return fmt.Errorf("the OS CA: %w", err)
	}
	if !IsNodeCA(cert) {
		return errors.New("not a node CA: it must be a CA for TLS servers and clients that issues no CA")
	}
	pool := x509.NewCertPool()
	issuers := 0
	for _, root := range roots {
		// A root that issues leaves only would refuse every node certificate's chain.
		if !root.MaxPathLenZero {
			pool.AddCert(root)
			issuers++
		}
	}
	if issuers == 0 {
		return errors.New("the OS CA issues no intermediate CA, so it cannot have a node CA")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: nodeCAUsages, CurrentTime: cert.NotBefore}); err != nil {
		return fmt.Errorf("does not verify against the OS CA: %w", err)
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

// EncodeAs encodes the secrets in the format given: plaintext, or encrypted to the recipients,
// armored or not.
func (s Secrets) EncodeAs(format Format, recipients ...age.Recipient) ([]byte, error) {
	switch format {
	case FormatJSON:
		return s.Encode()
	case FormatAge:
		return s.Encrypt(recipients...)
	case FormatArmoredAge:
		encrypted, err := s.Encrypt(recipients...)
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		w := armor.NewWriter(&out)
		if _, err := w.Write(encrypted); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return append(out.Bytes(), '\n'), nil
	}
	return nil, fmt.Errorf("unknown format %d", format)
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
