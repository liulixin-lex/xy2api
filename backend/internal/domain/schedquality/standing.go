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

// StandingOf applies placement after the caller checks global/group enablement and view freshness.
func StandingOf(v StateView, auto bool, now time.Time) Standing {
	ref, _ := v.EffectiveOverride(now)
	if ref.Mode == OverrideExempt {
		return StandFront
	}
	if auto && v.Failing {
		return StandBack
	}
	return StandFront
}
