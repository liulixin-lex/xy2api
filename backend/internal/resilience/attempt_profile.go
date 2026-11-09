package resilience

import "time"

// AttemptProfile is the scheduling snapshot used by first-output consumers for
// one upstream attempt. AttemptStart is the actual send start; timer ownership
// can use an earlier, stable timestamp while the request is being built.
type AttemptProfile struct {
	AccountID    int64
	Model        string
	AttemptStart time.Time
	// TimerOwnerStart stays fixed when preparation ends and AttemptStart is
	// refreshed for send-time samples. Zero means AttemptStart owns the timer.
	TimerOwnerStart time.Time `json:"-"`
	Warm            bool
	Stream          bool
	BodyBytes       int
}

// SetAttemptProfile replaces the current attempt snapshot.
func (s *RequestState) SetAttemptProfile(profile AttemptProfile) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.attemptProfile = profile
	b := s.budget
	if BudgetTraceEnabledForTest {
		b.traceProfileForTest(profile)
	}
	s.mu.Unlock()
	b.MarkAttemptStarted()
	s.noteAttemptStart(profile.timerOwnerStart())
}

func (p AttemptProfile) timerOwnerStart() time.Time {
	if !p.TimerOwnerStart.IsZero() {
		return p.TimerOwnerStart
	}
	return p.AttemptStart
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
