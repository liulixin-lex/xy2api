package service

import (
	"container/list"
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const openAIQualityMigrationCooldown = 5 * time.Minute
const openAIQualityMigrationMax = 16384

// OpenAIStickyRebindCache replaces a binding only while its owner is unchanged.
// Both modern and legacy session hashes use the same atomic operation.
type OpenAIStickyRebindCache interface {
	CompareAndSwapSessionAccountID(context.Context, int64, string, int64, int64, time.Duration) (bool, error)
}

type openAIQualityMigrationKey struct {
	groupID int64
	session string
}
type openAIQualityMigrationEntry struct {
	key   openAIQualityMigrationKey
	until time.Time
}
type openAIQualityMigrationState struct {
	mu      sync.Mutex
	entries map[openAIQualityMigrationKey]*list.Element
	lru     list.List
}

func (s *openAIQualityMigrationState) coolingDown(key openAIQualityMigrationKey, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entries[key]; e != nil {
		entry, _ := e.Value.(openAIQualityMigrationEntry)
		if now.Before(entry.until) {
			s.lru.MoveToFront(e)
			return true
		}
		delete(s.entries, key)
		s.lru.Remove(e)
	}
	return false
}

func (s *openAIQualityMigrationState) note(key openAIQualityMigrationKey, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[openAIQualityMigrationKey]*list.Element)
	}
	entry := openAIQualityMigrationEntry{key, now.Add(openAIQualityMigrationCooldown)}
	if e := s.entries[key]; e != nil {
		e.Value = entry
		s.lru.MoveToFront(e)
		return
	}
	if len(s.entries) >= openAIQualityMigrationMax {
		e := s.lru.Back()
		entry, _ := e.Value.(openAIQualityMigrationEntry)
		delete(s.entries, entry.key)
		s.lru.Remove(e)
	}
	s.entries[key] = s.lru.PushFront(entry)
}

