package resilience

import "time"

// AttemptProfile is the immutable scheduling snapshot used by all first-output
// guards for one upstream attempt.
type AttemptProfile struct {
	AccountID    int64
	Model        string
	AttemptStart time.Time
	Warm         bool
	Stream       bool
	BodyBytes    int
}

// SetAttemptProfile replaces the current attempt snapshot.
func (s *RequestState) SetAttemptProfile(profile AttemptProfile) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.attemptProfile = profile
	s.mu.Unlock()
}

// AttemptProfile returns the current attempt snapshot.
func (s *RequestState) AttemptProfile() AttemptProfile {
	if s == nil {
		return AttemptProfile{}
	}
	s.mu.Lock()
	profile := s.attemptProfile
	s.mu.Unlock()
	return profile
}

// FirstOutput returns the recorded first content delivery time.
func (s *RequestState) FirstOutput() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	at := s.firstOutput
	s.mu.Unlock()
	return at
}
