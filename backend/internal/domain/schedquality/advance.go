package schedquality

import "time"

// Due chooses one event; repeated timestamps never advance a line twice.
// Trial phase checkpoints are exact timestamps, including non-minute origins.
func Due(s LineState, at time.Time, fiveMin, touched bool) (EventKind, bool) {
	if !at.After(s.EvaluatedAt) {
		return EventRegular, false
	}
	if s.Identity == IdentityRear {
		return EventReview, !at.Before(s.Until)
	}
	if s.Trial {
		return EventTrial, touched || !at.Before(s.TrialUntil) || at.Equal(s.Epoch.Add(TrialMinimum))
	}
	if fiveMin {
		return EventRegular, true
	}
	return EventRegular, false
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
	if prev.Identity == IdentityRear && prev.Until.IsZero() || prev.Trial && prev.TrialUntil.IsZero() {
		basis.MissingDeadline = true
	}
	var action Action
	switch kind {
	case EventRegular:
		if s.Identity == IdentityRear || s.Trial {
			return prev, ""
		}
		switch v {
		case VerdictFail:
			s.GoodSince = time.Time{}
			if s.Line == DimensionTTFO || singleCacheConfirmation(s.Line, basis.Cache) || (!s.PendingAt.IsZero() && s.PendingAt.Equal(at.Add(-RegularInterval))) {
				enterRear(&s, at)
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
			enterRear(&s, at)
			action = ActionRenew
		default:
			origin := s.Until
			if origin.IsZero() {
				origin, basis.MissingDeadline = at, true
			}
			s.Identity, s.Trial = IdentityUnknown, true
			s.Level = 0
			s.Epoch, s.TrialUntil = origin, origin.Add(TrialDuration)
			s.Until, s.GoodSince, s.PendingAt = time.Time{}, time.Time{}, time.Time{}
			action = ActionTrial
			if !at.Before(s.TrialUntil) {
				releaseTrial(&s, &basis, at, "deadline")
				action = ActionTrialTimeout
			}
		}
	case EventTrial:
		if !s.Trial || s.Identity == IdentityRear {
			return prev, ""
		}
		switch v {
		case VerdictFail:
			recordTrialExit(&basis, s.Epoch, at, "fail")
			enterRear(&s, at)
			s.Admissions++
			action = ActionTrialFail
		default:
			if s.Line == DimensionCache && v == VerdictPass && (basis.Cache == nil || basis.Cache.Turns < TrialCacheTurns) {
				v = VerdictUnknown
			}
			switch {
			case v == VerdictPass && !at.Before(s.Epoch.Add(TrialMinimum)):
				releaseTrial(&s, &basis, at, "good")
				action = ActionTrialPass
			case !at.Before(s.TrialUntil):
				releaseTrial(&s, &basis, at, "deadline")
				action = ActionTrialTimeout
			}
		}
	default:
		return prev, ""
	}
	s.LastAssessment = RecordAssessment(prev, at, kind, v, action, basis)
	s.EvaluatedAt = at
	if action != "" || s.Identity != prev.Identity || s.Trial != prev.Trial {
		s.Basis = &basis
	}
	return s, action
}

func singleCacheConfirmation(line Dimension, cache *CacheAssessment) bool {
	return line == DimensionCache && cache != nil && (cache.Path == CachePathSingle5 || cache.Path == CachePathDeep10)
}

func enterRear(s *LineState, at time.Time) {
	s.Level = 0
	s.Identity, s.Trial = IdentityRear, false
	s.Entry, s.Epoch, s.LastRear = at, at, at
	s.Until = at.Add(RearDuration)
	s.TrialUntil, s.GoodSince, s.PendingAt = time.Time{}, time.Time{}, time.Time{}
}

func releaseTrial(s *LineState, basis *Basis, at time.Time, reason string) {
	if reason == "deadline" && !s.TrialUntil.IsZero() && s.TrialUntil.Before(at) {
		at = s.TrialUntil
	}
	recordTrialExit(basis, s.Epoch, at, reason)
	s.Identity, s.Trial, s.Epoch = IdentityUnknown, false, at
	s.Level = 0
	s.Until, s.TrialUntil, s.PendingAt, s.GoodSince = time.Time{}, time.Time{}, time.Time{}, time.Time{}
}

func recordTrialExit(basis *Basis, origin, at time.Time, reason string) {
	basis.ReleaseReason = reason
	if !origin.IsZero() {
		basis.TrialDurationSeconds = max(0, int64(at.Sub(origin)/time.Second))
	}
}

// Evaluation is an assessment frozen before any member of a group transitions.
type Evaluation struct {
	Kind    EventKind
	Verdict Verdict
	Basis   Basis
}

// StepGroup advances one account/group/model using evaluations frozen before any transition.
func StepGroup(prev []LineState, evaluations map[Dimension]Evaluation, at time.Time) ([]LineState, []Action) {
	next, actions := make([]LineState, len(prev)), make([]Action, len(prev))
	for i, r := range prev {
		next[i] = r
		if e, ok := evaluations[r.Line]; ok {
			next[i], actions[i] = Step(r, e.Kind, e.Verdict, e.Basis, at)
		}
	}
	return next, actions
}

// Relaxed forgets this line's history without changing its key or other lines.
func Relaxed(k LineKey) LineState { return LineState{LineKey: k} }
