package chalkd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/connrotation"

	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// credentialValidity is how long chalkd's client certificates for the API server and etcd live.
// They allow everything, so they are short-lived and exist only in memory.
const credentialValidity = time.Hour

// credential issues one of chalkd's client certificates from a CA of the share when a connection
// needs one, and again once half its lifetime has passed.
type credential struct {
	// issue issues the certificate, valid for validity; ca holds the PEM certificates of the CAs
	// the server's certificate may chain to, the one that issues the credential among them.
	issue    func(validity time.Duration, now time.Time) (pki.CertKey, error)
	ca       string
	validity time.Duration
	now      func() time.Time
	// conns are the connections to the API server, closed when the certificate is replaced:
	// a connection keeps the certificate it was made with.
	conns *connrotation.ConnectionTracker

	mu      sync.Mutex
	cert    *tls.Certificate
	renewAt time.Time
	// issued counts the certificates issued; closed is the count when the connections were
	// last closed.
	issued, closed int
}

// newCredential issues chalkd's client certificate for the API server, in system:masters.
func newCredential(share kpki.Share, validity time.Duration, now func() time.Time) *credential {
	issue := func(validity time.Duration, now time.Time) (pki.CertKey, error) {
		return kpki.IssueChalkd(share, validity, now)
	}
	return &credential{issue: issue, ca: share.CABundle(), validity: validity, now: now, conns: connrotation.NewConnectionTracker()}
}

// newEtcdCredential issues chalkd's client certificate for etcd.
func newEtcdCredential(share kpki.Share, validity time.Duration, now func() time.Time) (*credential, error) {
	if share.EtcdCA == nil {
		return nil, errors.New("the node's share holds no etcd CA")
	}
	issue := func(validity time.Duration, now time.Time) (pki.CertKey, error) {
		return kpki.IssueEtcdClient(share, validity, now)
	}
	return &credential{issue: issue, ca: share.EtcdCABundle(), validity: validity, now: now, conns: connrotation.NewConnectionTracker()}, nil
}

// GetClientCertificate returns the current certificate, issuing one when there is none or
// half its lifetime has passed.
func (c *credential) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.cert != nil && now.Before(c.renewAt) {
		return c.cert, nil
	}
	ck, err := c.issue(c.validity, now)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair([]byte(ck.Certificate), []byte(ck.Key))
	if err != nil {
		return nil, err
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, err
		}
	}
	c.cert = &cert
	c.renewAt = now.Add(cert.Leaf.NotAfter.Sub(now) / 2)
	c.issued++
	return c.cert, nil
}

// rotated reports, once per replacement, that the certificate was replaced since the
// connections were last closed.
func (c *credential) rotated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.issued <= 1 || c.issued == c.closed {
		return false
	}
	c.closed = c.issued
	return true
}

// renew replaces the certificate at its half-life until ctx ends and closes the connections
// made before, so no connection outlives the certificate it authenticated with.
func (c *credential) renew(ctx context.Context) {
	for {
		c.mu.Lock()
		wait := c.renewAt.Sub(c.now())
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if _, err := c.GetClientCertificate(nil); err != nil {
			log.Printf("kubernetes: issue chalkd's client certificate: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
			continue
		}
		if c.rotated() {
			c.conns.CloseAll()
		}
	}
}

// tlsConfig trusts the CA and authenticates with the credential.
func (c *credential) tlsConfig() (*tls.Config, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(c.ca)) {
		return nil, errors.New("the share's CA holds no certificate")
	}
	return &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, GetClientCertificate: c.GetClientCertificate}, nil
}

// restConfig reaches the API server at server, such as the local one, with the credential.
func (c *credential) restConfig(server string) (*rest.Config, error) {
	tlsConfig, err := c.tlsConfig()
	if err != nil {
		return nil, err
	}
	dialer := connrotation.NewDialerWithTracker((&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, c.conns)
	return &rest.Config{
		Host: server,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSClientConfig:     tlsConfig,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 30 * time.Second,
	}, nil
}
