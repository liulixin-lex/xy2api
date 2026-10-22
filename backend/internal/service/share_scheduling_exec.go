package service

import (
	"context"
	"log/slog"
	"maps"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

const (
	shareWaitInitialBackoff = 20 * time.Millisecond
	shareWaitMaxBackoff     = 250 * time.Millisecond
)

// shareSelectionTrace follows one selection call. The share executor fills
// in its decision; the selection result records the served account for
// cache attribution.
type shareSelectionTrace struct {
	groupID    int64
	sessionKey string
	switched   bool
	reason     sharesched.Reason
	// sampled selections feed cache attribution.
	sampled bool
}

type shareSelectionTraceKey struct{}

// withSelectionTrace starts the trace of one selection call.
func (s *ShareSchedulingService) withSelectionTrace(ctx context.Context, groupID int64, sessionKey string, switched bool) (context.Context, *shareSelectionTrace) {
	if s == nil {
		return ctx, nil
	}
	trace := &shareSelectionTrace{groupID: groupID, sessionKey: sessionKey, switched: switched,
		sampled: shareAttributionSampled(sessionKey, s.attributionRate)}
	return context.WithValue(ctx, shareSelectionTraceKey{}, trace), trace
}

func shareSelectionTraceFrom(ctx context.Context) *shareSelectionTrace {
	trace, _ := ctx.Value(shareSelectionTraceKey{}).(*shareSelectionTrace)
	return trace
}

// noteResilienceReason counts a gateway-resilience placement (a last-resort
// pick) with the group's share decisions, so the share report shows it.
func (s *ShareSchedulingService) noteResilienceReason(groupID int64, reason sharesched.Reason) {
	if s == nil || !s.shareSchedulingActive(groupID) {
		return
	}
	s.stats.add(shareStatsKindShare, groupID, time.Now(), shareStatsReasonPrefix+string(reason), 1)
}

// recordSelection runs for every selection result of the hooked schedulers.
func (s *ShareSchedulingService) recordSelection(ctx context.Context, account *Account) {
	if s == nil || account == nil {
		return
	}
	trace := shareSelectionTraceFrom(ctx)
	if trace == nil {
		return
	}
	if trace.sampled {
		requestID, _ := ctx.Value(ctxkey.RequestID).(string)
		s.traces.put(requestID, shareTrace{groupID: trace.groupID, sessionKey: trace.sessionKey, accountID: account.ID,
			platform: account.Platform, reason: trace.reason, switched: trace.switched, at: time.Now()})
	}
}

// newRequest builds the kernel request of one selection.
func (s *ShareSchedulingService) newRequest(ctx context.Context, gp *shareGroupPolicy, platform, model, sessionHash string, binding sharesched.Binding) (sharesched.Request, sharesched.KeySource) {
	key, source := shareAffinityKey(&gp.policy, sessionHash)
	req := sharesched.Request{GroupID: gp.groupID, Key: key, Now: time.Now(), Binding: binding,
		Cache: shareCacheProfile(ctx, platform, model)}
	if key == "" {
		req.Key, req.Sessionless, req.Binding = shareSessionlessKey(), true, sharesched.Binding{}
	}
	return req, source
}

// shareCandidate is the candidate of an account with its share weight in
// the group; the adapters fill in eligibility.
func (s *ShareSchedulingService) shareCandidate(groupID int64, acc *Account, now time.Time) sharesched.Candidate {
	w := s.shareWeight(groupID, acc, now)
	return sharesched.Candidate{AccountID: acc.ID, Platform: acc.Platform, Class: shareAccountClass(acc),
		Priority: acc.Priority, LoadFactor: w.LoadFactor, Weight: w.Effective}
}

// shareMissingBoundCandidate turns a bound account that the schedulable
// listing left out into an ineligible candidate. Listings drop rate-limited,
// overloaded and temporarily unschedulable accounts; when gateway resilience
// rates the state a soft penalty, the account can keep its binding through
// the short outage (soft spill). Anything else is treated as gone.
func (s *ShareSchedulingService) shareMissingBoundCandidate(groupID int64, acc *Account, model string, now time.Time, penalty resiliencePenaltyFunc) (sharesched.Candidate, bool) {
	if acc == nil {
		return sharesched.Candidate{}, false
	}
	reason, end := shareAccountStateReason(acc, now)
	recoverAt := shareSoftRecovery(acc, model, now, penalty, end)
	if recoverAt.IsZero() {
		return sharesched.Candidate{}, false
	}
	s.observeEligibility(acc.ID, false, reason, now)
	c := s.shareCandidate(groupID, acc, now)
	c.Reason, c.RecoverAt = reason, recoverAt
	return c, true
}

// resiliencePenaltyFunc rates an account's restrictions as gateway
// resilience does (resiliencePenaltyOf, or the OpenAI variant).
type resiliencePenaltyFunc func(*Account, string, time.Time) (resilience.PenaltyTier, time.Time, string)

// shareLegacySoftSpillMax is the soft-spill bound of 0.3.0 (its default
// policy value), kept while gateway resilience is off.
const shareLegacySoftSpillMax = 300 * time.Second

// shareSoftRecovery is when a soft-penalised account is expected back, or
// zero when its state is hard or the recovery is unknown. Share scheduling
// keeps a session's binding through a short outage only on the strength of
// this time, so it agrees with gateway resilience on soft and hard states
// (design §4): a temporary state whose reason is empty or not a soft prefix
// is hard, and so is anything that lasts beyond the hard rate-limit
// threshold.
//
// stateEnd is when the state that rejected the account ends, as the
// adapter read it. With the resilience master switch off the 0.3.0 rule
// applies: any state that ends within shareLegacySoftSpillMax.
func shareSoftRecovery(acc *Account, model string, now time.Time, penalty resiliencePenaltyFunc, stateEnd time.Time) time.Time {
	if !resilience.Current().FailureOn() {
		if stateEnd.IsZero() || stateEnd.Sub(now) > shareLegacySoftSpillMax {
			return time.Time{}
		}
		return stateEnd
	}
	if penalty == nil {
		penalty = resiliencePenaltyOf
	}
	tier, recoverAt, _ := penalty(acc, model, now)
	if tier != resilience.TierSoft || recoverAt.IsZero() || recoverAt.Sub(now) > resilience.HardRateLimitThreshold {
		return time.Time{}
	}
	return recoverAt
}

// shareClassNames interns account class names; building one per candidate
// per request would allocate for every member of a large group. The map is
// replaced on insert, so lookups need no lock.
var shareClassNames atomic.Pointer[map[[2]string]string]

const maxShareClassNames = 256

// shareAccountClass groups accounts whose request rewriting is identical.
func shareAccountClass(acc *Account) string {
	key := [2]string{acc.Platform, acc.Type}
	current := shareClassNames.Load()
	if current != nil {
		if name, ok := (*current)[key]; ok {
			return name
		}
	}
	name := acc.Platform + "/" + acc.Type
	for current == nil || len(*current) < maxShareClassNames {
		next := make(map[[2]string]string, 1)
		if current != nil {
			next = maps.Clone(*current)
		}
		next[key] = name
		if shareClassNames.CompareAndSwap(current, &next) {
			break
		}
		current = shareClassNames.Load()
	}
	return name
}

// shareModelLimitEnd is when the account's rate limit for the model ends;
// zero when it is not limited.
func shareModelLimitEnd(ctx context.Context, acc *Account, model string, now time.Time) time.Time {
	if remaining := acc.GetRateLimitRemainingTimeWithContext(ctx, model); remaining > 0 {
		return now.Add(remaining)
	}
	return time.Time{}
}

// shareAccountStateReason maps Account.IsSchedulable onto reason codes and
// the expected recovery time of temporary states.
func shareAccountStateReason(a *Account, now time.Time) (sharesched.Reason, time.Time) {
	switch {
	case a.Status == StatusDisabled:
		return sharesched.ReasonDisabled, time.Time{}
	case !a.IsActive():
		return sharesched.ReasonInactive, time.Time{}
	case !a.Schedulable:
		return sharesched.ReasonNotSchedulable, time.Time{}
	case a.IQCheck.BlocksScheduling():
		return sharesched.ReasonIQDegraded, time.Time{}
	case a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt):
		return sharesched.ReasonExpired, time.Time{}
	case a.OverloadUntil != nil && now.Before(*a.OverloadUntil):
		return sharesched.ReasonOverloaded, *a.OverloadUntil
	case a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt):
		return sharesched.ReasonRateLimited, *a.RateLimitResetAt
	case a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil):
		if resilience.Current().FailureOn() && resilience.IsBalancePause(a.TempUnschedulableReason) {
			// Out of balance at the relay is a hard state: the session moves
			// at once instead of waiting out the pause. With resilience off
			// it is an ordinary pause, as in 0.3.0.
			return sharesched.ReasonBalanceExhausted, time.Time{}
		}
		return sharesched.ReasonTempUnschedulable, *a.TempUnschedulableUntil
	case a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded():
		return sharesched.ReasonQuotaExceeded, time.Time{}
	}
	return sharesched.ReasonNotSchedulable, time.Time{}
}

