package registry

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"

	"github.com/trevex/chalkos/pkg/pki"
)

// credentials are the node certificate the registry serves and the OS CAs it verifies clients
// by, read again when either file is replaced. chalkd replaces both by a rename, so a new file is
// another file to os.SameFile.
type credentials struct {
	certificate, ca string

	mu         sync.Mutex
	cert, pool os.FileInfo
	config     *tls.Config
}

// current is the TLS config of the files as they are now: the one last read while they are
// unchanged. A failed read keeps serving the last one, and fails only before there is any.
func (c *credentials) current() (*tls.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config != nil && unchanged(c.cert, c.certificate) && unchanged(c.pool, c.ca) {
		return c.config, nil
	}
	config, cert, pool, err := c.read()
	if err != nil {
		if c.config != nil {
			return c.config, nil
		}
		return nil, err
	}
	c.config, c.cert, c.pool = config, cert, pool
	return config, nil
}

// read reads both files: the chain and key from the one file chalkd keeps them in, and the OS
// CAs, against one of which a client certificate verifies when a client gives one. The files'
// identities are taken first, so a file replaced while it is read is read again next time.
func (c *credentials) read() (*tls.Config, os.FileInfo, os.FileInfo, error) {
	cert, err := os.Stat(c.certificate)
	if err != nil {
		return nil, nil, nil, err
	}
	pool, err := os.Stat(c.ca)
	if err != nil {
		return nil, nil, nil, err
	}
	pair, err := tls.LoadX509KeyPair(c.certificate, c.certificate)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("the node certificate: %w", err)
	}
	bundle, err := os.ReadFile(c.ca)
	if err != nil {
		return nil, nil, nil, err
	}
	cas, err := pki.BundlePool(string(bundle))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("the OS CAs: %w", err)
	}
	config := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{pair},
		ClientCAs:    cas,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}
	return config, cert, pool, nil
}

// unchanged reports whether path is still the file was, unchanged.
func unchanged(was os.FileInfo, path string) bool {
	now, err := os.Stat(path)
	return err == nil && os.SameFile(was, now) && was.ModTime().Equal(now.ModTime()) && was.Size() == now.Size()
}
