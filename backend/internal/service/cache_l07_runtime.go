package service

import (
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// l07Runtime is the process-local state of L-07 (DESIGN §6.1A.2 A3–A5): the
// owner and group windows, the guard, and the counters of §6.1A.6. It is
// created once with the policy service and only touched by armed requests.
// With several instances every limit is per instance, so the fleet-wide limit
// is at most the per-instance limit times the instance count (runbook).
type l07Runtime struct {
	guardUntil atomic.Int64 // unix nanoseconds; zero when the guard is not tripped

	mu     sync.Mutex
	owners map[int64]*l07Window // A3: keeps per owner, 6×10s
	groups map[int64]*l07Group  // A4/A5: per serving group, 10×30s
	recent map[string]int64     // sessions this instance kept, until (unix ms)

	keepTotal      atomic.Int64
	abandonTotal   [len(l07AbandonReasons)]atomic.Int64
	spillColdTotal atomic.Int64
	returnHitTotal atomic.Int64
	lastSummary    atomic.Int64 // unix nanoseconds
}

const (
	l07OwnerBucket       = 10 * time.Second
	l07OwnerBuckets      = 6 // 60s
	l07GroupBucket       = 30 * time.Second
	l07GroupBuckets      = 10 // 5 minutes
	l07MaxOwners         = 4096
	l07MaxGroups         = 1024
	l07MaxRecent         = 8192
	l07GuardMinSamples   = 20 // per arm, before the guard compares failure rates
	l07SummaryInterval   = 5 * time.Minute
	l07OwnerAutoCapFloor = 4
)

// l07AbandonReasons is the abandon enum of cache_trace m4l07 §2, in order.
// Counters take labels only from it.
var l07AbandonReasons = [...]string{"none", "unhealthy", "retry", "not_busy_reason", "consecutive", "hold_time", "owner_cap", "group_budget", "guard", "state_error", "policy_off"}

func newL07Runtime() *l07Runtime {
	return &l07Runtime{owners: map[int64]*l07Window{}, groups: map[int64]*l07Group{}, recent: map[string]int64{}}
}

func (r *l07Runtime) guardTripped(now time.Time) bool {
	if r == nil {
		return false
	}
	until := r.guardUntil.Load()
	return until != 0 && now.UnixNano() < until
}

// l07Window counts events in fixed buckets; a bucket is reused once its
// epoch passed out of the window.
type l07Window struct {
	epoch [l07GroupBuckets]int64
	n     [l07GroupBuckets]int64
}

func (w *l07Window) add(now time.Time, width time.Duration, buckets int, delta int64) {
	e := now.UnixNano() / int64(width)
	i := int(e % int64(buckets))
	if w.epoch[i] != e {
		w.epoch[i], w.n[i] = e, 0
	}
	w.n[i] += delta
}

func (w *l07Window) sum(now time.Time, width time.Duration, buckets int) int64 {
	e := now.UnixNano() / int64(width)
	var total int64
	for i := 0; i < buckets; i++ {
		if w.epoch[i] > e-int64(buckets) && w.epoch[i] <= e {
			total += w.n[i]
		}
	}
	return total
}

func (w *l07Window) live(now time.Time, width time.Duration, buckets int) bool {
	e := now.UnixNano() / int64(width)
	for i := 0; i < buckets; i++ {
		if w.epoch[i] > e-int64(buckets) {
			return true
		}
	}
	return false
}

// l07Group holds the group budget (A4: keeps against all in-scope requests,
// both cohorts) and the guard samples (A5): the first attempt outcome of kept
// requests on their spill account against that of the group's other
// treatment requests.
type l07Group struct {
	requests, keeps         l07Window
	keptTotal, keptFailed   l07Window
	otherTotal, otherFailed l07Window
}

func (g *l07Group) live(now time.Time) bool {
	return g.requests.live(now, l07GroupBucket, l07GroupBuckets) || g.keptTotal.live(now, l07GroupBucket, l07GroupBuckets) || g.otherTotal.live(now, l07GroupBucket, l07GroupBuckets)
}

// groupLocked returns the group's windows, evicting idle groups when the map
// is full; nil when it stays full. Caller owns r.mu.
func (r *l07Runtime) groupLocked(id int64, now time.Time) *l07Group {
	if g := r.groups[id]; g != nil {
		return g
	}
	if len(r.groups) >= l07MaxGroups {
		for k, g := range r.groups {
			if !g.live(now) {
				delete(r.groups, k)
			}
		}
		if len(r.groups) >= l07MaxGroups {
			return nil
		}
	}
	g := &l07Group{}
	r.groups[id] = g
	return g
}

func (r *l07Runtime) ownerLocked(id int64, now time.Time) *l07Window {
	if w := r.owners[id]; w != nil {
		return w
	}
	if len(r.owners) >= l07MaxOwners {
		for k, w := range r.owners {
			if !w.live(now, l07OwnerBucket, l07OwnerBuckets) {
				delete(r.owners, k)
			}
		}
		if len(r.owners) >= l07MaxOwners {
			return nil
		}
	}
	w := &l07Window{}
	r.owners[id] = w
	return w
}

// noteRequest counts an in-scope request (either cohort) of the group (A4).
func (r *l07Runtime) noteRequest(groupID int64, now time.Time) {
	r.mu.Lock()
	if g := r.groupLocked(groupID, now); g != nil {
		g.requests.add(now, l07GroupBucket, l07GroupBuckets, 1)
	}
	r.mu.Unlock()
}

// admit checks A3 then A4 for one more keep. It reports "" when both allow
// it, otherwise the abandon reason. A full map refuses (toward baseline).
func (r *l07Runtime) admit(ownerID int64, ownerConcurrency int, groupID int64, p *CacheRoutingL07Policy, now time.Time) string {
	limit := int64(p.OwnerBusySkipPerMinCap)
	if limit == 0 {
		limit = int64(max(l07OwnerAutoCapFloor, 2*ownerConcurrency))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.ownerLocked(ownerID, now)
	if w == nil || w.sum(now, l07OwnerBucket, l07OwnerBuckets) >= limit {
		return "owner_cap"
	}
	g := r.groupLocked(groupID, now)
	if g == nil {
		return "group_budget"
	}
	keeps := g.keeps.sum(now, l07GroupBucket, l07GroupBuckets)
	requests := g.requests.sum(now, l07GroupBucket, l07GroupBuckets)
	if keeps*100 >= int64(p.GroupKeepRatioPct)*requests {
		return "group_budget"
	}
	return ""
}

// noteKeep records an admitted keep (A3, A4) and the session as recently
// kept by this instance until the state key expires.
func (r *l07Runtime) noteKeep(ownerID, groupID int64, session string, until, now time.Time) {
	r.mu.Lock()
	if w := r.ownerLocked(ownerID, now); w != nil {
		w.add(now, l07OwnerBucket, l07OwnerBuckets, 1)
	}
	if g := r.groupLocked(groupID, now); g != nil {
		g.keeps.add(now, l07GroupBucket, l07GroupBuckets, 1)
	}
	key := l07RecentKey(groupID, session)
	if _, ok := r.recent[key]; !ok && len(r.recent) >= l07MaxRecent {
		ms := now.UnixMilli()
		for k, end := range r.recent {
			if end <= ms {
				delete(r.recent, k)
			}
		}
	}
	if len(r.recent) < l07MaxRecent {
		r.recent[key] = until.UnixMilli()
	} else if _, ok := r.recent[key]; ok {
		r.recent[key] = until.UnixMilli()
	}
	r.mu.Unlock()
	r.keepTotal.Add(1)
	r.maybeSummary(now)
}

// takeRecent reports and forgets whether this instance kept the session in
// its current busy segment.
func (r *l07Runtime) takeRecent(groupID int64, session string, now time.Time) bool {
	key := l07RecentKey(groupID, session)
	r.mu.Lock()
	defer r.mu.Unlock()
	end, ok := r.recent[key]
	if ok {
		delete(r.recent, key)
	}
	return ok && end > now.UnixMilli()
}

func l07RecentKey(groupID int64, session string) string {
	return strconv.FormatInt(groupID, 10) + "|" + session
}

// noteOutcome records the first attempt outcome of an armed request and
// trips the guard (A5) when kept requests fail on their spill accounts at
// least deltaPP more often than the group's other armed requests. The guard
// pauses L-07 for this process only and never changes the stored policy.
func (r *l07Runtime) noteOutcome(groupID int64, kept, failed bool, deltaPP, pauseSeconds int, now time.Time) {
	r.mu.Lock()
	g := r.groupLocked(groupID, now)
	if g == nil {
		r.mu.Unlock()
		return
	}
	total, fails := &g.otherTotal, &g.otherFailed
	if kept {
		total, fails = &g.keptTotal, &g.keptFailed
	}
	total.add(now, l07GroupBucket, l07GroupBuckets, 1)
	if failed {
		fails.add(now, l07GroupBucket, l07GroupBuckets, 1)
	}
	kt := g.keptTotal.sum(now, l07GroupBucket, l07GroupBuckets)
	kf := g.keptFailed.sum(now, l07GroupBucket, l07GroupBuckets)
	ot := g.otherTotal.sum(now, l07GroupBucket, l07GroupBuckets)
	of := g.otherFailed.sum(now, l07GroupBucket, l07GroupBuckets)
	r.mu.Unlock()
	if kt < l07GuardMinSamples || ot < l07GuardMinSamples || deltaPP <= 0 {
		return
	}
	// kf/kt - of/ot >= deltaPP/100, in integers.
	if (kf*ot-of*kt)*100 >= int64(deltaPP)*kt*ot && !r.guardTripped(now) {
		r.guardUntil.Store(now.Add(time.Duration(pauseSeconds) * time.Second).UnixNano())
		slog.Warn("cache.l07_guard_tripped", "group_id", groupID, "kept_total", kt, "kept_failed", kf, "other_total", ot, "other_failed", of, "pause_seconds", pauseSeconds)
	}
}

func (r *l07Runtime) noteAbandon(reason string, now time.Time) {
	for i, name := range l07AbandonReasons {
		if name == reason {
			r.abandonTotal[i].Add(1)
			r.maybeSummary(now)
			return
		}
	}
}

func (r *l07Runtime) noteSpillCold(now time.Time) {
	r.spillColdTotal.Add(1)
	r.maybeSummary(now)
}

func (r *l07Runtime) noteReturnHit(now time.Time) {
	r.returnHitTotal.Add(1)
	r.maybeSummary(now)
}

// CacheL07Counters is a process-local snapshot of the L-07 counters.
type CacheL07Counters struct {
	KeepTotal      int64            `json:"l07_keep_total"`
	AbandonTotal   map[string]int64 `json:"l07_abandon_total"`
	SpillColdTotal int64            `json:"l07_spill_cold_total"`
	ReturnHitTotal int64            `json:"l07_return_hit_total"`
	GuardTripped   bool             `json:"guard_tripped"`
}

func (r *l07Runtime) counters(now time.Time) CacheL07Counters {
	out := CacheL07Counters{AbandonTotal: map[string]int64{}}
	if r == nil {
		return out
	}
	out.KeepTotal = r.keepTotal.Load()
	for i, name := range l07AbandonReasons {
		if n := r.abandonTotal[i].Load(); n > 0 {
			out.AbandonTotal[name] = n
		}
	}
	out.SpillColdTotal = r.spillColdTotal.Load()
	out.ReturnHitTotal = r.returnHitTotal.Load()
	out.GuardTripped = r.guardTripped(now)
	return out
}

// L07Counters returns the process-local L-07 counters (§6.1A.6).
func (s *CachePolicyService) L07Counters() CacheL07Counters {
	if s == nil {
		return CacheL07Counters{AbandonTotal: map[string]int64{}}
	}
	return s.l07.counters(s.now())
}

// maybeSummary logs the counters at most every five minutes, only after an
// armed request changed them.
func (r *l07Runtime) maybeSummary(now time.Time) {
	last := r.lastSummary.Load()
	if last != 0 && now.UnixNano()-last < int64(l07SummaryInterval) {
		return
	}
	if !r.lastSummary.CompareAndSwap(last, now.UnixNano()) || last == 0 {
		return
	}
	c := r.counters(now)
	slog.Info("cache.l07_summary", "keep_total", c.KeepTotal, "abandon_total", c.AbandonTotal, "spill_cold_total", c.SpillColdTotal, "return_hit_total", c.ReturnHitTotal, "guard_tripped", c.GuardTripped)
}
