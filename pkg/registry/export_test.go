package registry

import "crypto/x509"

// HoldCollection holds the registry as a running collection does, until release.
func (s *Server) HoldCollection() (release func()) {
	s.collecting.Store(true)
	s.writes.Lock()
	return func() {
		s.writes.Unlock()
		s.collecting.Store(false)
	}
}

// SetRoots replaces the OS CAs writes are verified against, as if the registry had restarted
// with them while a client kept its connection.
func (s *Server) SetRoots(roots *x509.CertPool) { s.roots.Store(roots) }
