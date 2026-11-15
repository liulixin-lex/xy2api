package resilience

type schedSelectionFlags struct {
	accountID int64
	fallback  bool
	takeover  bool
}

// SetSchedSelectionFlags replaces the facts for the selected attempt. A retry
// on another account must not inherit the previous target's flags.
func (s *RequestState) SetSchedSelectionFlags(accountID int64, fallback, takeover bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.selectionFlags = schedSelectionFlags{accountID, fallback, takeover}
	s.mu.Unlock()
}

func (s *RequestState) SchedSelectionFlags(accountID int64) (fallback, takeover bool) {
	if s == nil {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selectionFlags.accountID != accountID {
		return false, false
	}
	return s.selectionFlags.fallback, s.selectionFlags.takeover
}
