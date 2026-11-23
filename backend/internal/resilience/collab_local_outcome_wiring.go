package resilience

// NoteCollabLocalOutcome changes only the inbound result. It never completes
// an upstream attempt, clears account faults, or claims a billing owner.
func (s *RequestState) NoteCollabLocalOutcome(delivered bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.collab == nil || s.gatewayRecord != nil && s.gatewayRecord.final != nil {
		return
	}
	s.collab.LocalOutcome = RequestLocalUndeliverable
	if delivered {
		s.collab.LocalOutcome = RequestLocalEnded
	}
}
