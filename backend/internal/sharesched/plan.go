package sharesched

import (
	"sort"
	"strconv"
	"time"
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
	Class       string
	Priority    int
	Concurrency int
	Eligible    bool
	Reason      Reason
	// RecoverAt is when an ineligible account is expected back; zero if
	// unknown or not applicable.
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
	// Correction multiplies new-session weights; missing entries mean 1.
	Correction map[int64]float64
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
	Tier      int
}

// Entry is the per-member evaluation, kept for explain and decision samples.
type Entry struct {
	AccountID       int64   `json:"account_id"`
	Tier            int     `json:"tier"`
	Weight          int     `json:"weight"`
	EffectiveWeight float64 `json:"effective_weight"`
	Configured      bool    `json:"configured"`
	Drain           bool    `json:"drain"`
	CacheDomain     string  `json:"cache_domain"`
	Class           string  `json:"class,omitempty"`
	Eligible        bool    `json:"eligible"`
	Excluded        bool    `json:"excluded"`
	Reason          Reason  `json:"reason,omitempty"`
	Rank            int     `json:"rank"`
	score           float64
	configScore     float64
	recoverAt       time.Time
	stableSince     time.Time
	drainUntil      *time.Time
	present         bool
}

func (e *Entry) usable() bool { return e.present && e.Eligible && !e.Excluded }

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
	ActiveTier int
	Entries    []Entry
}

// Outcome returns the reason and binding write when Steps[index] served.
func (d Decision) Outcome(index int) (Reason, Write) {
	if index <= 0 || index >= len(d.Steps) {
		return d.Reason, d.Write
	}
	if d.Reason == ReasonAffinityHit {
		return d.SpillReason, d.Spill
	}
	if d.Steps[index].Tier > d.Steps[0].Tier {
		return ReasonTierOverflowSaturated, d.Spill
	}
	return d.Reason, d.Spill
}

// IdealSkipReason explains why the configured ideal account did not serve.
// skipped holds accounts the adapter passed over (saturated, session limit).
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
		case !e.present:
			return ReasonNotSchedulable
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

// ResolveRule returns the tier, weight, drain flag and cache domain for a
// member, applying Defaults to members without an explicit rule.
func (p *Policy) ResolveRule(accountID int64, priority, concurrency int) (rule AccountRule, configured bool) {
	return p.Resolver()(accountID, priority, concurrency)
}

// Resolver is ResolveRule for many members of one policy: it indexes the
// rules once instead of scanning them per member.
func (p *Policy) Resolver() func(accountID int64, priority, concurrency int) (rule AccountRule, configured bool) {
	rules := p.ruleIndex()
	return func(accountID int64, priority, concurrency int) (AccountRule, bool) {
		rule, configured := rules[accountID]
		if !configured {
			rule = p.defaultRule(accountID, priority, concurrency)
		}
		if rule.CacheDomain == "" {
			rule.CacheDomain = OwnCacheDomain(accountID)
		}
		return rule, configured
	}
}

func (p *Policy) ruleIndex() map[int64]AccountRule {
	if p.rules != nil {
		return p.rules
	}
	rules := make(map[int64]AccountRule, len(p.Accounts))
	for _, r := range p.Accounts {
		rules[r.AccountID] = r
	}
	return rules
}

// defaultRule is the rule of a member without an explicit one. Its cache
// domain stays empty: the account is a domain of its own.
func (p *Policy) defaultRule(accountID int64, priority, concurrency int) AccountRule {
	weight := 1
	if p.Defaults.Weight == DefaultWeightConcurrency {
		weight = min(max(concurrency, MinWeight), MaxWeight)
	}
	return AccountRule{AccountID: accountID, Tier: priority, Weight: weight}
}

// OwnCacheDomain names the cache domain of an account without a configured
// one. The prefix is reserved so no configured domain can join it.
func OwnCacheDomain(accountID int64) string {
	return ownCacheDomainPrefix + strconv.FormatInt(accountID, 10)
}

const ownCacheDomainPrefix = "account:"

// ExplainedEntries returns the entries with implicit cache domains spelled
// out. Plan leaves them empty to keep the request path free of allocations.
func (d Decision) ExplainedEntries() []Entry {
	out := make([]Entry, len(d.Entries))
	for i, e := range d.Entries {
		if e.CacheDomain == "" {
			e.CacheDomain = OwnCacheDomain(e.AccountID)
		}
		out[i] = e
	}
	return out
}

