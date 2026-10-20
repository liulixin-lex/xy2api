package sharesched

import (
	"sort"
	"time"
)

// Fixed placement rules (design §1). They equal the defaults of the
// configurable policy they replace.
const (
	// BindingTTL is how long an idle session binding lives; every hit
	// refreshes it. It equals the original sticky TTL.
	BindingTTL = time.Hour
	// MaxWait bounds the wait for a saturated bound account with a warm cache.
	MaxWait = 15 * time.Second
	// MaxWaiting caps the queue of requests waiting for one bound account.
	MaxWaiting = 3
	// StableFor is how long a higher priority must stay healthy before cold
	// sessions return to it.
	StableFor = 120 * time.Second
	// waitPer1kTokens is the wait budget per 1k estimated prompt tokens.
	waitPer1kTokens = 100 * time.Millisecond
)

// Candidate is one group member as evaluated by an adapter with the original
// Sub2API gates. Eligible members can serve this request; the rest carry the
// gate that rejected them.
type Candidate struct {
	AccountID int64
	Platform  string
	// Class groups accounts whose request rewriting is identical (platform and
	// account type). Spill and failover prefer the same class so a switch does
	// not also change the prompt prefix.
	Class string
	// Priority is the account priority: the tier, lower first.
	Priority int
	// Weight is the account's effective load factor; values below 1 count
	// as 1.
	Weight   int
	Eligible bool
	Reason   Reason
	// RecoverAt is when an ineligible account is expected back from a soft
	// penalty; zero when the state is hard or the recovery is unknown.
	RecoverAt time.Time
	// Excluded marks an account that already failed in this request.
	Excluded bool
	// StableSince is when the account last became eligible; zero means it has
	// been eligible for as long as the adapter knows.
	StableSince time.Time
}

// Binding is the session's current account and how long it has been idle.
type Binding struct {
	AccountID int64
	Idle      time.Duration
}

// Request carries the per-request inputs of Plan.
type Request struct {
	GroupID int64
	// Key is the affinity key. Sessionless requests pass a random key and set
	// Sessionless so nothing is bound.
	Key         string
	Sessionless bool
	Now         time.Time
	Binding     Binding
	Cache       CacheProfile
}

// Write is the binding update to apply when a step serves the request.
type Write uint8

const (
	WriteNone Write = iota
	// WriteRefresh extends the existing binding (original sticky behaviour).
	WriteRefresh
	// WriteIfAbsent creates a binding unless another instance already did.
	WriteIfAbsent
	// WriteReplace moves the binding to the serving account.
	WriteReplace
)

// Step is one account in the order the adapter tries.
type Step struct {
	AccountID int64
	Priority  int
}

// Entry is the per-member evaluation, kept for explain.
type Entry struct {
	AccountID  int64  `json:"account_id"`
	Priority   int    `json:"priority"`
	LoadFactor int    `json:"load_factor"`
	Class      string `json:"class,omitempty"`
	Eligible   bool   `json:"eligible"`
	Excluded   bool   `json:"excluded"`
	Reason     Reason `json:"reason,omitempty"`
	Rank       int    `json:"rank"`

	score       float64
	recoverAt   time.Time
	stableSince time.Time
}

func (e *Entry) usable() bool { return e.Eligible && !e.Excluded }

// Decision is the plan for one request. The adapter tries Steps in order and
// reports which one served through Outcome.
type Decision struct {
	Reason      Reason
	Steps       []Step
	Write       Write
	Spill       Write
	SpillReason Reason
	// Wait is the bounded wait for Steps[0] when it is the saturated bound
	// account; MaxWaiting caps the queue for that wait.
	Wait       time.Duration
	MaxWaiting int
	Bound      int64
	Target     int64
	Ideal      int64
	// ActivePriority is the lowest priority value with a usable account;
	// zero when none is usable.
	ActivePriority int
	Entries        []Entry
}

// Outcome returns the reason and binding write when Steps[index] served.
func (d Decision) Outcome(index int) (Reason, Write) {
	if index <= 0 || index >= len(d.Steps) {
		return d.Reason, d.Write
	}
	if d.Reason == ReasonAffinityHit {
		return d.SpillReason, d.Spill
	}
	if d.Steps[index].Priority > d.Steps[0].Priority {
		return ReasonTierOverflowSaturated, d.Spill
	}
	return d.Reason, d.Spill
}

// IdealSkipReason explains why the ideal account did not serve. skipped
// holds accounts the adapter passed over (saturated, session limit).
func (d Decision) IdealSkipReason(served int64, skipped map[int64]Reason) Reason {
	if d.Ideal == 0 || served == d.Ideal {
		return ""
	}
	for i := range d.Entries {
		e := &d.Entries[i]
		if e.AccountID != d.Ideal {
			continue
		}
		switch {
		case !e.Eligible:
			if e.Reason != "" {
				return e.Reason
			}
			return ReasonIneligible
		case e.Excluded:
			if e.Reason == ReasonResilFailureStreak {
				return e.Reason
			}
			return ReasonExcludedInRequest
		}
		if reason, ok := skipped[d.Ideal]; ok {
			return reason
		}
		return d.Reason
	}
	return ReasonNotSchedulable
}

