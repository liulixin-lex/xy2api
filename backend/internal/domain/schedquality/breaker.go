package schedquality

import "time"

// Next is section 7. prev.State == StateNormal with zero Since means "no row".
// remove = delete the row; wantTest = this row is waiting for test evidence. Next does not know whether a test is
// running: the executor asks the runner, which starts one only if none is running and TestRetryEvery has passed.
func Next(prev StateRow, in Inputs, now time.Time) (next StateRow, changed, remove, wantTest bool) {
	next = prev
	defer func() {
		remove = !prev.Since.IsZero() && next.State == StateNormal && next.Level == 0 && now.Sub(next.Since) >= WindowMaxAge
		changed = next != prev || remove
	}()
	eff, fromTest := Effective(Judge(in.Summary, in.Policy), in.Tests, prev.Model, in.Policy, in.ResetAt, prev.Since, now)
	cool := func(reason Reason) {
		next.State = StateCooling
		next.Until = now.Add(CooldownLadder[min(next.Level, len(CooldownLadder)-1)])
		next.Level = min(next.Level+1, len(CooldownLadder))
		next.Reason, next.Since = reason, now
	}
	recover := func(verified bool) {
		next.State, next.Since, next.Until = StateNormal, now, time.Time{}
		next.Reason = ""
		if !verified {
			next.Reason = ReasonUnverified
		}
	}
	firstOver := func(includeMarginal bool) Reason {
		for i, v := range [...]Verdict{eff.Speed, eff.Error, eff.Cache} {
			if v == VerdictFail || (includeMarginal && v == VerdictMarginal) {
				if fromTest[i] {
					return [...]Reason{ReasonTestSpeed, ReasonTestError, ReasonTestCache}[i]
				}
				return [...]Reason{ReasonSpeed, ReasonError, ReasonCache}[i]
			}
		}
		return ""
	}
	if next.State == StateNormal {
		if !next.Since.IsZero() && now.Sub(next.Since) >= LevelDecayAfter {
			next.Level, next.Reason = 0, ""
		}
		if in.Summary.Acute {
			cool(ReasonAcuteTimeout)
		} else if reason := firstOver(false); reason != "" {
			cool(reason)
		}
		return
	}
	if now.Before(next.Until) {
		return
	}
	if !in.Policy.Enabled {
		recover(false)
		return
	}
	if in.Summary.Acute {
		cool(ReasonAcuteTimeout)
		return
	}
	if reason := firstOver(true); reason != "" {
		cool(reason)
		return
	}
	var caused Verdict
	switch next.Reason {
	case ReasonSpeed, ReasonTestSpeed, ReasonAcuteTimeout:
		caused = eff.Speed
	case ReasonError, ReasonTestError:
		caused = eff.Error
	case ReasonCache, ReasonTestCache:
		caused = eff.Cache
	}
	if caused == VerdictPass {
		recover(true)
	} else if !in.TestsEnabled || now.Sub(next.Until) >= TestWaitMax {
		recover(false)
	} else {
		wantTest = true
	}
	return
}
