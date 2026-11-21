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
		return EventTrial, touched || !at.Before(s.TrialUntil) || at.Equal(s.Epoch.Add(TrialMinimum)) || at.Equal(s.Epoch.Add(TrialSampleCheck))
	}
	if fiveMin {
		return EventRegular, true
	}
	return EventClock, false
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
		default:
			origin := s.Until
			if origin.IsZero() {
				origin, basis.MissingDeadline = at, true
			}
			s.Identity, s.Trial = IdentityUnknown, true
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
			enterRear(&s, at, true)
			s.Admissions++
			action = ActionTrialFail
		default:
			if s.TrialUntil.IsZero() {
				basis.MissingDeadline = true
			}
			switch {
			case v == VerdictPass && !at.Before(s.Epoch.Add(TrialMinimum)):
				releaseTrial(&s, &basis, at, "qualified")
				action = ActionTrialPass
			case !at.Before(s.TrialUntil):
				releaseTrial(&s, &basis, at, "deadline")
				action = ActionTrialTimeout
			case !at.Before(s.Epoch.Add(TrialSampleCheck)) && basis.OwnSampleAt60 != nil && !*basis.OwnSampleAt60:
				releaseTrial(&s, &basis, at, "no_samples")
				action = ActionTrialTimeout
			}
		}
	case EventClock:
		// History clearing requires the entire group; StepGroup owns that clock.
		return prev, ""
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

func enterRear(s *LineState, at time.Time, escalate bool) {
	if escalate {
		s.Level = min(s.Level+1, len(CooldownLadder)-1)
	}
	s.Identity, s.Trial = IdentityRear, false
	s.Entry, s.Epoch, s.LastRear = at, at, at
	s.Until = at.Add(CooldownLadder[s.Level])
	s.TrialUntil, s.GoodSince, s.PendingAt = time.Time{}, time.Time{}, time.Time{}
}

func releaseTrial(s *LineState, basis *Basis, at time.Time, reason string) {
	basis.ReleaseReason = reason
	basis.TrialDurationSeconds = int64(at.Sub(s.Epoch) / time.Second)
	s.Identity, s.Trial, s.Epoch = IdentityUnknown, false, at
	s.Until, s.TrialUntil, s.PendingAt, s.GoodSince = time.Time{}, time.Time{}, time.Time{}, time.Time{}
}

// Evaluation is an assessment frozen before any member of a group transitions.
type Evaluation struct {
	Kind    EventKind
	Verdict Verdict
	Basis   Basis
}

// QuietDeadline exists only while the whole group is normal. A level-zero
// admission is still history; a stale LastRear alone is not.
func QuietDeadline(rows []LineState) time.Time {
	var last time.Time
	for _, r := range rows {
		if r.Identity == IdentityRear || r.Trial {
			return time.Time{}
		}
		if (r.Level > 0 || r.Admissions > 0) && r.LastRear.After(last) {
			last = r.LastRear
		}
	}
	if last.IsZero() {
		return last
	}
	return last.Add(LevelClearQuiet)
}

// StepGroup advances one account/group/model atomically using frozen history.
// Only confirmed failures share the new level; other transitions remain local.
func StepGroup(prev []LineState, evaluations map[Dimension]Evaluation, at time.Time) ([]LineState, []Action) {
	next, actions := make([]LineState, len(prev)), make([]Action, len(prev))
	level := -1
	for _, r := range prev {
		if r.Level > 0 || r.Admissions > 0 {
			level = max(level, r.Level)
		}
	}
	level = min(level+1, len(CooldownLadder)-1)
	bad := false
	for i, r := range prev {
		next[i] = r
		if e, ok := evaluations[r.Line]; ok {
			next[i], actions[i] = Step(r, e.Kind, e.Verdict, e.Basis, at)
		}
		if actions[i] == ActionEnter || actions[i] == ActionRenew || actions[i] == ActionTrialFail {
			bad = true
			next[i].Level, next[i].Until = level, at.Add(CooldownLadder[level])
		}
	}
	deadline := QuietDeadline(next)
	if !bad && !deadline.IsZero() && !at.Before(deadline) {
		for i, r := range next {
			if (r.Level > 0 || r.Admissions > 0) && at.After(prev[i].EvaluatedAt) {
				next[i].Level, next[i].Admissions = 0, 0
				next[i].EvaluatedAt = at
				actions[i] = ActionClear
				basis := Basis{RuleVersion: RuleVersion, Line: r.Line, Kind: "clock"}
				if e, ok := evaluations[r.Line]; ok {
					basis = e.Basis
				}
				next[i].Basis = &basis
				next[i].LastAssessment = RecordAssessment(prev[i], at, EventClock, VerdictUnknown, ActionClear, basis)
			}
		}
	}
	return next, actions
}

// Relaxed forgets this line's history without changing its key or other lines.
func Relaxed(k LineKey) LineState { return LineState{LineKey: k} }