// Plan computes the placement for one request. It is a pure function of its
// inputs: identical inputs give identical decisions on every instance.
func Plan(p *Policy, req Request, cands []Candidate) Decision {
	rules := p.ruleIndex()
	entries := make([]Entry, 0, len(cands)+len(p.Accounts))
	present := make(map[int64]struct{}, len(cands))
	for _, c := range cands {
		if _, dup := present[c.AccountID]; dup {
			continue
		}
		present[c.AccountID] = struct{}{}
		rule, configured := rules[c.AccountID]
		if !configured {
			rule = p.defaultRule(c.AccountID, c.Priority, c.Concurrency)
		}
		factor := 1.0
		if f, ok := req.Correction[c.AccountID]; ok && f > 0 {
			factor = f
		}
		e := Entry{
			AccountID: c.AccountID, Tier: rule.Tier, Weight: rule.Weight,
			EffectiveWeight: float64(rule.Weight) * factor, Configured: configured,
			Drain: rule.Drain, CacheDomain: rule.CacheDomain, Class: c.Class,
			Eligible: c.Eligible, Excluded: c.Excluded, Reason: c.Reason,
			recoverAt: c.RecoverAt, stableSince: c.StableSince, drainUntil: rule.DrainUntil, present: true,
		}
		switch {
		case e.Excluded && c.Reason == ReasonResilFailureStreak:
			// Keep why the session left the account (gateway resilience).
		case e.Eligible:
			e.Reason = ""
		case e.Reason == "":
			e.Reason = ReasonIneligible
		}
		entries = append(entries, e)
	}
	// Configured accounts missing from the candidate list are not schedulable
	// right now; they still define the ideal account for attribution.
	for _, r := range p.Accounts {
		if _, ok := present[r.AccountID]; ok {
			continue
		}
		entries = append(entries, Entry{AccountID: r.AccountID, Tier: r.Tier, Weight: r.Weight,
			EffectiveWeight: float64(r.Weight), Configured: true, Drain: r.Drain,
			CacheDomain: r.CacheDomain, Reason: ReasonNotSchedulable, drainUntil: r.DrainUntil})
	}

	for i := range entries {
		e := &entries[i]
		weight, configWeight := e.EffectiveWeight, float64(e.Weight)
		if e.Drain {
			weight, configWeight = 0, 0
		}
		draw := hrwDraw(req.GroupID, req.Key, e.AccountID)
		e.score, e.configScore = hrwWeighted(draw, weight), hrwWeighted(draw, configWeight)
	}

	d := Decision{Entries: entries, Bound: req.Binding.AccountID}

	// Ideal: highest configured tier, configured weights, ignoring health.
	var ideal *Entry
	for i := range entries {
		e := &entries[i]
		if e.Drain {
			continue
		}
		if ideal == nil || e.Tier < ideal.Tier || (e.Tier == ideal.Tier && less(e.configScore, e.AccountID, ideal.configScore, ideal.AccountID)) {
			ideal = e
		}
	}
	if ideal != nil {
		d.Ideal = ideal.AccountID
	}

	// Active tier: highest tier with a usable open account. baseTier ignores
	// request exclusions so on_failed=stop can refuse to fall further.
	activeTier, hasOpen := 0, false
	baseTier, hasBase := 0, false
	for i := range entries {
		e := &entries[i]
		if e.Drain || !e.present || !e.Eligible {
			continue
		}
		if !hasBase || e.Tier < baseTier {
			baseTier, hasBase = e.Tier, true
		}
		if !e.Excluded && (!hasOpen || e.Tier < activeTier) {
			activeTier, hasOpen = e.Tier, true
		}
	}
	d.ActiveTier = activeTier

	admit := func(e *Entry) bool {
		if !e.usable() {
			return false
		}
		if hasOpen && e.Drain {
			return false
		}
		if p.Tiers.OnFailed == OnFailedStop && hasBase && e.Tier > baseTier {
			return false
		}
		if p.Tiers.OnSaturated == OnSaturatedWait && hasOpen && e.Tier != activeTier {
			return false
		}
		return true
	}
	placement := orderEntries(entries, func(e *Entry) bool { return admit(e) }, func(a, b *Entry) bool {
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		return less(a.score, a.AccountID, b.score, b.AccountID)
	})
	if len(placement) > 0 {
		d.Target = placement[0].AccountID
	}

	spillFrom := func(bound *Entry) []*Entry {
		return orderEntries(entries, func(e *Entry) bool { return admit(e) && (bound == nil || e.AccountID != bound.AccountID) }, func(a, b *Entry) bool {
			if a.Tier != b.Tier {
				return a.Tier < b.Tier
			}
			if pa, pb := partition(bound, a), partition(bound, b); pa != pb {
				return pa < pb
			}
			return less(a.score, a.AccountID, b.score, b.AccountID)
		})
	}

	finish := func(reason Reason, order []*Entry, write, spill Write) Decision {
		d.Reason = reason
		d.Write, d.Spill = write, spill
		d.Steps = make([]Step, len(order))
		for i, e := range order {
			d.Steps[i] = Step{AccountID: e.AccountID, Tier: e.Tier}
		}
		for i, e := range order {
			e.Rank = i + 1
		}
		return d
	}

	if req.Sessionless || req.Key == "" {
		return finish(ReasonSessionless, placement, WriteNone, WriteNone)
	}

	newSessionReason := func() Reason {
		if ideal != nil && len(placement) > 0 && placement[0].Tier > ideal.Tier {
			return ReasonTierOverflowUnavailable
		}
		return ReasonNewSession
	}
	if req.Binding.AccountID <= 0 {
		return finish(newSessionReason(), placement, WriteIfAbsent, WriteIfAbsent)
	}

	idle := max(req.Binding.Idle, 0)
	warm := req.Cache.Warm(idle)
	var bound *Entry
	for i := range entries {
		if entries[i].AccountID == req.Binding.AccountID && entries[i].present {
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
		if softSpill(p, req, bound, idle) {
			return finish(ReasonSoftSpill, spillFrom(bound), WriteNone, WriteNone)
		}
		return finish(ReasonRebindUnavailable, spillFrom(bound), WriteReplace, WriteReplace)
	}

	now := req.Now
	if bound.Drain {
		if bound.drainUntil != nil && !now.Before(bound.drainUntil.Add(deadlineStagger(req.Key))) {
			return finish(ReasonDrainDeadline, placement, WriteReplace, WriteReplace)
		}
		if !warm && hasOpen {
			return finish(ReasonDrainCold, placement, WriteReplace, WriteReplace)
		}
	} else if len(placement) > 0 && placement[0].AccountID != bound.AccountID {
		target := placement[0]
		if bound.Tier > target.Tier {
			stableFor := time.Duration(1<<62 - 1)
			if !target.stableSince.IsZero() {
				stableFor = now.Sub(target.stableSince)
			}
			stable := stableFor >= time.Duration(p.Tiers.StableSeconds)*time.Second
			// The deadline counts from the target's recovery. Without a known
			// recovery (the higher tier was only saturated, or this instance
			// never saw it fail) there is nothing to count from: a warm
			// session must not be moved at once.
			deadlinePassed := p.Tiers.ReturnDeadlineSeconds > 0 && !target.stableSince.IsZero() &&
				stableFor >= time.Duration(p.Tiers.ReturnDeadlineSeconds)*time.Second+deadlineStagger(req.Key)
			switch {
			case p.Tiers.Return == ReturnSessionEnd:
			case p.Tiers.Return == ReturnImmediate && stable:
				return finish(ReasonReturnImmediate, placement, WriteReplace, WriteReplace)
			case deadlinePassed:
				return finish(ReasonReturnDeadline, placement, WriteReplace, WriteReplace)
			case p.Tiers.Return == ReturnCold && stable && !warm:
				return finish(ReasonReturnCold, placement, WriteReplace, WriteReplace)
			}
		} else if !warm && p.Cache.Realign == RealignCold {
			return finish(ReasonRealignCold, placement, WriteReplace, WriteReplace)
		}
	}

	// Affinity hit: the bound account first, then the session's spill order.
	order := append([]*Entry{bound}, spillFrom(bound)...)
	spillWrite, spillReason := WriteReplace, ReasonSpillRebind
	if p.Affinity.OnSpill == OnSpillKeep {
		spillWrite, spillReason = WriteNone, ReasonSpillKeep
	}
	d.SpillReason = spillReason
	if p.Affinity.Wait.MaxWaiting > 0 {
		d.Wait = WaitBudget(p.Affinity.Wait, req.Cache, idle)
		d.MaxWaiting = p.Affinity.Wait.MaxWaiting
	}
	return finish(ReasonAffinityHit, order, WriteRefresh, spillWrite)
}

// softSpill reports whether a briefly unavailable bound account should keep
// its binding: it is expected back soon enough that its cache is still warm.
func softSpill(p *Policy, req Request, bound *Entry, idle time.Duration) bool {
	if bound.recoverAt.IsZero() || p.Affinity.SoftSpillMaxSeconds <= 0 || !req.Cache.Cacheable {
		return false
	}
	remaining := max(bound.recoverAt.Sub(req.Now), 0)
	if remaining > time.Duration(p.Affinity.SoftSpillMaxSeconds)*time.Second {
		return false
	}
	return float64(remaining+idle) < 0.8*float64(req.Cache.TTL)
}

// partition orders spill candidates: same cache domain, then same class. An
// empty domain is the account's own, shared with no other account.
func partition(bound, e *Entry) int {
	switch {
	case bound == nil:
		return 2
	case e.CacheDomain != "" && e.CacheDomain == bound.CacheDomain:
		return 0
	case e.Class != "" && e.Class == bound.Class:
		return 1
	default:
		return 2
	}
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
