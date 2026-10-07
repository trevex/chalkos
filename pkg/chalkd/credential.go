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
)

// credentialValidity is how long chalkd's client certificate for the API server lives. It is
// in system:masters, so it is short-lived and exists only in memory.
const credentialValidity = time.Hour

// credential issues chalkd's client certificate from the share's CA when a connection needs
// one, and again once half its lifetime has passed.
type credential struct {
	share    kpki.Share
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

func newCredential(share kpki.Share, validity time.Duration, now func() time.Time) *credential {
	return &credential{share: share, validity: validity, now: now, conns: connrotation.NewConnectionTracker()}
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
	ck, err := kpki.IssueChalkd(c.share, c.validity, now)
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

// restConfig reaches the local API server with the credential.
func (c *credential) restConfig() (*rest.Config, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(c.share.CA.Certificate)) {
		return nil, errors.New("the share's CA holds no certificate")
	}
	dialer := connrotation.NewDialerWithTracker((&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, c.conns)
	return &rest.Config{
		Host: kpki.LocalAPIServer,
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
			TLSClientConfig: &tls.Config{
				RootCAs:              roots,
				MinVersion:           tls.VersionTLS12,
				GetClientCertificate: c.GetClientCertificate,
			},
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 30 * time.Second,
	}, nil
}
