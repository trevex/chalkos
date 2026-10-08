package client

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

// ConfigVersion is the version of the client file this package reads and writes.
const ConfigVersion = 1

// Config is a client file: a user's certificate from the OS CA, with what is needed to reach
// and verify the cluster's nodes, for operating the cluster without the secrets file.
type Config struct {
	Version int    `json:"version"`
	Cluster string `json:"cluster"`
	// Name is the user's name, the certificate's Common Name, and Role the role its Organization
	// grants.
	Name string `json:"name"`
	Role string `json:"role"`
	// Certificate and Key are PEM.
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
	// OSCA holds the PEM certificates of the OS CAs nodes' certificates chain to: the one that
	// issued the certificate, and while it rotates the other one.
	OSCA string `json:"osCA"`
	// Nodes are the nodes' addresses by name, as the cluster definition had them when the file
	// was issued; used only when no cluster definition is at hand.
	Nodes map[string]string `json:"nodes"`
}

// NewConfig issues a client certificate of the role from the OS CA for a new key, valid for
// validity and never beyond the OS CA, for a cluster whose nodes chain to the OS CAs trusted.
func NewConfig(osCA pki.CertKey, trusted, cluster, name, role string, validity time.Duration, nodes map[string]string, now time.Time) (Config, error) {
	ck, err := pki.IssueClient(osCA, name, role, validity, now)
	if err != nil {
		return Config{}, err
	}
	c := Config{Version: ConfigVersion, Cluster: cluster, Name: name, Role: role, Certificate: ck.Certificate, Key: ck.Key, OSCA: pki.Bundle(osCA.Certificate, trusted), Nodes: nodes}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// ReadConfig reads and checks a client file. Its dates are not checked: the caller says when
// the certificate expires.
func ReadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Config{}, fmt.Errorf("parse %s: data after the client file", path)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Validate checks the version, that the certificate belongs to the key, that an OS CA of the file
// issued it itself for clients, and that it grants the role and names the user the file says.
func (c Config) Validate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("client file version %d is not supported (want %d)", c.Version, ConfigVersion)
	}
	if c.Cluster == "" || c.Name == "" {
		return errors.New("a client file names the cluster and the user")
	}
	cert, _, err := (pki.CertKey{Certificate: c.Certificate, Key: c.Key}).Parse()
	if err != nil {
		return fmt.Errorf("the certificate: %w", err)
	}
	if _, err := pki.ValidateOSCABundle(c.OSCA); err != nil {
		return fmt.Errorf("the OS CA: %w", err)
	}
	roots, err := pki.BundlePool(c.OSCA)
	if err != nil {
		return fmt.Errorf("the OS CA: %w", err)
	}
	chains, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: cert.NotBefore})
	if err != nil {
		return fmt.Errorf("the certificate does not verify against the OS CA: %w", err)
	}
	if role, ok := pki.ClientRole(chains); !ok || role != c.Role || role == pki.RoleNode {
		return fmt.Errorf("the certificate grants the role %q, not %q", role, c.Role)
	}
	if cert.Subject.CommonName != c.Name {
		return fmt.Errorf("the certificate is for %q, not %q", cert.Subject.CommonName, c.Name)
	}
	return nil
}

// Leaf returns the client certificate.
func (c Config) Leaf() (*x509.Certificate, error) {
	return pki.ParseCertificate([]byte(c.Certificate))
}

// TLSCertificate returns the certificate with its key, as a client presents it.
func (c Config) TLSCertificate() (*tls.Certificate, error) {
	cert, err := tls.X509KeyPair([]byte(c.Certificate), []byte(c.Key))
	if err != nil {
		return nil, errors.New("the client file's certificate does not match its key")
	}
	return &cert, nil
}

// Encode returns the client file.
func (c Config) Encode() ([]byte, error) {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// String names the user and never prints the key.
func (c Config) String() string {
	return fmt.Sprintf("client.Config{cluster: %s, name: %s, role: %s, key: redacted}", c.Cluster, c.Name, c.Role)
}

// GoString redacts the file like String.
func (c Config) GoString() string {
	return c.String()
}
