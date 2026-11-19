package service

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type newPickFirstSelectionKey struct{}

type newPickPass struct {
	opportunity          atomic.Bool
	firstOutputAvoidRead bool
	target               *AccountSelectionResult
	reservation          *newPickReservation
	log                  func(string)
}

// Called at the outermost return, after all inner selection defers have run.
// On panic the caller's other defer still owns both the reservation and slot.
func (p *newPickPass) deliver(ctx context.Context, selected *AccountSelectionResult) error {
	if p.target == nil || p.target != selected {
		return nil
	}
	if err := p.reservation.commit(ctx); err != nil {
		p.log("target_vetoed")
		return err
	}
	p.log("picked")
	p.target, p.reservation, p.log = nil, nil, nil
	return nil
}

func (p *newPickPass) release() {
	if p.target != nil {
		defer p.target.ReleaseFunc()
		p.reservation.release()
		p.target, p.reservation, p.log = nil, nil, nil
	}
}

// The first public selection owns one opportunity, shared by its internal passes.
func beginNewPickSelection(ctx context.Context) context.Context {
	var pass *newPickPass
	nativeCompact, _ := ctx.Value(nativeCompactionContextKey{}).(bool)
	if resilience.StateFrom(ctx).BeginNewPickSelection() && !nativeCompact && !openAIQualityRoutingActive(ctx) {
		pass = new(newPickPass)
		pass.opportunity.Store(true)
	}
	return context.WithValue(ctx, newPickFirstSelectionKey{}, pass)
}

func newPickPassFrom(ctx context.Context) *newPickPass {
	pass, _ := ctx.Value(newPickFirstSelectionKey{}).(*newPickPass)
	return pass
}

func newPickOpportunity(ctx context.Context) *atomic.Bool {
	if pass := newPickPassFrom(ctx); pass != nil {
		return &pass.opportunity
	}
	return nil
}

func isFirstNewPickSelection(ctx context.Context) bool {
	pass := newPickOpportunity(ctx)
	return pass != nil && pass.Load()
}

func stopNewPick(ctx context.Context) {
	if pass := newPickOpportunity(ctx); pass != nil {
		pass.Store(false)
	}
}

type newPickSnapshot interface {
	Usable(time.Time) bool
	ScoreMicros(int64, string) (int64, bool)
	Recent(int64, int64, string) SchedPerfRecent
	First15(int64) int
}

type newPickAccount struct {
	account *Account
	key     selectionKey
	model   string
}

func (s *OpenAIGatewayService) newPickFront(ctx context.Context, view schedquality.View, group int64, model string, acc *Account, request selectionRequest, now, snapshotAt time.Time) (newPickAccount, bool) {
	state := selectionState(view, group, acc, model)
	override, _ := state.EffectiveOverride(now)
	if acc.Concurrency <= 0 || view.ResetAt(acc.ID).After(snapshotAt) || override.Mode == schedquality.OverrideExempt || state.Placement(true, now) != schedquality.PlaceFront {
		return newPickAccount{}, false
	}
	key := buildSelectionKey(view, group, model, acc, request, now)
	attemptModel := OpenAIAttemptModel(acc, model)
	return newPickAccount{acc, key, attemptModel}, key.Tier == 0 && s.newPickForwardModel(ctx, acc, model) == attemptModel
}

func (b newPickAccount) permits(t newPickAccount) bool {
	return t.account.ID != b.account.ID && t.model == b.model && t.key.Pinned == b.key.Pinned &&
		t.key.Priority == b.key.Priority && (!b.key.RateKnown || t.key.RateKnown && t.key.Rate <= b.key.Rate)
}

type newPickCandidate struct {
	target  newPickAccount
	score   int64
	recent  SchedPerfRecent
	first15 int
}

// counts includes successful decisions and reservations not yet resolved.
type newPickCount struct{ picked, pending int }
type newPickLimit struct {
	mu     sync.Mutex
	window time.Time
	counts map[int64]newPickCount
}

// chooseNewPick is read-only. Candidates retain their original selection order.
func chooseNewPick(candidates []newPickCandidate, baseScore int64, counts map[int64]newPickCount) (choice *newPickCandidate, capped bool) {
	var blocked *newPickCandidate
	for i := range candidates {
		c := &candidates[i]
		if baseScore-c.score < 30_000 || c.recent.First30 < 3 || c.recent.First10 < 1 {
			continue
		}
		count := counts[c.target.account.ID]
		if count.picked+count.pending >= c.first15/4 {
			if blocked == nil || c.score < blocked.score {
				blocked = c
			}
		} else if choice == nil || c.score < choice.score {
			choice = c
		}
	}
	if choice == nil {
		return blocked, blocked != nil
	}
	return choice, false
}

