package service

import (
	"cmp"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type schedPerfReach struct {
	cache              schedPerfCont
	outputs, timeouts  []uint64 // Sorted milliseconds for first streaming dispatches.
	secondSessionBound uint64   // The second highest bound among distinct first-output sessions.
}

type schedPerfReachSession struct {
	cache bool
	bound uint64 // 0: unknown; wait+1: timeout; MaxUint64: observed first output.
}

type schedPerfReachSessionKey struct {
	group int64
	key   string
}

type schedPerfReachBuilder struct {
	row      schedPerfReach
	sessions map[schedPerfReachSessionKey]schedPerfReachSession
}

func (b *schedPerfReachBuilder) add(f SchedPerfFact) {
	cache := f.Input > 0
	if cache {
		b.row.cache.Turns++
		b.row.cache.InputTokens += f.Input
		b.row.cache.ReadTokens += f.Read
	}
	var bound uint64
	if f.First && f.Stream {
		if ms, censored, ok := perfFirstClock(f); ok {
			if censored {
				b.row.timeouts = append(b.row.timeouts, ms)
				bound = ms + 1
			} else {
				b.row.outputs = append(b.row.outputs, ms)
				bound = math.MaxUint64
			}
		}
	}
	if f.Session != "" && (cache || bound != 0) {
		if b.sessions == nil {
			b.sessions = make(map[schedPerfReachSessionKey]schedPerfReachSession)
		}
		key := schedPerfReachSessionKey{f.Group, f.Session}
		s := b.sessions[key]
		s.cache = s.cache || cache
		if bound > s.bound {
			s.bound = bound
		}
		b.sessions[key] = s
	}
}

func (b *schedPerfReachBuilder) freeze() schedPerfReach {
	var highest uint64
	for _, s := range b.sessions {
		if s.cache {
			b.row.cache.Sessions++
		}
		// Session eligibility is monotonic in the limit, so two highest bounds
		// suffice to tell exactly when at least two sessions remain eligible.
		if s.bound > highest {
			b.row.secondSessionBound, highest = highest, s.bound
		} else if s.bound > b.row.secondSessionBound {
			b.row.secondSessionBound = s.bound
		}
	}
	slices.Sort(b.row.outputs)
	slices.Sort(b.row.timeouts)
	return b.row
}

func (r schedPerfReach) countFirst(limit uint64) (n, slow int, enough bool) {
	timeouts := len(r.timeouts) - sort.Search(len(r.timeouts), func(i int) bool { return r.timeouts[i] >= limit })
	n = len(r.outputs) + timeouts
	if n < 20 || r.secondSessionBound != math.MaxUint64 && r.secondSessionBound <= limit {
		return n, 0, false
	}
	slow = len(r.outputs) - sort.Search(len(r.outputs), func(i int) bool { return r.outputs[i] > limit }) + timeouts
	return n, slow, true
}

func (s *SchedPerfSnapshot) policyReach(members map[int64]string, resets map[int64]time.Time, policy schedquality.Policy, tolerance float64) []SchedAdminPolicyReach {
	limit := perfLimitMillis(policy.TTFTSeconds)
	byModel := make(map[string]SchedAdminPolicyReach)
	for key, row := range s.reach {
		if _, ok := members[key.AccountID]; !ok {
			continue
		}
		result := byModel[key.Model]
		result.Model = key.Model
		// A newer manual reset invalidates this account's published aggregates
		// until the background worker publishes data cut at that reset.
		if !resets[key.AccountID].After(s.resets[key.AccountID]) {
			if c := row.cache; c.Turns >= 20 && c.Sessions >= 2 {
				result.Cache.N++
				if float64(c.ReadTokens)/float64(c.InputTokens) >= policy.MinCacheRatio {
					result.Cache.OK++
				}
			}
			if n, slow, enough := row.countFirst(limit); enough {
				result.FirstOutput.N++
				if float64(slow)/float64(n) <= tolerance {
					result.FirstOutput.OK++
				}
			}
		}
		byModel[key.Model] = result
	}
	out := make([]SchedAdminPolicyReach, 0, len(byModel))
	for _, row := range byModel {
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b SchedAdminPolicyReach) int { return cmp.Compare(a.Model, b.Model) })
	return out
}
