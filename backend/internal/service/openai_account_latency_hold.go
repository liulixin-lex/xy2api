package service

import (
	"context"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"go.uber.org/zap"
)

const (
	openAIAccountLatencyHoldDecayTau  = 5 * time.Minute
	openAIAccountLatencyHoldDuration  = 3 * time.Minute
	openAIAccountLatencySlowThreshold = 10 * time.Second
	openAIAccountLatencyMinSamples    = 6
	openAIAccountLatencySlowFraction  = 0.35
)

type openAIAccountLatencyHoldEntry struct {
	weightedSamples float64
	weightedSlow    float64
	lastSample      time.Time
	holdUntil       time.Time
	lastTouched     time.Time
}

type openAIAccountLatencyHoldState struct {
	mu                      sync.Mutex
	entries                 map[openAIAccountModelKey]openAIAccountLatencyHoldEntry
	maxEntries              int
	latestHoldUntilUnixNano atomic.Int64
}

func newOpenAIAccountLatencyHoldState(maxEntries int) *openAIAccountLatencyHoldState {
	if maxEntries <= 0 {
		maxEntries = openAIModelTransientDefaultMax
	}
	return &openAIAccountLatencyHoldState{
		entries:    make(map[openAIAccountModelKey]openAIAccountLatencyHoldEntry),
		maxEntries: maxEntries,
	}
}

// openAIAccountLatencyHoldSnapshot is the scheduler-facing immutable view for one account.
type openAIAccountLatencyHoldSnapshot struct {
	Held      bool
	HoldUntil time.Time
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

func (s *openAIAccountLatencyHoldState) record(accountID int64, model string, sample time.Duration, now time.Time) (openAIAccountLatencyHoldEntry, bool, float64, float64) {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok || sample < 0 {
		return openAIAccountLatencyHoldEntry{}, false, 0, 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[openAIAccountModelKey]openAIAccountLatencyHoldEntry)
	}
	entry, exists := s.entries[key]
	if !exists {
		s.evictOldestLocked()
	}
	if !entry.lastSample.IsZero() {
		age := now.Sub(entry.lastSample)
		if age > 0 {
			factor := math.Exp(-float64(age) / float64(openAIAccountLatencyHoldDecayTau))
			entry.weightedSamples *= factor
			entry.weightedSlow *= factor
		}
	}
	entry.weightedSamples++
	if sample >= openAIAccountLatencySlowThreshold {
		entry.weightedSlow++
	}
	entry.lastSample = now
	entry.lastTouched = now
	started := false
	triggerSamples := entry.weightedSamples
	triggerSlowFraction := float64(0)
	if entry.weightedSamples > 0 {
		triggerSlowFraction = entry.weightedSlow / entry.weightedSamples
	}
	if entry.holdUntil.IsZero() || !now.Before(entry.holdUntil) {
		if entry.weightedSamples >= float64(openAIAccountLatencyMinSamples) && triggerSlowFraction >= openAIAccountLatencySlowFraction {
			entry.holdUntil = now.Add(openAIAccountLatencyHoldDuration)
			for {
				old := s.latestHoldUntilUnixNano.Load()
				if old >= entry.holdUntil.UnixNano() || s.latestHoldUntilUnixNano.CompareAndSwap(old, entry.holdUntil.UnixNano()) {
					break
				}
			}
			entry.weightedSamples = 0
			entry.weightedSlow = 0
			entry.lastSample = time.Time{}
			started = true
		}
	}
	s.entries[key] = entry
	return entry, started, triggerSamples, triggerSlowFraction
}

func (s *openAIAccountLatencyHoldState) snapshot(accountID int64, model string, now time.Time) openAIAccountLatencyHoldSnapshot {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return openAIAccountLatencyHoldSnapshot{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return openAIAccountLatencyHoldSnapshot{}
	}
	if !entry.holdUntil.IsZero() && now.Before(entry.holdUntil) {
		entry.lastTouched = now
		s.entries[key] = entry
		return openAIAccountLatencyHoldSnapshot{Held: true, HoldUntil: entry.holdUntil}
	}
	if !entry.holdUntil.IsZero() {
		entry.holdUntil = time.Time{}
		entry.lastTouched = now
		s.entries[key] = entry
	}
	return openAIAccountLatencyHoldSnapshot{}
}

