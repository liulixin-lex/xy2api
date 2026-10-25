package service

import (
	"sync/atomic"
	"time"
)

// keep_sticky_on_busy limit leaves of the routing policy (cache_policies
// m4l07 §1, DESIGN §6.1A.5). They are plain parameters, not capability
// fields: they are storable while L-07 is not implemented and never carry
// not_implemented_in_build.
const cacheL07Prefix = "keep_sticky_on_busy."

type cacheL07Field struct {
	name      string // leaf under keep_sticky_on_busy
	def       int
	min, max  int // contract range; max is also the ceiling of a static cap
	unit      string
	capSource string // static hard cap; empty when the leaf has none
}

// Contract order: inserted after rollout_percent, before affinity_mode.
var cacheL07Fields = [...]cacheL07Field{
	{name: "max_consecutive_busy", def: 2, min: 1, max: 5, capSource: "gateway.cache_routing.l07_max_consecutive_cap"},
	{name: "max_hold_seconds", def: 60, min: 10, max: 300, capSource: "gateway.cache_routing.l07_max_hold_seconds_cap"},
	{name: "owner_busy_skip_per_min_cap", def: 0, min: 0, max: 1000},
	{name: "group_keep_ratio_pct", def: 5, min: 1, max: 20, unit: "percent", capSource: "gateway.cache_routing.l07_group_keep_ratio_cap_pct"},
	{name: "health_lookback_seconds", def: 120, min: 30, max: 600},
}

func cacheL07FieldOf(field string) (cacheL07Field, bool) {
	for _, f := range cacheL07Fields {
		if cacheL07Prefix+f.name == field {
			return f, true
		}
	}
	return cacheL07Field{}, false
}

// limit is the current schema max: the static cap for capped leaves.
func (f cacheL07Field) limit(caps cacheL07Caps) int {
	value := 0
	switch f.name {
	case "max_consecutive_busy":
		value = caps.MaxConsecutive
	case "max_hold_seconds":
		value = caps.MaxHoldSeconds
	case "group_keep_ratio_pct":
		value = caps.GroupKeepRatioPct
	default:
		return f.max
	}
	if value <= 0 {
		return f.max
	}
	return min(value, f.max)
}

func cacheL07AddDefaults(busy map[string]any) {
	for _, f := range cacheL07Fields {
		busy[f.name] = f.def
	}
}

func cacheL07Schema(caps cacheL07Caps) []map[string]any {
	out := make([]map[string]any, 0, len(cacheL07Fields))
	for _, f := range cacheL07Fields {
		entry := map[string]any{"field": cacheL07Prefix + f.name, "type": "int", "min": f.min, "max": f.limit(caps)}
		if f.unit != "" {
			entry["unit"] = f.unit
		}
		if f.capSource != "" {
			entry["cap_source"] = f.capSource
		}
		out = append(out, entry)
	}
	return out
}

// cacheM4IntError checks an integer M4 leaf. Writes above a static cap are
// exceeds_static_cap. A stored value above a cap lowered after a restart
// loads and is clipped (effective.clipped_fields), as with observability's
// byte_budget_mib. A value outside the contract range never loads.
func cacheM4IntError(index int, field string, v int, caps cacheL07Caps, writing bool) *CachePolicyFieldError {
	f, ok := cacheL07FieldOf(field)
	if index != 5 || !ok {
		if v < 0 || v > 100 {
			return &CachePolicyFieldError{Field: field, Code: "out_of_range", Limit: map[string]int{"min": 0, "max": 100}}
		}
		return nil
	}
	hi := f.limit(caps)
	switch {
	case v < f.min || (f.capSource == "" && v > f.max):
		return &CachePolicyFieldError{Field: field, Code: "out_of_range", Limit: map[string]int{"min": f.min, "max": hi}}
	case f.capSource != "" && v > hi && writing:
		return &CachePolicyFieldError{Field: field, Code: "exceeds_static_cap", Limit: map[string]int{"max": hi}}
	case v > f.max:
		return &CachePolicyFieldError{Field: field, Code: "out_of_range", Limit: map[string]int{"min": f.min, "max": hi}}
	}
	return nil
}

// cacheL07Direction applies the two special rules. A longer health lookback
// is more conservative. owner_busy_skip_per_min_cap=0 means automatic, so
// non-0 to 0 relaxes the cap and 0 to non-0 tightens it.
func cacheL07Direction(field string, before, after int) (up, down, handled bool) {
	switch field {
	case cacheL07Prefix + "health_lookback_seconds":
		return after < before, after > before, true
	case cacheL07Prefix + "owner_busy_skip_per_min_cap":
		switch {
		case after == 0 && before != 0:
			return true, false, true
		case before == 0 && after != 0:
			return false, true, true
		}
		return after > before, after < before, true
	}
	return false, false, false
}

func (s *CachePolicyService) cacheL07ClippedFields(spec map[string]any) []string {
	out := []string{}
	for _, f := range cacheL07Fields {
		if f.capSource != "" && policyInt(m4PolicyField(spec, cacheL07Prefix+f.name)) > f.limit(s.l07Caps) {
			out = append(out, cacheL07Prefix+f.name)
		}
	}
	return out
}

// Share-active groups are scheduled by the share hook as a whole, so L-07
// has no effect in them (DESIGN §6.1A.3). The lookup reads in-memory share
// state only; it never queries a repository.
type cachePolicyShareActiveFunc func(groupID int64) bool

type cachePolicyShareLookup struct {
	active atomic.Pointer[cachePolicyShareActiveFunc]
}

// WithCachePolicyShareActive supplies the share-active group lookup.
func WithCachePolicyShareActive(active func(groupID int64) bool) CachePolicyOption {
	return func(s *CachePolicyService) { s.setCachePolicyShareActive(active) }
}

func (s *CachePolicyService) setCachePolicyShareActive(active func(groupID int64) bool) {
	if s == nil || active == nil {
		return
	}
	f := cachePolicyShareActiveFunc(active)
	s.share.active.Store(&f)
}

// attachShareScheduling wires the share scheduling state after both services
// exist. Administrator views read it through an atomic pointer.
func (s *CachePolicyService) attachShareScheduling(share *ShareSchedulingService) {
	if share == nil {
		return
	}
	s.setCachePolicyShareActive(share.shareSchedulingActive)
}

// Explicit IDs only: an empty busy scope means every group, not a selection.
func (s *CachePolicyService) cacheL07ShareGroupSelected(spec map[string]any) bool {
	if s == nil {
		return false
	}
	active := s.share.active.Load()
	if active == nil {
		return false
	}
	ids, _ := m4PolicyField(spec, cacheL07Prefix+"group_ids").([]int64)
	for _, id := range ids {
		if (*active)(id) {
			return true
		}
	}
	return false
}

// WithCacheL07GuardTrippedUntil starts the service with the process guard
// (A5) paused until the given time. Contract replays use it; in production
// only the request path trips the guard.
func WithCacheL07GuardTrippedUntil(until time.Time) CachePolicyOption {
	return func(s *CachePolicyService) {
		if s.l07 != nil {
			s.l07.guardUntil.Store(until.UnixNano())
		}
	}
}