// Plan computes the placement for one request. It is a pure function of its
// inputs: identical inputs give identical decisions on every instance. The
// tier of a candidate is its priority and its weight its load factor.
func Plan(req Request, cands []Candidate) Decision {
	entries := make([]Entry, 0, len(cands))
	present := make(map[int64]struct{}, len(cands))
	for _, c := range cands {
		if _, dup := present[c.AccountID]; dup {
			continue
		}
		present[c.AccountID] = struct{}{}
		e := Entry{
			AccountID: c.AccountID, Priority: c.Priority, LoadFactor: max(c.Weight, 1), Class: c.Class,
			Eligible: c.Eligible, Excluded: c.Excluded, Reason: c.Reason,
			recoverAt: c.RecoverAt, stableSince: c.StableSince,
		}
		switch {
		case e.Excluded && c.Reason == ReasonResilFailureStreak:
			// Keep why the session left the account (gateway resilience).
		case e.Eligible:
			e.Reason = ""
		case e.Reason == "":
			e.Reason = ReasonIneligible
		}
		e.score = hrwWeighted(hrwDraw(req.GroupID, req.Key, e.AccountID), float64(e.LoadFactor))
		entries = append(entries, e)
	}

	d := Decision{Entries: entries, Bound: req.Binding.AccountID}

	// Ideal: highest priority, weights, ignoring health.
	var ideal *Entry
	for i := range entries {
		e := &entries[i]
		if ideal == nil || before(e, ideal) {
			ideal = e
		}
	}
	if ideal != nil {
		d.Ideal = ideal.AccountID
	}

	placement := orderEntries(entries, (*Entry).usable, before)
	if len(placement) > 0 {
		d.Target = placement[0].AccountID
		d.ActivePriority = placement[0].Priority
	}

	spillFrom := func(bound *Entry) []*Entry {
		return orderEntries(entries, func(e *Entry) bool { return e.usable() && e.AccountID != bound.AccountID }, func(a, b *Entry) bool {
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			if sa, sb := a.Class != "" && a.Class == bound.Class, b.Class != "" && b.Class == bound.Class; sa != sb {
				return sa
			}
			return less(a.score, a.AccountID, b.score, b.AccountID)
		})
	}

	finish := func(reason Reason, order []*Entry, write, spill Write) Decision {
		d.Reason = reason
		d.Write, d.Spill = write, spill
		d.Steps = make([]Step, len(order))
		for i, e := range order {
			d.Steps[i] = Step{AccountID: e.AccountID, Priority: e.Priority}
			e.Rank = i + 1
		}
		return d
	}

	if req.Sessionless || req.Key == "" {
		return finish(ReasonSessionless, placement, WriteNone, WriteNone)
	}

	if req.Binding.AccountID <= 0 {
		reason := ReasonNewSession
		if ideal != nil && len(placement) > 0 && placement[0].Priority > ideal.Priority {
			reason = ReasonTierOverflowUnavailable
		}
		return finish(reason, placement, WriteIfAbsent, WriteIfAbsent)
	}

	idle := max(req.Binding.Idle, 0)
	warm := req.Cache.Warm(idle)
	var bound *Entry
	for i := range entries {
		if entries[i].AccountID == req.Binding.AccountID {
			bound = &entries[i]
			break
		}
	}

	switch {
	case bound == nil:
		return finish(ReasonRebindUnavailable, placement, WriteReplace, WriteReplace)
	case bound.Excluded:
		if bound.Reason == ReasonResilFailureStreak {
			// The session kept failing on its account: move it.
			return finish(ReasonResilFailureStreak, spillFrom(bound), WriteReplace, WriteReplace)
		}
		return finish(ReasonFailoverNext, spillFrom(bound), WriteReplace, WriteReplace)
	case !bound.Eligible:
		if softSpill(req, bound, idle) {
			return finish(ReasonSoftSpill, spillFrom(bound), WriteNone, WriteNone)
		}
		return finish(ReasonRebindUnavailable, spillFrom(bound), WriteReplace, WriteReplace)
	}

	if len(placement) > 0 && placement[0].AccountID != bound.AccountID && !warm {
		target := placement[0]
		if bound.Priority > target.Priority {
			// Return to the higher priority once it has been healthy for
			// StableFor; without a known failure it counts as stable.
			if target.stableSince.IsZero() || req.Now.Sub(target.stableSince) >= StableFor {
				return finish(ReasonReturnCold, placement, WriteReplace, WriteReplace)
			}
		} else {
			return finish(ReasonRealignCold, placement, WriteReplace, WriteReplace)
		}
	}

	// Affinity hit: the bound account first, then the session's spill order.
	order := append([]*Entry{bound}, spillFrom(bound)...)
	d.SpillReason = ReasonSpillRebind
	d.Wait = WaitBudget(req.Cache, idle)
	d.MaxWaiting = MaxWaiting
	return finish(ReasonAffinityHit, order, WriteRefresh, WriteReplace)
}

// softSpill reports whether a briefly unavailable bound account should keep
// its binding: its soft penalty ends soon enough that its cache is still warm.
func softSpill(req Request, bound *Entry, idle time.Duration) bool {
	if bound.recoverAt.IsZero() || !req.Cache.Cacheable {
		return false
	}
	remaining := max(bound.recoverAt.Sub(req.Now), 0)
	return float64(remaining+idle) < 0.8*float64(req.Cache.TTL)
}

// before orders entries by priority, then by rendezvous score.
func before(a, b *Entry) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	return less(a.score, a.AccountID, b.score, b.AccountID)
}

func less(scoreA float64, idA int64, scoreB float64, idB int64) bool {
	if scoreA != scoreB {
		return scoreA < scoreB
	}
	return idA < idB
}

func orderEntries(entries []Entry, keep func(*Entry) bool, before func(a, b *Entry) bool) []*Entry {
	out := make([]*Entry, 0, len(entries))
	for i := range entries {
		if keep(&entries[i]) {
			out = append(out, &entries[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return before(out[i], out[j]) })
	return out
}
