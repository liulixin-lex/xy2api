package service

import "github.com/liulixin-lex/xy2api/internal/config"

// cacheL07Caps are the static hard caps of keep_sticky_on_busy (DESIGN
// §6.1A.5). Runtime values above a cap are rejected; a cap lowered after a
// restart clips the stored value. The guard values are static only.
type cacheL07Caps struct {
	MaxConsecutive    int
	MaxHoldSeconds    int
	GroupKeepRatioPct int
	GuardErrorDeltaPP int
	GuardPauseSeconds int
}

func defaultCacheL07Caps() cacheL07Caps {
	return cacheL07Caps{MaxConsecutive: 5, MaxHoldSeconds: 300, GroupKeepRatioPct: 20, GuardErrorDeltaPP: 5, GuardPauseSeconds: 600}
}

// cacheL07CapsFromConfig keeps the default for every unset (zero) cap.
func cacheL07CapsFromConfig(cfg *config.Config) cacheL07Caps {
	caps := defaultCacheL07Caps()
	if cfg == nil {
		return caps
	}
	c := cfg.Gateway.CacheRouting
	if c.L07MaxConsecutiveCap > 0 {
		caps.MaxConsecutive = min(c.L07MaxConsecutiveCap, 5)
	}
	if c.L07MaxHoldSecondsCap > 0 {
		caps.MaxHoldSeconds = min(c.L07MaxHoldSecondsCap, 300)
	}
	if c.L07GroupKeepRatioCapPct > 0 {
		caps.GroupKeepRatioPct = min(c.L07GroupKeepRatioCapPct, 20)
	}
	if c.L07GuardErrorDeltaPP > 0 {
		caps.GuardErrorDeltaPP = c.L07GuardErrorDeltaPP
	}
	if c.L07GuardPauseSeconds > 0 {
		caps.GuardPauseSeconds = c.L07GuardPauseSeconds
	}
	return caps
}

// L07GuardTripped reports whether the process guard (A5) pauses L-07. The
// administrator view shows it as the guard_tripped blocker; it never changes
// the stored policy.
func (s *CachePolicyService) L07GuardTripped() bool {
	if s == nil || s.l07 == nil {
		return false
	}
	return s.l07.guardTripped(s.now())
}

// CacheRoutingL07Policy is the typed keep_sticky_on_busy policy of one
// snapshot (DESIGN §6.1A A6). It is built once at publication with the static
// caps applied; requests read it through the snapshot pointer and never
// allocate. Values are effective values: min(stored, static cap).
type CacheRoutingL07Policy struct {
	RolloutPercent         int
	MaxConsecutiveBusy     int
	MaxHoldSeconds         int
	OwnerBusySkipPerMinCap int // 0: max(4, 2×owner concurrency)
	GroupKeepRatioPct      int
	HealthLookbackSeconds  int
	GuardErrorDeltaPP      int
	GuardPauseSeconds      int
	active                 bool
	revision               int64
	groups                 map[int64]struct{} // empty: every group
}

// routingL07FromEntry folds the build capability, the kill switch, the
// master switch and the entry's own validity into active, so the request
// path has a single check.
func routingL07FromEntry(entry cachePolicyEntry, master, capable bool, caps cacheL07Caps) CacheRoutingL07Policy {
	busy, _ := entry.envelope.Spec["keep_sticky_on_busy"].(map[string]any)
	field := func(name string, def, lo, hi int) int {
		v, ok := busy[name]
		if !ok {
			return def
		}
		n := policyInt(v)
		return max(lo, min(n, hi))
	}
	p := CacheRoutingL07Policy{
		revision:               entry.envelope.Rev,
		RolloutPercent:         field("rollout_percent", 0, 0, 100),
		MaxConsecutiveBusy:     field("max_consecutive_busy", 2, 1, caps.MaxConsecutive),
		MaxHoldSeconds:         field("max_hold_seconds", 60, 10, caps.MaxHoldSeconds),
		OwnerBusySkipPerMinCap: field("owner_busy_skip_per_min_cap", 0, 0, 1000),
		GroupKeepRatioPct:      field("group_keep_ratio_pct", 5, 1, caps.GroupKeepRatioPct),
		HealthLookbackSeconds:  field("health_lookback_seconds", 120, 30, 600),
		GuardErrorDeltaPP:      caps.GuardErrorDeltaPP,
		GuardPauseSeconds:      caps.GuardPauseSeconds,
	}
	if ids, ok := busy["group_ids"].([]int64); ok && len(ids) > 0 {
		p.groups = make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			p.groups[id] = struct{}{}
		}
	}
	enabled, _ := busy["enabled"].(bool)
	p.active = capable && master && !entry.parseError && enabled && p.RolloutPercent > 0
	return p
}

// AllowsGroup tests the serving group against group_ids (empty: all).
func (p *CacheRoutingL07Policy) AllowsGroup(groupID int64) bool {
	if p == nil {
		return false
	}
	if len(p.groups) == 0 {
		return true
	}
	_, ok := p.groups[groupID]
	return ok
}

func (p *CacheRoutingL07Policy) Revision() int64 {
	if p == nil {
		return 0
	}
	return p.revision
}

// RoutingL07 returns the request policy of L-07 when it is active, nil
// otherwise: no kill switch, master on, build capability l07, valid entries,
// enabled with a positive rollout, and a fresh snapshot (FX-POL-2: routing is
// a behavior key, so a stale-bounded snapshot turns it off).
func (p *CachePolicySnapshot) RoutingL07() *CacheRoutingL07Policy {
	if p == nil || !p.routingL07.active || p.behaviorStale() {
		return nil
	}
	return &p.routingL07
}
