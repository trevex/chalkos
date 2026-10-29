package chalkd

import (
	"context"
	"crypto/x509"
	"time"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/pki"
)

// trustFor returns the OS CAs the node is to trust after the request: those delivered, checked
// as NodeCertificate.ReplaceTrust does, or else its own.
func (s *Server) trustFor(ctx context.Context, delivered []byte) (string, error) {
	if s.Certificate == nil {
		if len(delivered) > 0 {
			return "", failed(connect.CodeFailedPrecondition, "the node serves no node certificate the OS CAs would verify")
		}
		return string(s.readOSCA()), nil
	}
	if len(delivered) == 0 {
		return s.Certificate.OSCA(), nil
	}
	if err := s.Certificate.CheckTrust(string(delivered), peerOf(ctx)...); err != nil {
		return "", failed(connect.CodeInvalidArgument, "%v", err)
	}
	return pki.Bundle(string(delivered)), nil
}

// peerOf returns the verified client certificate of the call, if there is one.
func peerOf(ctx context.Context) []*x509.Certificate {
	if peer := layer.Peer(ctx); peer != nil {
		return []*x509.Certificate{peer}
	}
	return nil
}

// checkNodeCertificate refuses a delivered node certificate before anything changes, as
// NodeCertificate.Replace would; none delivered passes.
func (s *Server) checkNodeCertificate(chain, key []byte) error {
	if len(chain) == 0 && len(key) == 0 {
		return nil
	}
	if s.Certificate == nil {
		return failed(connect.CodeFailedPrecondition, "the node serves no node certificate to replace")
	}
	if err := s.Certificate.Check(string(chain), string(key), time.Now()); err != nil {
		return failed(connect.CodeInvalidArgument, "%v", err)
	}
	return nil
}