func (l *newPickLimit) advance(window time.Time) {
	if l.counts == nil || window.After(l.window) {
		l.window, l.counts = window, make(map[int64]newPickCount)
	}
}

// Keep the actual bucket alive until the scheduler returns. Another request
// may advance the process window while the selected slot is being handed off.
type newPickReservation struct {
	limit     *newPickLimit
	counts    map[int64]newPickCount
	window    time.Time
	id        int64
	used      int
	committed bool
}

func (l *newPickLimit) reserve(opportunity *atomic.Bool, candidates []newPickCandidate, baseScore int64, window time.Time) (*newPickCandidate, *newPickReservation, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.advance(window)
	if opportunity == nil || !opportunity.Load() || window.Before(l.window) {
		return nil, nil, 0
	}
	c, capped := chooseNewPick(candidates, baseScore, l.counts)
	if c == nil || !opportunity.CompareAndSwap(true, false) {
		return nil, nil, 0
	}
	id := c.target.account.ID
	count := l.counts[id]
	if capped {
		return c, nil, count.picked + count.pending
	}
	count.pending++
	l.counts[id] = count
	used := count.picked + count.pending
	return c, &newPickReservation{limit: l, counts: l.counts, window: window, id: id, used: used}, used
}

// Before B is released, obtain capacity in the decision window if it changed.
func (r *newPickReservation) confirm(ctx context.Context, window time.Time, first15 int) bool {
	l := r.limit
	l.mu.Lock()
	defer l.mu.Unlock()
	l.advance(window)
	if ctx.Err() != nil || !window.Equal(l.window) {
		return false
	}
	if r.window.Equal(window) {
		return true
	}
	next := l.counts[r.id]
	if next.picked+next.pending >= first15/4 {
		return false
	}
	old := r.counts[r.id]
	old.pending--
	r.counts[r.id] = old
	next.pending++
	l.counts[r.id] = next
	r.counts, r.window, r.used = l.counts, window, next.picked+next.pending
	return true
}

func (r *newPickReservation) commit(ctx context.Context) error {
	r.limit.mu.Lock()
	defer r.limit.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	count := r.counts[r.id]
	count.pending--
	count.picked++
	r.counts[r.id] = count
	r.committed = true
	return nil
}

// Also undo a commit if the decision logger panics before the result returns.
func (r *newPickReservation) release() {
	r.limit.mu.Lock()
	defer r.limit.mu.Unlock()
	count := r.counts[r.id]
	if r.committed {
		count.picked--
	} else {
		count.pending--
	}
	r.counts[r.id] = count
}

func (change *openAIStickyChange) readyForNewPick(req OpenAIAccountScheduleRequest) bool {
	if !change.matchesSession(req.GroupID, req.SessionHash) || !change.newPickMissing {
		return false
	}
	change.mu.Lock()
	defer change.mu.Unlock()
	return !change.skipCommit && change.expected.AccountID == 0 && change.kind == "create" &&
		!change.ownerReleased && change.target == 0 && !change.done
}

func (s *OpenAIGatewayService) newPickNeedsAliasRead(ctx context.Context, group int64, session string) bool {
	if !isFirstNewPickSelection(ctx) || s.concurrencyService == nil || s.selectionBoard == nil || s.selectionBoard.performance == nil {
		return false
	}
	req, ok := ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest)
	if !ok || !newPickRequestEligible(ctx, req, session) {
		return false
	}
	now := s.selectionNow()
	if !selectionAuto(s.selectionView(), group, now) {
		return false
	}
	snap, _ := s.selectionBoard.performance()
	return snap != nil && snap.Usable(now)
}

// These exclusions are already known before reading any binding evidence.
func newPickRequestEligible(ctx context.Context, req OpenAIAccountScheduleRequest, session string) bool {
	state := resilience.StateFrom(ctx)
	_, scoped := state.GatewayRequestScope()
	return req.Platform == PlatformOpenAI && !openAISelectionNoWait(ctx) && !req.NoWait &&
		!req.RequireCompact && req.RequiredImageCapability == "" && len(req.ExcludedIDs) == 0 &&
		!openAISelectionManualOnly(ctx) && !openAISelectionExempt(ctx) && firstOutputMigrationAllowed(ctx, session) &&
		state.StickyOwner() == 0 && state.KeptSticky() == 0 && state.Budget().Attempts() == 0 &&
		!resilienceLastResortGate(ctx) && !openAIProxyStreamQuarantineBypassed(ctx) &&
		(scoped || req.RequiredCapability == OpenAIEndpointCapabilityChatCompletions || req.RequiredCapability == OpenAIEndpointCapabilityResponses)
}

