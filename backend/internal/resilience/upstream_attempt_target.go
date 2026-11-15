package resilience

// UpstreamAttemptTarget identifies the actual outgoing destination, without secrets.
type UpstreamAttemptTarget struct {
	AccountID        int64
	Protocol, Origin string
}

func (s *RequestState) SetUpstreamAttemptTarget(target *UpstreamAttemptTarget) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstreamAttemptTarget = nil
	if target != nil {
		v := *target
		s.upstreamAttemptTarget = &v
	}
}
func (s *RequestState) UpstreamAttemptTarget() *UpstreamAttemptTarget {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upstreamAttemptTarget == nil {
		return nil
	}
	v := *s.upstreamAttemptTarget
	return &v
}