// shareOutcome is how a plan was served.
type shareOutcome struct {
	result   *AccountSelectionResult
	err      error
	served   int64
	reason   sharesched.Reason
	write    sharesched.Write
	attempts int
	waited   time.Duration
	waitHit  bool
	skipped  map[int64]sharesched.Reason
}

// shareExecutor tries a plan's steps with the platform's own slot, session
// and freshness gates.
type shareExecutor struct {
	ctx         context.Context
	gp          *shareGroupPolicy
	cache       GatewayCache
	concurrency *ConcurrencyService
	cfg         config.GatewaySchedulingConfig
	// key is the binding cache key; empty for sessionless requests.
	key      string
	accounts map[int64]*Account
	// limit is the account's slot limit.
	limit func(acc *Account) int
	// verify, when set, re-reads an account before it serves and returns the
	// account to serve, or a skip reason.
	verify func(acc *Account) (*Account, sharesched.Reason)
	// admit applies per-session limits once a slot is held.
	admit     func(acc *Account) bool
	newResult func(acc *Account, acquired bool, release func(), wait *AccountWaitPlan) (*AccountSelectionResult, error)
	// createOnRefresh marks a binding read from a legacy key: refreshing the
	// primary key would not create it, so a hit creates it instead.
	createOnRefresh bool
	// reason, when set, replaces the plan's reason in the selection trace
	// (a last-resort placement).
	reason   sharesched.Reason
	skipped  map[int64]sharesched.Reason
	attempts int
	// loadGate skips new placements and spills on accounts whose load rate
	// is 100 or more, as the original load-aware selection does (design §5).
	// The bound account of an affinity hit is judged by its slots only.
	loadGate bool
	// loadOrder lists the accounts whose load is read, in listing order: a
	// stable list lets the load cache of the concurrency service serve
	// repeated reads, as it does for the original selection.
	loadOrder []int64
	loads     map[int64]*AccountLoadInfo
	loadsRead bool
}

