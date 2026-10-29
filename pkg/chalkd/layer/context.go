package layer

import (
	"context"
	"crypto/x509"
)

// peerKey holds the verified client certificate of a call.
type peerKey struct{}

// WithPeer is ctx with the call's verified client certificate.
func WithPeer(ctx context.Context, cert *x509.Certificate) context.Context {
	return context.WithValue(ctx, peerKey{}, cert)
}

// Peer is the call's verified client certificate; nil without one.
func Peer(ctx context.Context) *x509.Certificate {
	cert, _ := ctx.Value(peerKey{}).(*x509.Certificate)
	return cert
}
