package registry

// HoldCollection holds the registry as a running collection does, until release.
func (s *Server) HoldCollection() (release func()) {
	s.collecting.Store(true)
	s.writes.Lock()
	return func() {
		s.writes.Unlock()
		s.collecting.Store(false)
	}
}