// shareLoadOrder is the load read list of a selection: its usable
// candidates in listing order.
func shareLoadOrder(cands []sharesched.Candidate) []int64 {
	out := make([]int64, 0, len(cands))
	for _, c := range cands {
		if c.Eligible && !c.Excluded {
			out = append(out, c.AccountID)
		}
	}
	return out
}

func (e *shareExecutor) skip(id int64, reason sharesched.Reason) {
	if e.skipped == nil {
		e.skipped = map[int64]sharesched.Reason{}
	}
	if _, ok := e.skipped[id]; !ok {
		e.skipped[id] = reason
	}
}

func (e *shareExecutor) tryAcquire(acc *Account, limit int) *AcquireResult {
	return e.tryAcquireWithin(e.ctx, acc, limit)
}

func (e *shareExecutor) tryAcquireWithin(ctx context.Context, acc *Account, limit int) *AcquireResult {
	if e.concurrency == nil {
		return &AcquireResult{Acquired: true, ReleaseFunc: func() {}}
	}
	res, err := e.concurrency.AcquireAccountSlot(ctx, acc.ID, limit)
	if err != nil || res == nil || !res.Acquired {
		return nil
	}
	return res
}

// saturated reports whether acc's load rate, (in flight + waiting) × 100 ÷
// effective load factor, is 100 or more. Loads are read once per selection;
// a failed read counts as not saturated, as the original selection falls
// back to its plain order.
func (e *shareExecutor) saturated(d sharesched.Decision, acc *Account) bool {
	if !e.loadGate || e.concurrency == nil {
		return false
	}
	if !e.loadsRead {
		e.loadsRead = true
		order := e.loadOrder
		if order == nil {
			order = make([]int64, 0, len(d.Steps))
			for _, step := range d.Steps {
				order = append(order, step.AccountID)
			}
		}
		accounts := make([]AccountWithConcurrency, 0, len(order))
		for _, id := range order {
			if a := e.accounts[id]; a != nil {
				accounts = append(accounts, AccountWithConcurrency{ID: a.ID, MaxConcurrency: a.EffectiveLoadFactor()})
			}
		}
		if loads, err := e.concurrency.GetAccountsLoadBatch(e.ctx, accounts); err == nil {
			e.loads = loads
		}
	}
	info := e.loads[acc.ID]
	return info != nil && info.LoadRate >= 100
}