func (s *OpenAIGatewayService) migrateOpenAIQualityOwner(ctx context.Context, groupID *int64, platform, session, model string, owner int64, excluded map[int64]struct{}, compact bool, capability OpenAIEndpointCapability, preferLow bool, now time.Time) (int64, *AccountSelectionResult) {
	cache, ok := s.cache.(OpenAIStickyRebindCache)
	if !ok || session == "" || groupQualityGateExempt(ctx) || s.shareScheduling.shareSchedulingActive(derefGroupID(groupID)) {
		return owner, nil
	}
	if _, failed := excluded[owner]; failed {
		return owner, nil
	}
	key := openAIQualityMigrationKey{derefGroupID(groupID), session}
	cooldown := &s.getOpenAIAccountLatencyHoldState().migration
	if cooldown.coolingDown(key, now) {
		return owner, nil
	}
	view := s.GroupQualityGateView()
	trigger := "tier"
	if view.OrderTier(key.groupID, owner) != GroupQualityGateTierFail {
		acc, err := s.getSchedulableAccount(ctx, owner)
		if err != nil || acc == nil || !s.openAIAccountLatencyHoldSnapshot(acc, canonicalOpenAIAccountSchedulingModel(acc, model), now).Held {
			return owner, nil
		}
		trigger = "latency_hold"
	}
	accounts, err := s.listSchedulableAccounts(ctx, groupID, NormalizeOpenAICompatiblePlatform(platform))
	if err != nil {
		return owner, nil
	}
	gate := &defaultOpenAIAccountScheduler{service: s}
	req := OpenAIAccountScheduleRequest{GroupID: groupID, Platform: platform, SessionHash: session, RequestedModel: model, RequireCompact: compact, RequiredCapability: capability}
	if capacity, _ := ctx.Value(openAILegacyStickyCapacityKey{}).(*openAILegacyStickyCapacity); capacity != nil {
		req.RequiredTransport, req.RequiredImageCapability = capacity.transport, capacity.image
	}
	var candidates []accountWithLoad
	var loadsReq []AccountWithConcurrency
	for i := range accounts {
		a := &accounts[i]
		if a.ID == owner {
			continue
		}
		if _, skip := excluded[a.ID]; skip {
			continue
		}
		if !view.MigrationTarget(key.groupID, a.ID) || s.openAIAccountLatencyHoldSnapshot(a, canonicalOpenAIAccountSchedulingModel(a, model), now).Held {
			continue
		}
		fresh := s.recheckSelectedOpenAIAccountFromDB(ctx, a, groupID, platform, model, compact, capability)
		if fresh == nil || !s.openAIAccountMatchesSchedulingGroup(fresh, groupID) || !gate.isAccountRequestCompatible(ctx, fresh, req) || !gate.isAccountTransportCompatible(fresh, req.RequiredTransport) {
			continue
		}
		candidates = append(candidates, accountWithLoad{account: fresh})
		loadsReq = append(loadsReq, AccountWithConcurrency{ID: fresh.ID, MaxConcurrency: fresh.EffectiveLoadFactor()})
	}
	if len(candidates) == 0 {
		return owner, nil
	}
	if s.concurrencyService != nil {
		loads, err := s.concurrencyService.GetAccountsLoadBatchFresh(ctx, loadsReq)
		if err != nil {
			return owner, nil
		}
		eligible := candidates[:0]
		for _, item := range candidates {
			load := loads[item.account.ID]
			if load == nil || (item.account.Concurrency > 0 && load.CurrentConcurrency >= item.account.Concurrency) {
				continue
			}
			item.loadInfo = load
			eligible = append(eligible, item)
		}
		candidates = eligible
	}
	if len(candidates) == 0 {
		return owner, nil
	}
	rates := openAILegacyUpstreamRateOrder{}
	if preferLow {
		ptrs := make([]*Account, len(candidates))
		for i, item := range candidates {
			ptrs[i] = item.account
		}
		rates = newOpenAILegacyUpstreamRateOrder(ptrs, now, s.openAIOAuthSchedulingRateMultiplier(ctx))
		rates.enabled = true
	}
	order := s.openAICandidateOrderForItems(groupID, rates, candidates)
	sort.SliceStable(candidates, func(i, j int) bool { return openAICandidateLess(candidates[i], candidates[j], order) })
	if !rates.enabled {
		for i := range candidates {
			if candidates[i].loadInfo == nil {
				candidates[i].loadInfo = &AccountLoadInfo{AccountID: candidates[i].account.ID}
			}
		}
		shuffleOpenAIWithinQualityGroups(candidates, order.tiers, nil)
	}
	target := candidates[0].account
	slot, err := s.tryAcquireAccountSlot(ctx, target.ID, target.Concurrency)
	if err != nil || slot == nil || !slot.Acquired {
		return owner, nil
	}
	selection, err := s.newAcquiredSelectionResult(ctx, target, slot.ReleaseFunc)
	if err != nil {
		return owner, nil
	}
	latest, veto, _ := s.SelectionAdmissionVetoLatest(ctx, selection.Account)
	if veto || latest == nil || !view.Enabled(key.groupID) || !view.MigrationTarget(key.groupID, target.ID) || s.openAIAccountLatencyHoldSnapshot(target, canonicalOpenAIAccountSchedulingModel(target, model), time.Now()).Held {
		slot.ReleaseFunc()
		return owner, nil
	}
	changed, err := cache.CompareAndSwapSessionAccountID(ctx, key.groupID, s.openAISessionCacheKey(session), owner, target.ID, s.openAIStickyBindingTTL(groupID))
	if err != nil || !changed {
		slot.ReleaseFunc()
		return owner, nil
	}
	resilience.StateFrom(ctx).ClearKeptStickyIfOwner(owner)
	cooldown.note(key, time.Now())
	slog.Warn("group_quality_gate.migrate", "session_hash", shortSessionHash(session), "group_id", key.groupID, "from_account_id", owner, "to_account_id", target.ID, "trigger", trigger)
	return target.ID, selection
}
