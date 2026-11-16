package schedquality

import "time"

// Due chooses one event; repeated timestamps never advance a line twice.
// A first admission at level zero still needs the 24-hour history-clear clock.
func Due(s LineState, at time.Time, fiveMin, touched bool) (EventKind, bool) {
	if !at.After(s.EvaluatedAt) {
		return EventRegular, false
	}
	if s.Identity == IdentityRear {
		return EventReview, !at.Before(s.Until)
	}
	if s.Trial {
		return EventTrial, touched || !at.Before(s.TrialUntil)
	}
	if fiveMin {
		return EventRegular, true
	}
	return EventClock, quietClearDue(s, at)
}

func (s LineState) Cut(kind EventKind) time.Time {
	if kind == EventReview {
		return s.Entry
	}
	return s.Epoch
}

// Step applies evidence before deadlines. An empty action does not mean the
// state is unchanged: front Pass/Unknown and pending failures only update state.
// The caller persists changed states even when there is no decision to record.
func Step(prev LineState, kind EventKind, v Verdict, basis Basis, at time.Time) (LineState, Action) {
	if !at.After(prev.EvaluatedAt) {
		return prev, ""
	}
	s := prev
	var action Action
	switch kind {
	case EventRegular:
		if s.Identity == IdentityRear || s.Trial {
			return prev, ""
		}
		switch v {
		case VerdictFail:
			s.GoodSince = time.Time{}
			if s.Line == DimensionTTFO || (!s.PendingAt.IsZero() && s.PendingAt.Equal(at.Add(-RegularInterval))) {
				enterRear(&s, at, s.Admissions > 0)
				s.Admissions++
				action = ActionEnter
			} else {
				s.Identity = IdentityUnknown
				s.PendingAt = at
			}
		case VerdictPass:
			s.Identity = IdentityPass
			s.PendingAt = time.Time{}
			if s.GoodSince.IsZero() {
				s.GoodSince = at
			}
			if (s.Level > 0 || s.Admissions > 0) && !at.Before(s.GoodSince.Add(LevelClearGood)) {
				s.Level, s.Admissions = 0, 0
				action = ActionClear
			}
		default:
			s.Identity = IdentityUnknown
			s.GoodSince, s.PendingAt = time.Time{}, time.Time{}
		}
	case EventReview:
		if s.Identity != IdentityRear || at.Before(s.Until) {
			return prev, ""
		}
		switch v {
		case VerdictFail:
			enterRear(&s, at, true)
			action = ActionRenew
		case VerdictPass:
			s.Identity, s.Trial = IdentityPass, false
			s.Until, s.TrialUntil, s.PendingAt = time.Time{}, time.Time{}, time.Time{}
			s.GoodSince = at
			action = ActionRelease
		default:
			s.Identity, s.Trial = IdentityUnknown, true
			s.Epoch, s.TrialUntil = at, at.Add(TrialDuration)
			s.Until, s.GoodSince, s.PendingAt = time.Time{}, time.Time{}, time.Time{}
			action = ActionTrial
		}
	case EventTrial:
		if !s.Trial || s.Identity == IdentityRear {
			return prev, ""
		}
		switch v {
		case VerdictFail:
			enterRear(&s, at, true)
			s.Admissions++
			action = ActionTrialFail
		case VerdictPass:
			s.Identity, s.Trial = IdentityPass, false
			s.TrialUntil, s.PendingAt = time.Time{}, time.Time{}
			s.GoodSince = at
			action = ActionTrialPass
		default:
			if !at.Before(s.TrialUntil) {
				s.Identity, s.Trial = IdentityUnknown, false
				s.TrialUntil, s.GoodSince = time.Time{}, time.Time{}
				action = ActionTrialTimeout
			}
		}
	case EventClock:
		if !quietClearDue(s, at) {
			return prev, ""
		}
	default:
		return prev, ""
	}
	// New bad evidence has already refreshed LastRear, so it wins over a quiet
	// deadline at the same timestamp. Clearing never changes identity or epoch.
	if quietClearDue(s, at) {
		s.Level, s.Admissions = 0, 0
		if action == "" {
			action = ActionClear
		}
	}
	s.EvaluatedAt = at
	if action != "" || s.Identity != prev.Identity || s.Trial != prev.Trial {
		s.Basis = &basis
	}
	return s, action
}

func enterRear(s *LineState, at time.Time, escalate bool) {
	if escalate {
		s.Level = min(s.Level+1, len(CooldownLadder)-1)
	}
	s.Identity, s.Trial = IdentityRear, false
	s.Entry, s.LastRear = at, at
	s.Until = at.Add(CooldownLadder[s.Level])
	s.TrialUntil, s.GoodSince, s.PendingAt = time.Time{}, time.Time{}, time.Time{}
}

func quietClearDue(s LineState, at time.Time) bool {
	return s.Identity != IdentityRear && !s.Trial && (s.Level > 0 || s.Admissions > 0) &&
		!s.LastRear.IsZero() && !at.Before(s.LastRear.Add(LevelClearQuiet))
}

// Relaxed forgets this line's history without changing its key or other lines.
func Relaxed(k LineKey) LineState { return LineState{LineKey: k} }