// acquire walks the steps in order. Only the bound account of an affinity
// hit is waited for, within the plan's budget; everything else spills.
func (e *shareExecutor) acquire(d sharesched.Decision) *shareOutcome {
	for i, step := range d.Steps {
		acc := e.accounts[step.AccountID]
		if acc == nil {
			continue
		}
		boundHit := i == 0 && d.Reason == sharesched.ReasonAffinityHit
		if !boundHit && e.saturated(d, acc) {
			e.skip(acc.ID, sharesched.ReasonSaturated)
			continue
		}
		e.attempts++
		limit := e.limit(acc)
		res := e.tryAcquire(acc, limit)
		var waited time.Duration
		if res == nil && boundHit && d.Wait > 0 {
			res, waited = e.waitForSlot(acc, limit, d.Wait, d.MaxWaiting)
		}
		if res == nil {
			if e.ctx.Err() != nil {
				// The client is gone: further accounts would only hand out
				// slots, and none of them was actually saturated.
				return nil
			}
			e.skip(acc.ID, e.busyReason(acc, limit))
			continue
		}
		serving := acc
		if e.verify != nil {
			fresh, reason := e.verify(acc)
			if fresh == nil {
				res.ReleaseFunc()
				e.skip(acc.ID, reason)
				continue
			}
			if fresh.Concurrency != acc.Concurrency {
				res.ReleaseFunc()
				if limit = e.limit(fresh); limit > 0 {
					res = e.tryAcquire(fresh, limit)
				}
				if res == nil {
					e.skip(acc.ID, e.busyReason(fresh, limit))
					continue
				}
			}
			serving = fresh
		}
		if !e.admit(serving) {
			res.ReleaseFunc()
			e.skip(acc.ID, sharesched.ReasonSessionLimit)
			continue
		}
		reason, write := d.Outcome(i)
		if waited > 0 {
			reason = sharesched.ReasonAffinityWait
		}
		out := e.finish(serving, true, res.ReleaseFunc, nil, reason, write)
		out.waited, out.waitHit = waited, waited > 0
		return out
	}
	return nil
}

// fallbackWait is the original last layer: queue on the first step that
// passes the session limit, preferring the bound account's warm cache.
func (e *shareExecutor) fallbackWait(d sharesched.Decision) *shareOutcome {
	for i, step := range d.Steps {
		serving := e.accounts[step.AccountID]
		if serving == nil {
			continue
		}
		if e.verify != nil {
			if serving, _ = e.verify(serving); serving == nil {
				continue
			}
		}
		if !e.admit(serving) {
			continue
		}
		reason, write := d.Outcome(i)
		return e.finish(serving, false, nil, &AccountWaitPlan{AccountID: serving.ID, MaxConcurrency: e.limit(serving),
			Timeout: e.cfg.FallbackWaitTimeout, MaxWaiting: e.cfg.FallbackMaxWaiting}, reason, write)
	}
	return nil
}

// busyReason names why a slot could not be taken: an adaptive limit below
// the configured concurrency is upstream pressure, not ordinary load.
func (e *shareExecutor) busyReason(acc *Account, limit int) sharesched.Reason {
	if limit > 0 && limit < acc.Concurrency {
		return sharesched.ReasonCodexPressure
	}
	return sharesched.ReasonSaturated
}

