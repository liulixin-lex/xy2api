package service

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type selectionSessionSlots interface {
	tryAcquireAccountSlot(context.Context, int64, int) (*AcquireResult, error)
}

// selectionSession receives already admitted candidates. Recheck repeats fresh
// account admission before and after acquiring a slot; view always reads latest.
type selectionSession struct {
	view              func() schedquality.View
	now               func() time.Time
	slots             selectionSessionSlots
	cache             OpenAIStickyVersionedCache
	store             ResilienceStore
	cacheKey          func(string) string
	bindings          gatewaySessionBindingReader
	marks             *fullMarks
	bindingTTL        time.Duration
	recheck           func(context.Context, *Account) *Account
	resetAt           func(int64) time.Time
	leases            SchedTrialLeases
	vetoOpportunity   func(string, map[int64]struct{}, map[int64]struct{}, func(*Account) bool) *Account
	qualityOwnsSticky func(context.Context, *int64, string) bool
}

// Reuse this evidence when planning any voluntary switch. Keeping it on the
// pending change also requests the atomic target check at commit.
type voluntarySwitchEvidence struct {
	idleSetting, idleObserved  *int64
	avoidSeen, qualityOwned    *bool
	sessionOn, transferAllowed bool
}

func (s *selectionSession) voluntaryAvoid(ctx context.Context, group int64, session string, v *voluntarySwitchEvidence) (map[int64]struct{}, string) {
	v.sessionOn = resilience.Current().SessionOn()
	v.transferAllowed = firstOutputMigrationAllowed(ctx, session)
	if s.qualityOwnsSticky != nil {
		owned := s.qualityOwnsSticky(ctx, &group, session)
		v.qualityOwned = &owned
	}
	avoided, err := sessionAvoidAfterReset(ctx, s.store, group, firstOutputAvoidSession(session), s.resetAt)
	if err != nil {
		return nil, "avoid_read_error"
	}
	v.avoidSeen = new(len(avoided) != 0)
	if v.qualityOwned != nil && *v.qualityOwned {
		return avoided, "quality_owned"
	}
	return avoided, ""
}

func (change *openAIStickyChange) logVeto(ctx context.Context, target *Account, reason string) {
	if !resilience.StateFrom(ctx).ClaimVoluntarySwitchVeto() {
		return
	}
	if target != nil {
		change.target, change.toPriority = target.ID, new(target.Priority)
		change.toModel = OpenAIAttemptModel(target, change.model)
	}
	change.logSwitch(ctx, reason, BindingCommitResult{})
}

func (s *selectionSession) takeoverReady(ctx context.Context, view schedquality.View, groupID int64, key string, owner int64, now time.Time, evidence *voluntarySwitchEvidence) bool {
	if !selectionLive(view, now) {
		return false
	}
	idle, enabled := view.TakeoverIdle(groupID)
	if !enabled || idle < 0 {
		return false
	}
	if evidence != nil {
		evidence.idleObserved = nil
		evidence.idleSetting = new(int64(idle / time.Second))
	}
	if idle == 0 {
		return true
	}
	if s.bindings == nil {
		return false
	}
	id, remaining, err := s.bindings.GetSessionAccountIDWithTTL(ctx, groupID, key)
	if err != nil || id != owner || remaining <= 0 {
		return false
	}
	observed := s.bindingTTL - remaining
	if evidence != nil {
		evidence.idleObserved = new(int64(observed / time.Second))
	}
	return observed >= idle
}

func (s *selectionSession) candidateAllowed(view schedquality.View, groupID int64, model string, owner, target *Account, migrate bool, req selectionRequest, now time.Time) bool {
	if groupID > 0 && !openAIStickyAccountMatchesGroup(target, &groupID) {
		return false
	}
	state := selectionState(view, groupID, target, model)
	auto := selectionAuto(view, groupID, now)
	if migrate {
		return selectionMigrateOwner(view, groupID, owner, model, req.Session, now) && (state.Placement(auto, now) == schedquality.PlaceFront || state.Placement(auto, now) == schedquality.PlaceTrial && req.Room != nil && req.Room(target.ID, OpenAIAttemptModel(target, model)))
	}
	return selectionLive(view, now) && target.Priority < owner.Priority && state.Placement(auto, now) == schedquality.PlaceFront && !s.marks.Recently(target.ID, now)
}

