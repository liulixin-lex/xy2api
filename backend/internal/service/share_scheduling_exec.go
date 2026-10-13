package service

import (
	"context"
	"log/slog"
	"maps"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/servertiming"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

const (
	shareWaitInitialBackoff = 20 * time.Millisecond
	shareWaitMaxBackoff     = 250 * time.Millisecond
	shareShadowTimeout      = 3 * time.Second
	shareShadowConcurrency  = 32
)

// shareSelectionTrace follows one selection call. The share executor fills
// in its decision; the selection result records the served account for
// cache attribution and starts the shadow comparison.
type shareSelectionTrace struct {
	groupID    int64
	sessionKey string
	switched   bool
	reason     sharesched.Reason
	// shadow runs the share plan off the request path once the original
	// scheduler has chosen; nil outside shadow groups.
	shadow     func(original int64)
	shadowDone bool
	// sampled selections feed cache attribution and the selection cost;
	// meter, when the timing hooks are installed, counts their Redis and
	// database calls up to the selection result.
	sampled  bool
	costDone bool
	meter    *servertiming.Collector
	start    time.Time
}

type shareSelectionTraceKey struct{}

// withSelectionTrace starts the trace of one selection call.
func (s *ShareSchedulingService) withSelectionTrace(ctx context.Context, groupID int64, sessionKey string, switched bool) (context.Context, *shareSelectionTrace) {
	if s == nil {
		return ctx, nil
	}
	trace := &shareSelectionTrace{groupID: groupID, sessionKey: sessionKey, switched: switched, start: time.Now()}
	if settings := s.Settings(); settings.CacheAttribution && shareAttributionSampled(sessionKey, settings.AttributionSampleRate) {
		trace.sampled = true
		if s.meterIO && !servertiming.Active(ctx) {
			trace.meter = servertiming.New(trace.start)
			ctx = servertiming.WithCollector(ctx, trace.meter)
		}
	}
	return context.WithValue(ctx, shareSelectionTraceKey{}, trace), trace
}

func shareSelectionTraceFrom(ctx context.Context) *shareSelectionTrace {
	trace, _ := ctx.Value(shareSelectionTraceKey{}).(*shareSelectionTrace)
	return trace
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
	if trace.sampled && !trace.costDone {
		trace.costDone = true
		s.recordSelectionCost(trace)
	}
	if trace.shadow != nil && !trace.shadowDone {
		trace.shadowDone = true
		s.startShadow(trace.groupID, trace.shadow, account.ID)
	}
}

// startShadow runs a shadow comparison off the request path, or counts it as
// skipped when shareShadowConcurrency comparisons are already running.
func (s *ShareSchedulingService) startShadow(groupID int64, run func(original int64), original int64) {
	select {
	case s.shadows <- struct{}{}:
	default:
		s.stats.add(shareStatsKindShare, groupID, time.Now(), shareStatsShadowSkippedField, 1)
		return
	}
	go func() {
		defer func() { <-s.shadows }()
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("share_scheduling.shadow_panic", "group_id", groupID, "panic", r)
			}
		}()
		run(original)
	}()
}

// recordSelectionCost adds a sampled selection's time, and when metered its
// Redis round trips and database queries, to the counters of its scheduler:
// share, shadow (the original choice plus the shadow binding read) or
// original.
func (s *ShareSchedulingService) recordSelectionCost(trace *shareSelectionTrace) {
	mode := shareTTFTOriginal
	switch {
	case trace.reason != "":
		mode = string(sharesched.SchedulerShare)
	case trace.shadow != nil:
		mode = string(sharesched.SchedulerShadow)
	}
	now := time.Now()
	prefix := shareStatsSelectionPrefix + mode + ":"
	gid := trace.groupID
	s.stats.add(shareStatsKindCache, gid, now, prefix+shareSelectionSamples, 1)
	s.stats.add(shareStatsKindCache, gid, now, prefix+shareSelectionMicros, now.Sub(trace.start).Microseconds()+1)
	if trace.meter == nil {
		return
	}
	redis := trace.meter.Summary(servertiming.MetricRedis)
	db := trace.meter.Summary(servertiming.MetricDatabase)
	s.stats.add(shareStatsKindCache, gid, now, prefix+shareSelectionIOSamples, 1)
	s.stats.add(shareStatsKindCache, gid, now, prefix+shareSelectionRedisTrips, int64(redis.RoundTrips))
	s.stats.add(shareStatsKindCache, gid, now, prefix+shareSelectionRedisMicros, redis.Busy.Microseconds())
	s.stats.add(shareStatsKindCache, gid, now, prefix+shareSelectionDBQueries, db.Count)
	trace.meter = nil
}

