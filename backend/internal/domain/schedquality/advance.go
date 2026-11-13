package schedquality

import "time"

// Cooldown durations are fixed by the scheduling rules.
var ruleCooldownLadder = [...]time.Duration{20 * time.Minute, 40 * time.Minute, 60 * time.Minute, 90 * time.Minute, 120 * time.Minute}

// Advance returns the internal state even for exempt accounts. Effective
// placement is applied separately by StandingOf, so exemptions never clear or
// freeze the underlying evidence and cooldown history.
func Advance(prev StateRow, p Policy, samples []AttemptSample, now time.Time) (StateRow, Evaluation) {
	return AdvanceAt(prev, p, samples, now, EvaluateDeadline)
}

func AdvanceAt(prev StateRow, p Policy, samples []AttemptSample, now time.Time, kind EvaluationKind) (StateRow, Evaluation) {
	scope := Scope{AccountID: prev.AccountID, Model: prev.Model, GroupID: prev.GroupID}
	e := EvaluateAt(samples, scope, p, prev.EvidenceSince, now, kind)
	next := prev
	cool := func(reason Reason) {
		next.Level = min(max(next.Level, 0)+1, len(ruleCooldownLadder))
		next.State, next.Reason = StateCooling, reason
		next.Since, next.Until = now, now.Add(ruleCooldownLadder[next.Level-1])
		basis := DescribeDecision(e, samples, p)
		next.Basis = &basis
	}
	if prev.State == StateCooling {
		if now.Before(prev.Until) || now.Before(prev.Since) {
			return next, e
		}
		// Keep the full rolling windows of other-group evidence. A completion
		// after this round began is an additional prerequisite, not a new
		// evidence cutoff. Moving Since prevents the same batch renewing twice.
		other := evaluate(samples, scope, p, prev.EvidenceSince, now, true, EvaluateDeadline)
		foreign := completedSamples(samples, scope, prev.EvidenceSince, now, true)
		fresh := len(foreign) > 0 && foreign[len(foreign)-1].FinishedAt.After(prev.Since)
		if reason := other.Reason(); fresh && reason != "" {
			e = other
			cool(reason)
			return next, other
		}
		next.State, next.Reason = StateNormal, ""
		next.Since, next.Until, next.EvidenceSince = now, time.Time{}, now
		return next, Evaluate(samples, scope, p, next.EvidenceSince, now)
	}
	if !next.EvidenceSince.IsZero() && now.Sub(next.EvidenceSince) >= LevelDecayAfter {
		next.Level = 0
	}
	if reason := e.Reason(); reason != "" {
		cool(reason)
	}
	return next, e
}

// Restore clears the account/group/model's quality history. The service also
// persists an account-wide reset cutoff for scopes that do not yet have rows.
func Restore(prev StateRow, at time.Time) StateRow {
	return StateRow{AccountID: prev.AccountID, Model: prev.Model, GroupID: prev.GroupID,
		State: StateNormal, Since: at, EvidenceSince: at}
}
