package resilience

// SetLastUpstreamFailure retains the handler's existing failure so a rejected
// dispatch can end with the same error, without inventing a transport failure.
// The error remains owned by the request; no payload is persisted here.
func (s *RequestState) SetLastUpstreamFailure(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastUpstreamFailure = err
	s.mu.Unlock()
}

func (s *RequestState) LastUpstreamFailure() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUpstreamFailure
}
