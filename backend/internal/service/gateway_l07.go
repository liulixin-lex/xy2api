package service

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// L-07 keeps a healthy Anthropic sticky owner whose slots are busy and whose
// wait queue is full (cache DESIGN §6.1, §6.1A). It only adds conditions after
// the 0.4.x decisions and only ever returns to the baseline earlier:
//   - the request is served by the Layer2 account as before, but no bind
//     point may move the session off the owner for this attempt (the
//     soft-penalty refusal channel of resilienceBindAllowed, INV-04);
//   - the next selection of the request releases the keep (per attempt), so
//     a failover may bind the account that finally serves it;
//   - any failed state read or write abandons the keep (fail-open to 0.4.x).
//
// Nothing here runs unless the routing policy armed the request: the request
// record exists only then, and every hook first checks for it.

const (
	l07ArmOff = "off"
	l07ArmOn  = "on"
)

// l07Request is the record of an armed request, stored in its resilience
// state. It is written by selection and read by the usage, failure and trace
// hooks, which may run on other goroutines.
type l07Request struct {
	policy  *CacheRoutingL07Policy // frozen at the first selection
	runtime *l07Runtime
	now     func() time.Time // the policy service clock
	groupID int64
	session string
	arm     string

	mu           sync.Mutex
	selections   int
	retry        bool // the current selection is not the first attempt (H2)
	kept         bool
	keptAttempt  bool
	owner        int64
	abandon      string
	busyN        int64
	holdMS       int64
	returned     bool // an owner hit ended a busy segment of this session
	outcomeNoted bool
}

// L07BusyState is what the busy path reads in one round trip: the session's
// L-07 segment and the resilience failure count of the session on the owner.
type L07BusyState struct {
	Owner       int64
	FirstBusyMs int64
	BusyN       int64
	FailCount   int64
	FailEndMs   int64 // end of the owner's failure window (last failure + streak window)
}

// L07StateStore is implemented by the Redis gateway cache. A cache without it
// cannot keep: every keep needs the segment state.
type L07StateStore interface {
	L07ReadBusy(ctx context.Context, groupID int64, sessionHash string, owner int64) (L07BusyState, error)
	L07WriteBusy(ctx context.Context, groupID int64, sessionHash string, owner, firstBusyMs, busyN int64, ttl time.Duration) error
	// L07RefreshSessionTTLClear refreshes the sticky binding and deletes the
	// segment in one pipeline; cleared reports whether a segment existed.
	L07RefreshSessionTTLClear(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) (cleared bool, err error)
	L07Clear(ctx context.Context, groupID int64, sessionHash string) (cleared bool, err error)
}

// l07Control is the shared, immutable record of control-cohort requests: they
// behave exactly as the baseline and allocate nothing, but the trace still
// tells them (arm=off) from unarmed requests (arm=none).
var l07Control = &l07Request{arm: l07ArmOff}

func l07FromState(st *resilience.RequestState) *l07Request {
	rec, _ := st.L07().(*l07Request)
	return rec
}

// l07Cohort is the rollout bucket of a session, independent of trace
// sampling and of the other rollouts (DESIGN §11.3 "l07-cohort|").
func l07Cohort(session string) int {
	const domain = "l07-cohort|"
	var input [len(domain) + 64]byte
	var digest [sha256.Size]byte
	if len(session) > len(input)-len(domain) {
		digest = sha256.Sum256([]byte(domain + session))
	} else {
		n := copy(input[:], domain)
		n += copy(input[n:], session)
		digest = sha256.Sum256(input[:n])
	}
	return int(digest[0]) * 100 / 256
}

// l07ReleaseAttempt runs when a selection starts: a keep holds for the
// attempt that made it only (DESIGN §6.1A.2 step 5, X C-2).
func (s *GatewayService) l07ReleaseAttempt(ctx context.Context) {
	if s.cachePolicies == nil {
		return
	}
	st := resilience.StateFrom(ctx)
	rec := l07FromState(st)
	if !rec.armed() {
		return
	}
	rec.mu.Lock()
	held := rec.keptAttempt
	rec.keptAttempt = false
	rec.mu.Unlock()
	if held {
		st.ReleaseAttemptSticky()
	}
}