func (s *openAIAccountLatencyHoldState) evictOldestLocked() {
	if len(s.entries) < s.maxEntries {
		return
	}
	var oldestKey openAIAccountModelKey
	var oldest time.Time
	found := false
	for key, entry := range s.entries {
		if !found || entry.lastTouched.Before(oldest) {
			oldestKey, oldest, found = key, entry.lastTouched, true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
}

func (s *OpenAIGatewayService) getOpenAIAccountLatencyHoldState() *openAIAccountLatencyHoldState {
	if s == nil {
		return nil
	}
	s.openaiAccountLatencyHoldOnce.Do(func() {
		if s.openaiAccountLatencyHold == nil {
			s.openaiAccountLatencyHold = newOpenAIAccountLatencyHoldState(openAIModelTransientDefaultMax)
		}
	})
	return s.openaiAccountLatencyHold
}

// recordOpenAIAccountLatencySample records one warm, successful HTTP text
// stream's first-content delay (first delivered content - attempt start).
func (s *OpenAIGatewayService) recordOpenAIAccountLatencySample(account *Account, canonicalModel string, sample time.Duration, now time.Time) {
	if s == nil || account == nil || !resilience.Current().Enabled {
		return
	}
	state := s.getOpenAIAccountLatencyHoldState()
	entry, started, triggerSamples, triggerSlowFraction := state.record(account.ID, openAIAccountModelTransientModel(canonicalModel), sample, now)
	if started {
		logger.L().Info("openai_account_latency_hold_started",
			zap.Int64("account_id", account.ID),
			zap.String("model", openAIAccountModelTransientModel(canonicalModel)),
			zap.Float64("sample_count", triggerSamples),
			zap.Float64("slow_fraction", triggerSlowFraction),
			zap.Int("hold_seconds", int(openAIAccountLatencyHoldDuration/time.Second)),
			zap.Time("hold_until", entry.holdUntil),
		)
	}
}

func (s *OpenAIGatewayService) openAIAccountLatencyHoldSnapshots(accounts []Account, requestedModel string, now time.Time) map[int64]openAIAccountLatencyHoldSnapshot {
	result := make(map[int64]openAIAccountLatencyHoldSnapshot, len(accounts))
	if s == nil {
		return result
	}
	state := s.getOpenAIAccountLatencyHoldState()
	if state == nil {
		return result
	}
	if now.IsZero() {
		now = time.Now()
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for i := range accounts {
		account := &accounts[i]
		model := openAIAccountModelTransientModel(canonicalOpenAIAccountSchedulingModel(account, requestedModel))
		key, ok := openAIAccountModelTransientKey(account.ID, model)
		if !ok {
			continue
		}
		entry, exists := state.entries[key]
		if !exists {
			continue
		}
		if !entry.holdUntil.IsZero() && now.Before(entry.holdUntil) {
			entry.lastTouched = now
			state.entries[key] = entry
			result[account.ID] = openAIAccountLatencyHoldSnapshot{Held: true, HoldUntil: entry.holdUntil}
		} else if !entry.holdUntil.IsZero() {
			entry.holdUntil = time.Time{}
			entry.lastTouched = now
			state.entries[key] = entry
		}
	}
	return result
}

func (s *OpenAIGatewayService) partitionOpenAILatencyHeldAccounts(_ context.Context, accounts []*Account, requestedModel string, compact bool) []*Account {
	if s == nil || !resilience.Current().Enabled || s.getOpenAIAccountLatencyHoldState().latestHoldUntilUnixNano.Load() <= time.Now().UnixNano() {
		return accounts
	}
	return partitionLatencyHeld(accounts, func(account *Account) *Account { return account }, s.openAIAccountLatencyHoldSnapshotForAccounts(accounts, requestedModel), compact)
}

func (s *OpenAIGatewayService) partitionOpenAILatencyHeldLoad(_ context.Context, items []accountWithLoad, requestedModel string, compact bool) []accountWithLoad {
	if s == nil || !resilience.Current().Enabled || s.getOpenAIAccountLatencyHoldState().latestHoldUntilUnixNano.Load() <= time.Now().UnixNano() {
		return items
	}
	accounts := make([]*Account, 0, len(items))
	for _, item := range items {
		accounts = append(accounts, item.account)
	}
	return partitionLatencyHeld(items, func(item accountWithLoad) *Account { return item.account }, s.openAIAccountLatencyHoldSnapshotForAccounts(accounts, requestedModel), compact)
}

func partitionLatencyHeld[T any](items []T, accountOf func(T) *Account, held map[int64]openAIAccountLatencyHoldSnapshot, compact bool) []T {
	if len(items) < 2 || len(held) == 0 {
		return items
	}
	partition := func(segment []T) []T {
		healthy := make([]T, 0, len(segment))
		heldItems := make([]T, 0, len(segment))
		for _, item := range segment {
			account := accountOf(item)
			if account != nil && held[account.ID].Held {
				heldItems = append(heldItems, item)
			} else {
				healthy = append(healthy, item)
			}
		}
		return append(healthy, heldItems...)
	}
	if !compact {
		return partition(items)
	}
	result := make([]T, 0, len(items))
	start := 0
	for start < len(items) {
		tier := openAICompactSupportTier(accountOf(items[start]))
		end := start + 1
		for end < len(items) && openAICompactSupportTier(accountOf(items[end])) == tier {
			end++
		}
		result = append(result, partition(items[start:end])...)
		start = end
	}
	return result
}

func (s *OpenAIGatewayService) openAIAccountLatencyHoldSnapshotForAccounts(accounts []*Account, requestedModel string) map[int64]openAIAccountLatencyHoldSnapshot {
	if s == nil || len(accounts) == 0 {
		return nil
	}
	latest := s.getOpenAIAccountLatencyHoldState().latestHoldUntilUnixNano.Load()
	now := time.Now()
	if latest == 0 || now.UnixNano() >= latest {
		return nil
	}
	keys := make([]openAIAccountModelKey, len(accounts))
	for i, account := range accounts {
		if account != nil {
			keys[i], _ = openAIAccountModelTransientKey(account.ID, openAIAccountModelTransientModel(canonicalOpenAIAccountSchedulingModel(account, requestedModel)))
		}
	}
	state := s.getOpenAIAccountLatencyHoldState()
	result := make(map[int64]openAIAccountLatencyHoldSnapshot)
	state.mu.Lock()
	defer state.mu.Unlock()
	for i, key := range keys {
		if key.AccountID == 0 {
			continue
		}
		if entry, ok := state.entries[key]; ok && now.Before(entry.holdUntil) {
			result[accounts[i].ID] = openAIAccountLatencyHoldSnapshot{Held: true, HoldUntil: entry.holdUntil}
		}
	}
	return result
}

func (s *OpenAIGatewayService) migrateOpenAILatencyHeldOwner(ctx context.Context, groupID *int64, platform, sessionHash, requestedModel string, stickyAccountID int64, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability) int64 {
	if stickyAccountID <= 0 || openAINoStickyMigration(ctx) || s == nil || !resilience.Current().SessionOn() || s.qualityOwnsSticky(ctx, groupID, sessionHash) {
		return stickyAccountID
	}
	state := s.getOpenAIAccountLatencyHoldState()
	if state == nil || state.latestHoldUntilUnixNano.Load() <= time.Now().UnixNano() {
		return stickyAccountID
	}
	now := time.Now()
	ownerSnapshot := openAIAccountLatencyHoldSnapshot{}
	var ownerAccount *Account
	ok := false
	if account, err := s.getSchedulableAccount(ctx, stickyAccountID); err != nil || account == nil {
		return stickyAccountID
	} else {
		ownerAccount = account
		ownerSnapshot = s.openAIAccountLatencyHoldSnapshot(account, canonicalOpenAIAccountSchedulingModel(account, requestedModel), now)
		ok = ownerSnapshot.Held
	}
	if !ok {
		return stickyAccountID
	}
	accounts, err := s.listSchedulableAccounts(ctx, groupID, NormalizeOpenAICompatiblePlatform(platform))
	if err != nil {
		return stickyAccountID
	}
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	for i := range accounts {
		candidate := &accounts[i]
		if candidate.ID == stickyAccountID {
			continue
		}
		if _, excluded := excludedIDs[candidate.ID]; excluded {
			continue
		}
		if !isOpenAICompatibleAccountEligibleForRequest(ctx, candidate, platform, requestedModel, requireCompact, requiredCapability) {
			continue
		}
		if needsUpstreamCheck && groupID != nil && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, candidate, requestedModel, requireCompact) {
			continue
		}
		if s.openAIAccountLatencyHoldSnapshot(candidate, canonicalOpenAIAccountSchedulingModel(candidate, requestedModel), now).Held {
			continue
		}
		if !parentHealthyForShadow(candidate, s.parentAccountLookup(ctx)) || s.isOpenAIAccountRequestRuntimeBlocked(candidate, requestedModel, requireCompact) {
			continue
		}
		if vetoed, _ := openAIProfitControlVetoReason(ctx, candidate); vetoed {
			continue
		}
		if err := s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, stickyAccountID); err != nil {
			continue
		}
		resilience.StateFrom(ctx).ClearKeptStickyIfOwner(stickyAccountID)
		logger.L().Info("openai_account_latency_sticky_migrated",
			zap.Int64("from_account_id", stickyAccountID),
			zap.Int64("alternative_account_id", candidate.ID),
			zap.String("model", canonicalOpenAIAccountSchedulingModel(ownerAccount, requestedModel)),
		)
		return 0
	}
	return stickyAccountID
}

func (s *OpenAIGatewayService) openAIAccountLatencyHoldSnapshot(account *Account, canonicalModel string, now time.Time) openAIAccountLatencyHoldSnapshot {
	if s == nil || account == nil {
		return openAIAccountLatencyHoldSnapshot{}
	}
	return s.getOpenAIAccountLatencyHoldState().snapshot(account.ID, openAIAccountModelTransientModel(canonicalModel), now)
}