// newRequest builds the kernel request of one selection.
func (s *ShareSchedulingService) newRequest(ctx context.Context, gp *shareGroupPolicy, platform, model, sessionHash string, binding sharesched.Binding) (sharesched.Request, sharesched.KeySource) {
	key, source := shareAffinityKey(&gp.policy, sessionHash)
	req := sharesched.Request{GroupID: gp.groupID, Key: key, Now: time.Now(), Binding: binding,
		Cache: shareCacheProfile(ctx, &gp.policy, platform, model), Correction: s.correctionsFor(gp.groupID)}
	if key == "" {
		req.Key, req.Sessionless, req.Binding = shareSessionlessKey(), true, sharesched.Binding{}
	}
	return req, source
}

// shareMissingBoundCandidate turns a bound account that the schedulable
// listing left out into an ineligible candidate. Listings drop rate-limited,
// overloaded and temporarily unschedulable accounts; with its recovery time
// such an account can keep its binding through a short outage (soft spill).
// Anything else is treated as gone.
func (s *ShareSchedulingService) shareMissingBoundCandidate(acc *Account, now time.Time) (sharesched.Candidate, bool) {
	if acc == nil {
		return sharesched.Candidate{}, false
	}
	reason, recoverAt := shareAccountStateReason(acc, now)
	if recoverAt.IsZero() {
		return sharesched.Candidate{}, false
	}
	s.observeEligibility(acc.ID, false, reason, now)
	return sharesched.Candidate{AccountID: acc.ID, Platform: acc.Platform, Class: shareAccountClass(acc),
		Priority: acc.Priority, Concurrency: acc.Concurrency, Reason: reason, RecoverAt: recoverAt}, true
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
	ttl      time.Duration
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
	skipped         map[int64]sharesched.Reason
	attempts        int
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

// acquire walks the steps in order. Only the bound account of an affinity
// hit is waited for, within the plan's budget; everything else spills.
func (e *shareExecutor) acquire(d sharesched.Decision) *shareOutcome {
	for i, step := range d.Steps {
		acc := e.accounts[step.AccountID]
		if acc == nil {
			continue
		}
		e.attempts++
		limit := e.limit(acc)
		res := e.tryAcquire(acc, limit)
		var waited time.Duration
		if res == nil && i == 0 && d.Wait > 0 && d.Reason == sharesched.ReasonAffinityHit {
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
	var intent *shareBindingIntent
	if e.key != "" {
		intent = &shareBindingIntent{cache: e.cache, groupID: e.gp.groupID, key: e.key, write: write, ttl: e.ttl}
		if acquired && !gatewayProfitControlGateActive(e.ctx) {
			intent.done = true
			if err := applyShareBindingWrite(e.ctx, e.cache, e.gp.groupID, e.key, acc.ID, write, e.ttl); err != nil {
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
		decision := sharesched.Plan(&e.gp.policy, req, routed)
		if out := e.acquire(decision); out != nil {
			rec.routing = sharesched.ReasonModelRouting
			return out, decision
		}
		rec.routing = sharesched.ReasonModelRoutingFallback
	}
	decision := sharesched.Plan(&e.gp.policy, req, cands)
	out := e.acquire(decision)
	if out == nil {
		out = e.fallbackWait(decision)
	}
	return out, decision
}

// readBinding reads the session binding of a share group. A failed read
// places the request as new; SET NX keeps any binding that still exists.
func (s *ShareSchedulingService) readBinding(ctx context.Context, cache GatewayCache, gp *shareGroupPolicy, key string) sharesched.Binding {
	if key == "" {
		return sharesched.Binding{}
	}
	ttl := time.Duration(gp.policy.Affinity.TTLSeconds) * time.Second
	binding, err := readShareBinding(ctx, cache, gp.groupID, key, ttl)
	if err != nil {
		slog.Debug("share_scheduling.binding_read_failed", "group_id", gp.groupID, "error", err)
		return sharesched.Binding{}
	}
	return binding
}