// l07Begin returns the record of the request when the routing policy arms
// it, nil otherwise. The first selection takes the policy snapshot and the
// cohort; later selections reuse the record as retries. Only an Anthropic
// serving group with a session can be armed; share groups never get here.
// Every in-scope request counts toward the group budget (A4); only the
// treatment cohort gets its own record.
func (s *GatewayService) l07Begin(ctx context.Context, group *Group, sessionHash, platform string, excluded int) *l07Request {
	if s.cachePolicies == nil || sessionHash == "" || platform != PlatformAnthropic || group == nil || group.Platform != PlatformAnthropic {
		return nil
	}
	st := resilience.StateFrom(ctx)
	if st == nil {
		return nil
	}
	if rec := l07FromState(st); rec != nil {
		if rec.armed() {
			rec.mu.Lock()
			rec.selections++
			rec.retry = true
			rec.mu.Unlock()
		}
		return rec
	}
	policy := s.cachePolicies.Snapshot().RoutingL07()
	if policy == nil || !policy.AllowsGroup(group.ID) || st.SelectionNoted() {
		return nil
	}
	s.cachePolicies.l07.noteRequest(group.ID, s.cachePolicies.now())
	if l07Cohort(sessionHash) >= policy.RolloutPercent {
		st.SetL07(l07Control)
		return l07Control
	}
	rec := &l07Request{policy: policy, runtime: s.cachePolicies.l07, now: s.cachePolicies.now, groupID: group.ID, session: sessionHash, arm: l07ArmOn, selections: 1, retry: excluded > 0}
	st.SetL07(rec)
	return rec
}

func (rec *l07Request) armed() bool { return rec != nil && rec.arm == l07ArmOn }

// abandonOnce records the first abandon of the request; a keep made by an
// earlier attempt stays the request's outcome.
func (rec *l07Request) abandonOnce(reason string, now time.Time) {
	rec.mu.Lock()
	if rec.kept || rec.abandon != "" {
		rec.mu.Unlock()
		return
	}
	rec.abandon = reason
	rec.mu.Unlock()
	rec.runtime.noteAbandon(reason, now)
}

// l07OwnerSkipped records H3: the owner was skipped for a reason other than
// a busy slot with a full queue (session quota, gate, rpm, model support).
// The baseline rebinds as before.
func (s *GatewayService) l07OwnerSkipped(rec *l07Request) {
	if rec.armed() {
		rec.abandonOnce("not_busy_reason", rec.now())
	}
}

