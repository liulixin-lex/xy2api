package resilience

// StickyChange is opaque to resilience; the scheduler owns the pending CAS.
func (s *RequestState) StickyChange() any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stickyChange
}

func (s *RequestState) SetStickyChange(change any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stickyChange = change
	s.mu.Unlock()
}
