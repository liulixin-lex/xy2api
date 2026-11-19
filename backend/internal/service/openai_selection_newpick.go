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

// The first public selection owns one opportunity, shared by its internal passes.
func beginNewPickSelection(ctx context.Context) context.Context {
	var opportunity *atomic.Bool
	nativeCompact, _ := ctx.Value(nativeCompactionContextKey{}).(bool)
	if resilience.StateFrom(ctx).BeginNewPickSelection() && !nativeCompact && !openAIQualityRoutingActive(ctx) {
		opportunity = new(atomic.Bool)
		opportunity.Store(true)
	}
	return context.WithValue(ctx, newPickFirstSelectionKey{}, opportunity)
}

func newPickOpportunity(ctx context.Context) *atomic.Bool {
	pass, _ := ctx.Value(newPickFirstSelectionKey{}).(*atomic.Bool)
	return pass
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

func newPickFront(view schedquality.View, group int64, model string, acc *Account, request selectionRequest, now, snapshotAt time.Time) (newPickAccount, bool) {
	state := selectionState(view, group, acc, model)
	override, _ := state.EffectiveOverride(now)
	if acc.Concurrency <= 0 || view.ResetAt(acc.ID).After(snapshotAt) || override.Mode == schedquality.OverrideExempt || state.Placement(true, now) != schedquality.PlaceFront {
		return newPickAccount{}, false
	}
	key := buildSelectionKey(view, group, model, acc, request, now)
	return newPickAccount{acc, key, OpenAIAttemptModel(acc, model)}, key.Tier == 0
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

// The short critical section uses only already materialized values.
func (l *newPickLimit) reserve(opportunity *atomic.Bool, candidates []newPickCandidate, baseScore int64, window time.Time) (*newPickCandidate, int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.advance(window)
	if opportunity == nil || !opportunity.Load() || window.Before(l.window) {
		return nil, 0, false
	}
	c, capped := chooseNewPick(candidates, baseScore, l.counts)
	if c == nil {
		return nil, 0, false
	}
	id := c.target.account.ID
	count := l.counts[id]
	if !opportunity.CompareAndSwap(true, false) {
		return nil, 0, false
	}
	if capped {
		return c, count.picked + count.pending, true
	}
	count.pending++
	l.counts[id] = count
	return c, count.picked + count.pending, false
}

// Finish in the actual decision window. A delayed release cannot refund a
// newer window, and a successful attempt crossing a boundary needs room there.
func (l *newPickLimit) finish(ctx context.Context, id int64, first15 int, reserved, current time.Time, picked bool) (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.advance(current)
	count := l.counts[id]
	if reserved.Equal(l.window) {
		count.pending--
	}
	if picked && ctx.Err() == nil && current.Equal(l.window) && count.pending+count.picked < first15/4 {
		count.picked++
	} else {
		picked = false
	}
	l.counts[id] = count
	return count.picked + count.pending, picked
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

func (s *OpenAIGatewayService) newSessionPick(ctx context.Context, req OpenAIAccountScheduleRequest, base *Account, remaining []*Account, loads map[int64]*AccountLoadInfo) (selected *AccountSelectionResult) {
	board, state := s.selectionBoard, resilience.StateFrom(ctx)
	if board == nil || board.performance == nil || len(remaining) == 0 || s.concurrencyService == nil ||
		req.Platform != PlatformOpenAI || req.NoWait || req.RequireCompact || req.RequiredImageCapability != "" || len(req.ExcludedIDs) != 0 ||
		openAISelectionManualOnly(ctx) || openAISelectionExempt(ctx) || !firstOutputMigrationAllowed(ctx, req.SessionHash) ||
		state.StickyOwner() > 0 || state.KeptSticky() > 0 || resilienceLastResortGate(ctx) || openAIProxyStreamQuarantineBypassed(ctx) {
		return nil
	}
	if !pendingOpenAIStickyChange(ctx).readyForNewPick(req) {
		return nil
	}
	_, scoped := state.GatewayRequestScope()
	if !scoped && req.RequiredCapability != OpenAIEndpointCapabilityChatCompletions && req.RequiredCapability != OpenAIEndpointCapabilityResponses {
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
	b, ok := newPickFront(view, group, req.RequestedModel, base, request, now, at)
	if !ok || request.Borrow {
		return nil
	}
	fromScore, ok := snap.ScoreMicros(b.account.ID, b.model)
	if !ok {
		return nil
	}
	candidates := make([]newPickCandidate, 0, len(remaining))
	for _, a := range remaining {
		t, front := newPickFront(view, group, req.RequestedModel, a, request, now, at)
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
	if resilienceLastCandidate(ctx, base.ID) || state.Budget().Attempts() > 0 {
		return nil
	}
	window := now.UTC().Truncate(15 * time.Minute)
	c, used, capped := board.newPickLimit.reserve(newPickOpportunity(ctx), candidates, fromScore, window)
	if c == nil {
		return nil
	}
	outcome := "capped"
	var target *AccountSelectionResult
	defer func() {
		if target != nil && selected == nil {
			target.ReleaseFunc()
		}
	}()
	if !capped {
		// Unwind the reservation even if an account, cache, policy or clock read panics.
		limit := &board.newPickLimit
		pending := true
		defer func() {
			if pending {
				limit.finish(ctx, c.target.account.ID, c.first15, window, window, false)
			}
		}()
		target, outcome = s.tryNewPick(ctx, req, c.target)
		eligible := false
		if target != nil {
			bNow, tNow, ok := s.validateNewPick(ctx, req, b, target.Account, request, snap, at)
			eligible = ok
			if ok {
				b, c.target = bNow, tNow
			}
		}
		end := s.selectionNow().UTC().Truncate(15 * time.Minute)
		finalUsed, committed := limit.finish(ctx, c.target.account.ID, c.first15, window, end, eligible)
		pending = false
		if target != nil && !committed {
			outcome = "target_vetoed"
		} else if committed && !end.Equal(window) {
			window, used = end, finalUsed
		}
	}

	slog.InfoContext(ctx, "sched_quality.new_pick", "component", "sched_quality", "gateway_request_id", state.GatewayRequestID(),
		"group_id", group, "model", b.model, "from", b.account.ID, "to", c.target.account.ID,
		"from_score", fromScore, "to_score", c.score, "snapshot_at", at, "first15", c.first15, "used", used,
		"window", window, "first10", c.recent.First10, "first30", c.recent.First30, "from_priority", b.key.Priority, "to_priority", c.target.key.Priority,
		"from_rate_known", b.key.RateKnown, "from_rate", newPickRate(b.key), "to_rate_known", c.target.key.RateKnown, "to_rate", newPickRate(c.target.key), "result", outcome)

	if outcome == "picked" {
		return target
	}
	return nil
}

func newPickRate(key selectionKey) *float64 {
	if !key.RateKnown {
		return nil
	}
	return &key.Rate
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
	b, baseFront := newPickFront(view, group, req.RequestedModel, original.account, request, now, at)
	t, targetFront := newPickFront(view, group, req.RequestedModel, target, request, now, at)
	return b, t, baseFront && targetFront && b.permits(t) && b.model == original.model && !openAIStickyBindingKept(ctx, req.SessionHash, target.ID)
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
		} else if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
	if selected == nil {
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