// l07OwnerBusy runs where the baseline gives up a sticky owner because its
// wait queue is full. slot is the owner's slot attempt: busy means it was not
// acquired without error. It reports whether the owner is kept for this
// attempt; the caller continues to Layer2 either way.
func (s *GatewayService) l07OwnerBusy(ctx context.Context, rec *l07Request, owner *Account, sessionHash, model string, slot *AcquireResult, slotErr, waitErr error) bool {
	if !rec.armed() || owner == nil {
		return false
	}
	now := rec.now()
	if slotErr == nil && slot != nil && slot.Acquired {
		// The slot was free but the session quota refused it (H3).
		rec.abandonOnce("not_busy_reason", now)
		return false
	}
	if slotErr != nil || waitErr != nil || slot == nil {
		rec.abandonOnce("state_error", now)
		return false
	}
	if owner.Platform != PlatformAnthropic {
		return false
	}
	rec.mu.Lock()
	retry := rec.retry
	rec.mu.Unlock()
	if retry {
		rec.abandonOnce("retry", now)
		return false
	}
	// H1: any cooldown, temp-unschedulable, overload, rate-limit window or
	// IQ failure of the owner (the penalty tiers of design §3.1).
	if tier, _, _ := resiliencePenaltyOf(owner, model, now); tier != resilience.TierNone {
		rec.abandonOnce("unhealthy", now)
		return false
	}
	st := resilience.StateFrom(ctx)
	store, ok := s.cache.(L07StateStore)
	if !ok || st == nil {
		rec.abandonOnce("state_error", now)
		return false
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	state, err := store.L07ReadBusy(stateCtx, rec.groupID, sessionHash, owner.ID)
	if err != nil {
		rec.abandonOnce("state_error", now)
		return false
	}
	p := rec.policy
	nowMs := now.UnixMilli()
	// H1: a recent RATE_LIMIT/TRANSIENT/FIRST_OUTPUT_TIMEOUT/STALL failure of
	// the session on the owner. Counts carry the end of their window, so the
	// effective lookback is min(health_lookback_seconds, streak window).
	if state.FailCount > 0 {
		window := int64(resilience.Current().Session.StreakWindowSeconds) * 1000
		if state.FailEndMs <= 0 || state.FailEndMs-window >= nowMs-int64(p.HealthLookbackSeconds)*1000 {
			rec.abandonOnce("unhealthy", now)
			return false
		}
	}
	n, first := int64(1), nowMs
	if state.Owner == owner.ID && state.BusyN > 0 && state.FirstBusyMs > 0 && state.FirstBusyMs <= nowMs {
		n, first = state.BusyN+1, state.FirstBusyMs
	}
	rec.mu.Lock()
	rec.busyN, rec.holdMS = n, nowMs-first
	rec.mu.Unlock()
	if n >= int64(p.MaxConsecutiveBusy) { // A1
		rec.abandonOnce("consecutive", now)
		return false
	}
	if nowMs-first >= int64(p.MaxHoldSeconds)*1000 { // A2
		rec.abandonOnce("hold_time", now)
		return false
	}
	if reason := rec.runtime.admit(owner.ID, owner.Concurrency, rec.groupID, p, now); reason != "" { // A3, A4
		rec.abandonOnce(reason, now)
		return false
	}
	if rec.runtime.guardTripped(now) { // A5
		rec.abandonOnce("guard", now)
		return false
	}
	ttl := 2 * time.Duration(p.MaxHoldSeconds) * time.Second
	if err := store.L07WriteBusy(stateCtx, rec.groupID, sessionHash, owner.ID, first, n, ttl); err != nil {
		rec.abandonOnce("state_error", now)
		return false
	}
	if !st.KeepStickyForAttempt(owner.ID) {
		rec.abandonOnce("state_error", now)
		return false
	}
	rec.mu.Lock()
	rec.kept, rec.keptAttempt, rec.owner = true, true, owner.ID
	rec.mu.Unlock()
	rec.runtime.noteKeep(owner.ID, rec.groupID, sessionHash, now.Add(ttl), now)
	return true
}

// l07RefreshOnOwnerHit replaces the owner-hit RefreshSessionTTL of an armed
// session: the same pipeline also ends its busy segment (A1 resets after the
// owner serves). A failed delete changes nothing and is not counted.
func (s *GatewayService) l07RefreshOnOwnerHit(ctx context.Context, rec *l07Request, groupID int64, sessionHash string, owner int64, ttl time.Duration) {
	store, ok := s.cache.(L07StateStore)
	if !ok {
		_ = s.cache.RefreshSessionTTL(ctx, groupID, sessionHash, ttl)
		return
	}
	cleared, err := store.L07RefreshSessionTTLClear(ctx, groupID, sessionHash, ttl)
	rec.runtime.takeRecent(rec.groupID, sessionHash, rec.now())
	if err == nil && cleared {
		rec.mu.Lock()
		rec.returned, rec.owner = true, owner
		rec.mu.Unlock()
	}
}

// l07RoutedOwnerHit ends the busy segment on a routed owner hit. The routed
// path has no binding refresh to share, so it deletes only when this
// instance kept the session in the current segment.
func (s *GatewayService) l07RoutedOwnerHit(ctx context.Context, rec *l07Request, sessionHash string, owner int64) {
	if !rec.armed() || !rec.runtime.takeRecent(rec.groupID, sessionHash, rec.now()) {
		return
	}
	store, ok := s.cache.(L07StateStore)
	if !ok {
		return
	}
	if cleared, err := store.L07Clear(ctx, rec.groupID, sessionHash); err == nil && cleared {
		rec.mu.Lock()
		rec.returned, rec.owner = true, owner
		rec.mu.Unlock()
	}
}

// l07NoteUsage records a successful upstream request of a treatment request:
// the first attempt outcome for the guard (kept requests on their spill
// account against the group's other treatment requests), a cold spill (kept and read 0) and
// a warm return to the owner (read/prompt ≥ 0.9).
func (s *GatewayService) l07NoteUsage(ctx context.Context, account *Account, read, prompt int) {
	if s.cachePolicies == nil || account == nil {
		return
	}
	st := resilience.StateFrom(ctx)
	rec := l07FromState(st)
	if !rec.armed() || st.AccountFailed(account.ID) {
		return
	}
	now := rec.now()
	rec.mu.Lock()
	first := !rec.outcomeNoted
	rec.outcomeNoted = true
	spill := rec.kept && account.ID != rec.owner
	returned := rec.returned && account.ID == rec.owner
	rec.mu.Unlock()
	if first {
		rec.runtime.noteOutcome(rec.groupID, spill, false, rec.policy.GuardErrorDeltaPP, rec.policy.GuardPauseSeconds, now)
	}
	if spill && read == 0 {
		rec.runtime.noteSpillCold(now)
	}
	if returned && prompt > 0 && read*10 >= prompt*9 {
		rec.runtime.noteReturnHit(now)
	}
}

// noteL07AttemptFailure records an account-attributable failure of the first
// attempt of an armed request for the guard (A5).
func noteL07AttemptFailure(ctx context.Context, accountID int64, c resilience.Category) {
	rec := l07FromState(resilience.StateFrom(ctx))
	if !rec.armed() || !c.AccountScoped() {
		return
	}
	rec.mu.Lock()
	first := !rec.outcomeNoted
	rec.outcomeNoted = true
	spill := rec.kept && accountID != rec.owner
	rec.mu.Unlock()
	if first {
		rec.runtime.noteOutcome(rec.groupID, spill, true, rec.policy.GuardErrorDeltaPP, rec.policy.GuardPauseSeconds, rec.now())
	}
}

// L07TraceFields are the l07_* fields of an Anthropic cache_trace event
// (cache_trace_contract/m4l07 §1–§2).
type L07TraceFields struct {
	Arm     string
	Kept    bool
	Abandon string
	BusyN   int64
	HoldMS  int64
}

// L07TraceFieldsFromContext returns the l07_* fields of the request for the
// Anthropic event emitter, given the event's owner_skip_reason. A request
// the policy did not arm is arm=none; its busy path reports policy_off.
func L07TraceFieldsFromContext(ctx context.Context, ownerSkipReason string) L07TraceFields {
	rec := l07FromState(resilience.StateFrom(ctx))
	if rec == nil {
		f := L07TraceFields{Arm: "none", Abandon: "none"}
		if ownerSkipReason == "busy_wait_full" {
			f.Abandon = "policy_off"
		}
		return f
	}
	if rec.arm != l07ArmOn {
		return L07TraceFields{Arm: l07ArmOff, Abandon: "none"}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	f := L07TraceFields{Arm: l07ArmOn, Kept: rec.kept, Abandon: rec.abandon, BusyN: rec.busyN, HoldMS: rec.holdMS}
	switch {
	case f.Kept:
		f.Abandon = "none"
	case f.Abandon == "" && ownerSkipReason != "" && ownerSkipReason != "none" && ownerSkipReason != "busy_wait_full":
		f.Abandon = "not_busy_reason"
	case f.Abandon == "":
		f.Abandon = "none"
	}
	if f.Abandon == "not_busy_reason" || f.Abandon == "none" && !f.Kept {
		f.BusyN, f.HoldMS = 0, 0
	}
	return f
}