func (s *OpenAIGatewayService) newSessionPick(ctx context.Context, req OpenAIAccountScheduleRequest, base *Account, remaining []*Account, loads map[int64]*AccountLoadInfo) (selected *AccountSelectionResult) {
	board, state := s.selectionBoard, resilience.StateFrom(ctx)
	if board == nil || board.performance == nil || len(remaining) == 0 || s.concurrencyService == nil || !newPickRequestEligible(ctx, req, req.SessionHash) {
		return nil
	}
	if !pendingOpenAIStickyChange(ctx).readyForNewPick(req) {
		return nil
	}
	if s.firstOutputQualityOwnsSticky(ctx, req.GroupID, req.SessionHash) || openAIStickyBindingKept(ctx, req.SessionHash, base.ID) {
		return nil
	}
	group, now, view := derefGroupID(req.GroupID), s.selectionNow(), s.selectionView()
	if !selectionAuto(view, group, now) {
		return nil
	}
	snap, at := board.performance()
	if snap == nil || !snap.Usable(now) {
		return nil
	}
	request := s.selectionRequest(ctx, req, loads)
	b, ok := s.newPickFront(ctx, view, group, req.RequestedModel, base, request, now, at)
	if !ok || request.Borrow {
		return nil
	}
	fromScore, ok := snap.ScoreMicros(b.account.ID, b.model)
	if !ok {
		return nil
	}
	candidates := make([]newPickCandidate, 0, len(remaining))
	for _, a := range remaining {
		t, front := s.newPickFront(ctx, view, group, req.RequestedModel, a, request, now, at)
		if !front || !b.permits(t) || s.selectionFullMarks.Recently(a.ID, now) {
			continue
		}
		if score, known := snap.ScoreMicros(a.ID, t.model); known {
			candidates = append(candidates, newPickCandidate{t, score, snap.Recent(group, a.ID, t.model), snap.First15(a.ID)})
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	if resilienceLastCandidate(ctx, base.ID) {
		return nil
	}
	window := now.UTC().Truncate(15 * time.Minute)
	c, reservation, used := board.newPickLimit.reserve(newPickOpportunity(ctx), candidates, fromScore, window)
	if c == nil {
		return nil
	}
	logDecision := func(outcome string) {
		slog.InfoContext(ctx, "sched_quality.new_pick", "component", "sched_quality", "gateway_request_id", state.GatewayRequestID(),
			"group_id", group, "model", b.model, "from", b.account.ID, "to", c.target.account.ID,
			"from_score", fromScore, "to_score", c.score, "snapshot_at", at, "first15", c.first15, "used", used,
			"window", window, "first10", c.recent.First10, "first30", c.recent.First30, "from_priority", b.key.Priority, "to_priority", c.target.key.Priority,
			"from_rate_known", b.key.RateKnown, "from_rate", newPickRate(b.key), "to_rate_known", c.target.key.RateKnown, "to_rate", newPickRate(c.target.key), "result", outcome)
	}
	if reservation == nil {
		logDecision("capped")
		return nil
	}

	// Until ownership passes to the scheduler's return guard, this scope
	// owns T and the reservation on every rejection, cancellation or panic.
	var target *AccountSelectionResult
	defer func() {
		if selected == nil {
			if target != nil {
				defer target.ReleaseFunc()
			}
			reservation.release()
		}
	}()
	pass := newPickPassFrom(ctx)
	if !pass.firstOutputAvoidRead {
		// The ordinary path may have skipped this scope with session resilience off.
		// Existing avoid records still apply to voluntary placement.
		avoided, err := sessionAvoidAfterReset(ctx, s.resilience.store, group, firstOutputAvoidSession(req.SessionHash), s.resilience.sessionResetAt)
		if _, blocked := avoided[c.target.account.ID]; err != nil || blocked {
			return nil
		}
	}
	target, outcome := s.tryNewPick(ctx, req, c.target)
	if target == nil {
		logDecision(outcome)
		return nil
	}
	bNow, tNow, eligible := s.validateNewPick(ctx, req, b, target.Account, request, snap, at)
	if !eligible || !reservation.confirm(ctx, s.selectionNow().UTC().Truncate(15*time.Minute), c.first15) {
		logDecision("target_vetoed")
		return nil
	}
	b, c.target, window, used = bNow, tNow, reservation.window, reservation.used
	target.ReleaseFunc = sync.OnceFunc(target.ReleaseFunc)
	pass.target, pass.reservation, pass.log = target, reservation, logDecision
	return target
}

func newPickRate(key selectionKey) any {
	if !key.RateKnown {
		return nil
	}
	return key.Rate
}

// T takes the ordinary recheck, slot and admission gates. Preserve exactly the
// admitted row (including a newer profit-cache row) through result construction.
func (s *OpenAIGatewayService) tryNewPick(ctx context.Context, req OpenAIAccountScheduleRequest, t newPickAccount) (result *AccountSelectionResult, outcome string) {
	fresh := s.recheckSelectionAccount(ctx, req, t.account)
	if fresh == nil || fresh.Concurrency <= 0 {
		return nil, "target_recheck_failed"
	}
	slot, err := s.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
	if err != nil || slot == nil || !slot.Acquired {
		return nil, "target_slot_busy"
	}
	defer func() {
		if result == nil {
			slot.ReleaseFunc()
		}
	}()
	latest, veto, _ := s.SelectionAdmissionVeto(ctx, fresh)
	if veto || latest == nil || latest.ID != fresh.ID {
		return nil, "target_vetoed"
	}
	return selectionResultForAccount(ctx, latest, true, slot.ReleaseFunc, nil), "picked"
}

func (s *OpenAIGatewayService) validateNewPick(ctx context.Context, req OpenAIAccountScheduleRequest, original newPickAccount, target *Account, request selectionRequest, snap newPickSnapshot, at time.Time) (newPickAccount, newPickAccount, bool) {
	view, now, group := s.selectionView(), s.selectionNow(), derefGroupID(req.GroupID)
	var b, t newPickAccount
	if !selectionAuto(view, group, now) || !snap.Usable(now) || s.selectionFullMarks.Recently(target.ID, now) {
		return b, t, false
	}
	b, baseFront := s.newPickFront(ctx, view, group, req.RequestedModel, original.account, request, now, at)
	t, targetFront := s.newPickFront(ctx, view, group, req.RequestedModel, target, request, now, at)
	return b, t, baseFront && targetFront && b.permits(t) && b.model == original.model && !openAIStickyBindingKept(ctx, req.SessionHash, target.ID)
}

// Load filtering/sorting establishes this pass's order. A requeued trial
// remains available to ordinary selection but cannot become a fresh target.
func newPickSuffix(original []*Account, base int64, tried map[int64]bool) []*Account {
	for i, a := range original {
		if a.ID != base {
			continue
		}
		suffix := original[i+1:]
		for j, t := range suffix {
			if !tried[t.ID] {
				continue
			}
			clean := append([]*Account(nil), suffix[:j]...)
			for _, other := range suffix[j+1:] {
				if !tried[other.ID] {
					clean = append(clean, other)
				}
			}
			return clean
		}
		return suffix
	}
	return nil
}

// The acquired slot is owned here until the final result is handed
// to the caller. A lost trial reservation returns its row to the ordinary sort.
func (s *OpenAIGatewayService) finishSelectionSlot(ctx context.Context, req OpenAIAccountScheduleRequest, fresh *Account, slot *AcquireResult, trial *selectionTrial, remaining []*Account, loads map[int64]*AccountLoadInfo, canPick bool) (selected *AccountSelectionResult, retryTrial *Account, err error) {
	releaseBase := sync.OnceFunc(slot.ReleaseFunc)
	owned := releaseBase
	defer func() {
		if owned != nil {
			owned()
		}
	}()
	latest, vetoed, _ := s.SelectionAdmissionVeto(ctx, fresh)
	if vetoed || latest == nil {
		return nil, nil, nil
	}
	if canPick {
		selected = s.newSessionPick(ctx, req, latest, remaining, loads)
		if selected != nil {
			owned = selected.ReleaseFunc
			releaseBase()
			latest = selected.Account
		}
	}
	if selected == nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		full := buildSelectionKey(s.selectionView(), derefGroupID(req.GroupID), req.RequestedModel, latest, s.selectionRequest(ctx, req, loads), s.selectionNow()).Tier >= 1
		reservation, accepted := trial.reserve(ctx, latest, full)
		if !accepted {
			return nil, latest, nil
		}
		trial.selected = reservation
		selected, err = s.newAcquiredSelectionResult(ctx, latest, releaseBase)
		if err != nil {
			return nil, nil, err
		}
	}
	if req.SessionHash != "" && (!trial.isTrial(latest) || !trial.borrowing(ctx)) && !gatewayProfitControlGateActive(ctx) {
		_ = s.bindOpenAIStickySessionDuringSelection(ctx, req.GroupID, req.SessionHash, latest.ID)
	}
	owned = nil
	return selected, nil, nil
}
