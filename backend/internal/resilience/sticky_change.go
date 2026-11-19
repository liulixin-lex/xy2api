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

// VoluntarySwitchVetoLogged reports whether this request has already logged a veto.
func (s *RequestState) VoluntarySwitchVetoLogged() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.voluntarySwitchVetoLogged
}

// ClaimVoluntarySwitchVeto limits selection-time veto evidence across scheduler retries.
func (s *RequestState) ClaimVoluntarySwitchVeto() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.voluntarySwitchVetoLogged {
		return false
	}
	s.voluntarySwitchVetoLogged = true
	return true
}
