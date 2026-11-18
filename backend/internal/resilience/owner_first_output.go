package resilience

import "time"

// OwnerFirstOutputCutoff identifies the physical HTTP send on which the
// first-output guard won. BindingKey and Generation come from selection.
type OwnerFirstOutputCutoff struct {
	GroupID, AccountID                     int64
	Session, BindingKey, Generation, Model string
	AttemptSeq                             uint64
	DispatchAt, FiredAt                    time.Time
	Effective                              time.Duration
	CanTransfer                            bool
}

// OwnerHTTPDispatchPending allows an owner send to retain its own guard handle
// even when neither the request log nor the scheduler recorder is installed.
func (s *RequestState) OwnerHTTPDispatchPending(accountID int64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.httpSendSeq == 0 && s.sessionHash != "" && s.schedSelection.HTTPStreamText &&
		s.schedSelection.AccountID == accountID && s.schedSelection.StickyWarm
}

// IsCurrentHTTPDispatch checks both the handle and the physical send. A
// transport can make a second send without replacing the forwarding handle.
func (s *RequestState) IsCurrentHTTPDispatch(handle any, seq uint64) bool {
	if s == nil || seq == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentAttempt == handle && s.httpSendSeq == seq
}

func (s *RequestState) RecordOwnerCutoff(v OwnerFirstOutputCutoff) bool {
	if s == nil || v.AttemptSeq == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ownerCutoff.AttemptSeq != 0 || s.httpSendSeq != v.AttemptSeq {
		return false
	}
	s.ownerCutoff = v
	return true
}

func (s *RequestState) OwnerCutoff() (OwnerFirstOutputCutoff, bool) {
	if s == nil {
		return OwnerFirstOutputCutoff{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownerCutoff, s.ownerCutoff.AttemptSeq != 0
}

// ConfirmOwnerAvoid accepts only a successful write for the frozen binding.
func (s *RequestState) ConfirmOwnerAvoid(attemptSeq uint64, generation string) bool {
	if s == nil || attemptSeq == 0 || generation == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.ownerCutoff
	if !v.CanTransfer || v.AttemptSeq != attemptSeq || v.Generation != generation {
		return false
	}
	s.ownerAvoidConfirmed = true
	return true
}

func (s *RequestState) ConfirmedOwnerCutoff() (OwnerFirstOutputCutoff, bool) {
	if s == nil {
		return OwnerFirstOutputCutoff{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ownerAvoidConfirmed {
		return OwnerFirstOutputCutoff{}, false
	}
	return s.ownerCutoff, true
}
