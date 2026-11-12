package schedquality

import "time"

func (v StateView) EffectiveOverride(now time.Time) (ref OverrideRef, allGroups bool) {
	if v.GroupOverride.Mode != OverrideNone && (v.GroupOverride.Expires.IsZero() || now.Before(v.GroupOverride.Expires)) {
		return v.GroupOverride, false
	}
	if v.AllOverride.Mode != OverrideNone && (v.AllOverride.Expires.IsZero() || now.Before(v.AllOverride.Expires)) {
		return v.AllOverride, true
	}
	return OverrideRef{}, false
}

// StandingOf: auto = GlobalEnabled() && now-At() <= ViewMaxAge.
func StandingOf(v StateView, auto bool, now time.Time) Standing {
	ref, _ := v.EffectiveOverride(now)
	if ref.Mode == OverrideDemote {
		return StandBack
	}
	if ref.Mode == OverrideExempt {
		return StandFront
	}
	if auto && v.State == StateCooling {
		return StandBack
	}
	return StandFront
}

// CanTakeOver is the quality part of the takeover conditions (section 8): auto is true, front standing, and not "unverified"
// (State == StateNormal && Reason == "unverified" && now-Since < LevelDecayAfter); an exempt override skips the last check.
func CanTakeOver(v StateView, auto bool, now time.Time) bool {
	if !auto || StandingOf(v, auto, now) != StandFront {
		return false
	}
	ref, _ := v.EffectiveOverride(now)
	return ref.Mode == OverrideExempt || v.State != StateNormal || v.Reason != ReasonUnverified || now.Sub(v.Since) >= LevelDecayAfter
}