func (s *selectionSession) selectOwner(ctx context.Context, groupID int64, sessionHash, model string, owner *Account, candidates []*Account, req selectionRequest) *AccountSelectionResult {
	if owner == nil || s.cache == nil || s.store == nil || s.cacheKey == nil || resilience.StateFrom(ctx) == nil || openAINoStickyMigration(ctx) || openAISelectionExempt(ctx) || req.ManualOnly {
		return nil
	}
	if pending := pendingOpenAIStickyChange(ctx); pending != nil {
		pending.mu.Lock()
		blocked := pending.target != 0 && (!pending.done || pending.kind != "migrate")
		pending.mu.Unlock()
		if blocked {
			return nil
		}
	}
	key := s.cacheKey(sessionHash)
	if pending := pendingOpenAIStickyChange(ctx); pending != nil && pending.groupID == groupID && pending.session == strings.TrimSpace(sessionHash) {
		key = pending.key
	}
	if key == "" {
		return nil
	}
	now, view := s.now(), s.view()
	req.Session = sessionHash
	req.Borrow = false // This path plans an actual migration or takeover.
	migrate := selectionMigrateOwner(view, groupID, owner, model, sessionHash, now)
	facts := &voluntarySwitchEvidence{}
	if !migrate && !s.takeoverReady(ctx, view, groupID, key, owner.ID, now, facts) {
		return nil
	}
	expected, err := s.cache.GetSessionBinding(ctx, groupID, key)
	if err != nil || expected.AccountID != owner.ID {
		return nil
	}
	if pending := pendingOpenAIStickyChange(ctx); pending != nil && pending.key == key {
		expected.LegacyKey = pending.expected.LegacyKey
	}
	kind := "takeover"
	if migrate {
		kind = "migrate"
	}
	change := &openAIStickyChange{groupID: groupID, session: strings.TrimSpace(sessionHash), key: key, model: model, expected: expected,
		fromPriority: new(owner.Priority), fromModel: OpenAIAttemptModel(owner, model), kind: kind, voluntary: facts, ttl: s.bindingTTL, cache: s.cache, resetAt: s.resetAt, leases: s.leases, trialSession: sessionHash}
	soft, veto := s.voluntaryAvoid(ctx, groupID, sessionHash, facts)
	avoided, err := sessionAvoidAfterReset(ctx, s.store, groupID, strings.TrimSpace(sessionHash), s.resetAt)
	if err != nil {
		return nil
	}
	failedTargets, err := sessionAvoidAfterReset(ctx, s.store, groupID, stickyChangeAvoidSession(sessionHash), s.resetAt)
	if err != nil {
		return nil
	}
	avoided = mergeExcluded(avoided, failedTargets)
	allowedForVeto := func(a *Account) bool {
		return a != nil && s.candidateAllowed(view, groupID, model, owner, a, migrate, req, now)
	}
	logOpportunity := func(reason string) {
		if resilience.StateFrom(ctx).VoluntarySwitchVetoLogged() {
			return
		}
		var target *Account
		if s.vetoOpportunity != nil {
			target = s.vetoOpportunity(reason, soft, avoided, allowedForVeto)
		} else {
			for _, a := range candidates {
				if a == nil || a.ID == owner.ID || !allowedForVeto(a) {
					continue
				}
				_, strict := avoided[a.ID]
				_, hit := soft[a.ID]
				if !strict && (reason != "target_avoided" || hit) {
					target = a
					break
				}
			}
		}
		if target != nil {
			change.logVeto(ctx, target, reason)
		}
	}
	if veto != "" {
		if facts.qualityOwned != nil && *facts.qualityOwned {
			if facts.avoidSeen == nil {
				return nil
			} // An unreadable scope cannot establish an independent opportunity.
			avoided = mergeExcluded(avoided, soft)
		}
		logOpportunity(veto)
		return nil
	}
	s.marks.observe(candidates, req.Loads, now)
	type candidate struct {
		account *Account
		key     selectionKey
	}
	ordered := make([]candidate, 0, len(candidates))
	for _, account := range candidates {
		if account == nil || account.ID == owner.ID || !s.candidateAllowed(view, groupID, model, owner, account, migrate, req, now) {
			continue
		}
		if _, blocked := avoided[account.ID]; blocked {
			continue
		}
		if _, blocked := soft[account.ID]; blocked {
			continue
		}
		ordered = append(ordered, candidate{account, buildSelectionKey(view, groupID, model, account, req, now)})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		// Compact capability routing predates quality ranking and applies only
		// to RequireCompact: confirmed support precedes unknown, then the quality key.
		if req.RequireCompact {
			a, b := openAICompactSupportTier(ordered[i].account), openAICompactSupportTier(ordered[j].account)
			if a != b {
				return a > b
			}
		}

		return selectionLess(ordered[i].key, ordered[j].key)
	})
	if !migrate && len(ordered) > schedquality.TakeoverMaxCandidates {
		ordered = ordered[:schedquality.TakeoverMaxCandidates]
	}
	for _, item := range ordered {
		if ctx.Err() != nil {
			return nil
		}
		target := s.recheck(ctx, item.account)
		if target == nil || !s.candidateAllowed(s.view(), groupID, model, owner, target, migrate, req, s.now()) {
			continue
		}
		slot, err := s.slots.tryAcquireAccountSlot(ctx, target.ID, target.Concurrency)
		if err != nil || slot == nil || !slot.Acquired {
			if err == nil && slot != nil && !slot.Acquired {
				s.marks.Mark(target.ID, s.now())
			}
			if slot != nil && slot.ReleaseFunc != nil {
				slot.ReleaseFunc()
			}
			continue
		}
		var reservation *selectionTrialReservation
		transferred := false
		selection := func() *AccountSelectionResult {
			defer func() {
				if reservation != nil && !transferred {
					reservation.release(ctx)
				}
			}()
			selected, _ := withOpenAISelectionSlotGuard(slot.ReleaseFunc, func(slotRelease func()) (*AccountSelectionResult, error) {
				latest := s.recheck(ctx, target)
				latestOwner := owner
				if !migrate {
					latestOwner = s.recheck(ctx, owner)
				}
				now, view = s.now(), s.view()
				allowed := latest != nil && latestOwner != nil && s.candidateAllowed(view, groupID, model, latestOwner, latest, migrate, req, now)
				if !migrate {
					allowed = allowed && s.takeoverReady(ctx, view, groupID, key, owner.ID, now, facts)
				}
				if !allowed || ctx.Err() != nil {
					return nil, nil
				}
				if selectionState(view, groupID, latest, model).Placement(selectionAuto(view, groupID, now), now) == schedquality.PlaceTrial {
					acquired := false
					var generation string
					if s.leases != nil {
						var err error
						acquired, generation, err = s.leases.Acquire(ctx, latest.ID, OpenAIAttemptModel(latest, model), groupID, TrialLeaseRequestMember(resilience.StateFrom(ctx).GatewayRequestID()), now)
						if err != nil {
							slog.WarnContext(ctx, "sched_quality.trial_acquire_failed", "component", "sched_quality", "account_id", latest.ID, "error", err)
							acquired = false
						}
					}
					if !acquired {
						return nil, nil
					}
					reservation = &selectionTrialReservation{leases: s.leases, account: latest.ID, group: groupID, model: OpenAIAttemptModel(latest, model), member: TrialLeaseRequestMember(resilience.StateFrom(ctx).GatewayRequestID()), session: TrialLeaseSessionMember(sessionHash), now: s.now}
					reservation.rememberRound(view, generation)
				}
				reason := "priority_takeover"
				if migrate {
					reason = "quality_migration_ttfo"
					if selectionState(view, groupID, owner, model).Error.Identity == schedquality.IdentityRear {
						reason = "quality_migration_error"
					}
				}
				resilience.StateFrom(ctx).NoteStickyBypassReason(reason)
				change.reason, change.target, change.trial = reason, latest.ID, reservation
				change.fromPriority, change.toPriority = new(latestOwner.Priority), new(latest.Priority)
				change.fromModel, change.toModel = OpenAIAttemptModel(latestOwner, model), OpenAIAttemptModel(latest, model)
				resilience.StateFrom(ctx).SetStickyChange(change)
				return attachSelectionProfitGate(ctx, &AccountSelectionResult{Account: latest, Acquired: true, ReleaseFunc: releaseSelectionTrialWithSlot(ctx, change, reservation, slotRelease)}), nil
			})
			transferred = selected != nil
			return selected
		}()
		if selection != nil {
			return selection
		}
	}
	if len(soft) != 0 {
		logOpportunity("target_avoided")
	}
	return nil
}

type openAINoStickyMigrationKey struct{}

func withOpenAINoStickyMigration(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAINoStickyMigrationKey{}, true)
}

func openAINoStickyMigration(ctx context.Context) bool {
	value, _ := ctx.Value(openAINoStickyMigrationKey{}).(bool)
	return value
}

func withOpenAIStickyMigrationGuard(ctx context.Context, previousResponseID string, requiredTransport OpenAIUpstreamTransport) context.Context {
	if strings.TrimSpace(previousResponseID) != "" || (requiredTransport != OpenAIUpstreamTransportAny && requiredTransport != OpenAIUpstreamTransportHTTPSSE) {
		return withOpenAINoStickyMigration(ctx)
	}
	return ctx
}

func selectionMigrateOwner(view schedquality.View, groupID int64, owner *Account, model, session string, now time.Time) bool {
	if view == nil || owner == nil {
		return false
	}
	state := selectionState(view, groupID, owner, model)
	return state.MigrateOwner(selectionAuto(view, groupID, now), now, view.SessionSlowTwice(groupID, owner.ID, OpenAIAttemptModel(owner, model), session))
}
