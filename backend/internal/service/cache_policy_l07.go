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
