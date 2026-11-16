package schedquality

import "time"

type LineView struct {
	Identity          Identity
	Trial             bool
	Level             int
	Until, TrialUntil time.Time `json:",omitzero"`
}
type StateView struct {
	TTFO, Error, Cache         LineView
	GroupOverride, AllOverride OverrideRef
}
type Placement int8

const (
	PlaceFront Placement = iota
	PlaceTrial
	PlaceRear
)

// 豁免生效或 !auto → Front；任一线 Rear → Rear；任一线 Trial → Trial；其余 Front。
func (v StateView) Placement(auto bool, now time.Time) Placement {
	ref, _ := v.EffectiveOverride(now)
	if !auto || ref.Mode == OverrideExempt {
		return PlaceFront
	}
	lines := [...]LineView{v.TTFO, v.Error, v.Cache}
	for _, line := range lines {
		if line.Identity == IdentityRear {
			return PlaceRear
		}
	}
	for _, line := range lines {
		if line.Trial {
			return PlaceTrial
		}
	}
	return PlaceFront
}

// 豁免或 !auto → false；Error 线后排 → true；TTFO 线后排且 slowTwice → true；只有 Cache 线后排 → false。
func (v StateView) MigrateOwner(auto bool, now time.Time, slowTwice bool) bool {
	ref, _ := v.EffectiveOverride(now)
	return auto && ref.Mode != OverrideExempt &&
		(v.Error.Identity == IdentityRear || (v.TTFO.Identity == IdentityRear && slowTwice))
}

type View interface {
	Version() int64
	At() time.Time
	GlobalEnabled() bool
	GroupEnabled(groupID int64) bool
	ResetAt(accountID int64) time.Time
	StateOf(groupID, accountID int64, model string) StateView
	TakeoverIdle(groupID int64) (idle time.Duration, enabled bool)
	// N6：该会话（组×模型×会话）最近两次尝试都在这个号上，并且按本组 L 都超限
	// （TTFOMs>L，或没有首字且 CensoredMs≥L；换号首轮也算一轮）。
	SessionSlowTwice(groupID, accountID int64, model, sessionKey string) bool
}

func (v StateView) EffectiveOverride(now time.Time) (ref OverrideRef, allGroups bool) {
	if v.GroupOverride.Mode != OverrideNone && (v.GroupOverride.Expires.IsZero() || now.Before(v.GroupOverride.Expires)) {
		return v.GroupOverride, false
	}
	if v.AllOverride.Mode != OverrideNone && (v.AllOverride.Expires.IsZero() || now.Before(v.AllOverride.Expires)) {
		return v.AllOverride, true
	}
	return OverrideRef{}, false
}