// finish applies or defers the binding write and builds the result. Writes
// are deferred while the request may still be rejected: when it has to wait
// for a slot, or when the profit gate makes the final check in the handler.
func (e *shareExecutor) finish(acc *Account, acquired bool, release func(), wait *AccountWaitPlan, reason sharesched.Reason, write sharesched.Write) *shareOutcome {
	if e.createOnRefresh && write == sharesched.WriteRefresh {
		write = sharesched.WriteIfAbsent
	}
	if e.reason != "" {
		reason = e.reason
	}
	var intent *shareBindingIntent
	if e.key != "" {
		intent = &shareBindingIntent{cache: e.cache, groupID: e.gp.groupID, key: e.key, write: write, ttl: sharesched.BindingTTL}
		if acquired && !gatewayProfitControlGateActive(e.ctx) {
			intent.done = true
			if err := applyShareBindingWrite(e.ctx, e.cache, e.gp.groupID, e.key, acc.ID, write, sharesched.BindingTTL); err != nil {
				slog.Debug("share_scheduling.binding_write_failed", "group_id", e.gp.groupID, "account_id", acc.ID, "error", err)
			}
		}
	}
	if trace := shareSelectionTraceFrom(e.ctx); trace != nil {
		trace.reason = reason
	}
	result, err := e.newResult(acc, acquired, release, wait)
	if err != nil && release != nil {
		release()
	}
	if result != nil {
		result.shareBinding = intent
	}
	return &shareOutcome{result: result, err: err, served: acc.ID, reason: reason, write: write, attempts: e.attempts, skipped: e.skipped}
}

// waitForSlot waits up to budget for the bound account, counted in the
// account's wait queue so max_waiting holds across instances.
func (e *shareExecutor) waitForSlot(acc *Account, limit int, budget time.Duration, maxWaiting int) (*AcquireResult, time.Duration) {
	// The wait counter fails open on Redis errors: with a cancelled context
	// the increment fails yet reports success, and the deferred decrement
	// would then lower another waiter's count.
	if e.concurrency == nil || budget <= 0 || e.ctx.Err() != nil {
		return nil, 0
	}
	start := time.Now()
	if ok, err := e.concurrency.IncrementAccountWaitCount(e.ctx, acc.ID, maxWaiting); err != nil || !ok {
		return nil, 0
	}
	defer e.concurrency.DecrementAccountWaitCount(e.ctx, acc.ID)
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	// A slow slot call must not stretch the wait past its budget.
	waitCtx, cancel := context.WithDeadline(e.ctx, start.Add(budget))
	defer cancel()
	backoff := shareWaitInitialBackoff
	for {
		poll := time.NewTimer(backoff)
		select {
		case <-e.ctx.Done():
			poll.Stop()
			return nil, time.Since(start)
		case <-deadline.C:
			poll.Stop()
			return nil, time.Since(start)
		case <-poll.C:
		}
		if res := e.tryAcquireWithin(waitCtx, acc, limit); res != nil {
			return res, time.Since(start)
		}
		backoff = min(backoff*3/2, shareWaitMaxBackoff)
	}
}

// run executes a plan: model routing first when configured, then the whole
// group, then the original fallback wait.
func (e *shareExecutor) run(rec *shareDecisionRecord, req sharesched.Request, cands []sharesched.Candidate, routing []int64) (*shareOutcome, sharesched.Decision) {
	if len(routing) > 0 {
		allowed := make(map[int64]struct{}, len(routing))
		for _, id := range routing {
			allowed[id] = struct{}{}
		}
		routed := make([]sharesched.Candidate, 0, len(routing))
		for _, c := range cands {
			if _, ok := allowed[c.AccountID]; ok {
				routed = append(routed, c)
			}
		}
		decision := sharesched.Plan(req, routed)
		if out := e.acquire(decision); out != nil {
			rec.routing = sharesched.ReasonModelRouting
			return out, decision
		}
		rec.routing = sharesched.ReasonModelRoutingFallback
	}
	decision := sharesched.Plan(req, cands)
	return e.serve(decision), decision
}

// serve tries a decision's steps and, when none has a free slot, queues on
// the first one that admits the request.
func (e *shareExecutor) serve(d sharesched.Decision) *shareOutcome {
	if out := e.acquire(d); out != nil {
		return out
	}
	if out := e.monitorProbeAcquire(d); out != nil {
		return out
	}
	return e.fallbackWait(d)
}

// readBinding reads the session binding of a share group. A failed read
// places the request as new; SET NX keeps any binding that still exists.
func (s *ShareSchedulingService) readBinding(ctx context.Context, cache GatewayCache, gp *shareGroupPolicy, key string) sharesched.Binding {
	if key == "" {
		return sharesched.Binding{}
	}
	binding, err := readShareBinding(ctx, cache, gp.groupID, key, sharesched.BindingTTL)
	if err != nil {
		slog.Debug("share_scheduling.binding_read_failed", "group_id", gp.groupID, "error", err)
		return sharesched.Binding{}
	}
	return binding
}
