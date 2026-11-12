//go:build unit

package schedquality

import (
	"testing"
	"time"
)

func TestStandingAndTakeover(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		v    StateView
		auto bool
		want Standing
	}{
		{StateView{State: StateNormal, GroupOverride: OverrideRef{Mode: OverrideDemote}}, true, StandBack},
		{StateView{State: StateCooling, GroupOverride: OverrideRef{Mode: OverrideExempt}}, true, StandFront},
		{StateView{State: StateCooling}, false, StandFront},
		{StateView{State: StateCooling}, true, StandBack},
		{StateView{State: StateNormal}, true, StandFront},
		{StateView{State: StateNormal, GroupOverride: OverrideRef{Mode: OverrideExempt, Expires: now}, AllOverride: OverrideRef{Mode: OverrideDemote}}, true, StandBack},
	}
	for _, c := range cases {
		if got := StandingOf(c.v, c.auto, now); got != c.want {
			t.Fatalf("%+v: %v", c, got)
		}
	}
	for _, c := range []struct {
		enabled bool
		age     time.Duration
		want    Standing
	}{
		{true, ViewMaxAge, StandBack},
		{true, ViewMaxAge + time.Nanosecond, StandFront},
		{false, 0, StandFront},
	} {
		auto := c.enabled && c.age <= ViewMaxAge
		if got := StandingOf(StateView{State: StateCooling}, auto, now); got != c.want {
			t.Fatal("view age/global switch", c, got)
		}
	}
	for _, c := range []struct {
		group, all OverrideRef
		mode       Override
		allGroups  bool
	}{
		{OverrideRef{Mode: OverrideExempt}, OverrideRef{Mode: OverrideDemote}, OverrideExempt, false},
		{OverrideRef{Mode: OverrideExempt, Expires: now}, OverrideRef{Mode: OverrideDemote}, OverrideDemote, true},
		{OverrideRef{Mode: OverrideExempt, Expires: now.Add(time.Nanosecond)}, OverrideRef{Mode: OverrideDemote}, OverrideExempt, false},
		{OverrideRef{}, OverrideRef{Mode: OverrideDemote, Expires: now}, OverrideNone, false},
	} {
		ref, allGroups := (StateView{GroupOverride: c.group, AllOverride: c.all}).EffectiveOverride(now)
		if ref.Mode != c.mode || allGroups != c.allGroups {
			t.Fatal("override precedence", c, ref, allGroups)
		}
	}
	v := StateView{State: StateNormal, Reason: ReasonUnverified, Since: now.Add(-LevelDecayAfter + time.Nanosecond)}
	if CanTakeOver(v, true, now) {
		t.Fatal("unverified")
	}
	v.Since = now.Add(-LevelDecayAfter)
	if !CanTakeOver(v, true, now) {
		t.Fatal("boundary")
	}
	v.Since = now
	v.GroupOverride = OverrideRef{Mode: OverrideExempt}
	if !CanTakeOver(v, true, now) || CanTakeOver(v, false, now) {
		t.Fatal("exempt or auto")
	}
}
