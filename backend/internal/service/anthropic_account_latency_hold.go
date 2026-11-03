package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type anthropicAttemptWarmKey struct{}
type anthropicLatencyModelKey struct{}

const (
	anthropicStickyMigrationCooldown = 30 * time.Minute
	anthropicStickyMigrationMax      = 16_384
)

type anthropicStickyMigrationKey struct {
	groupID     int64
	sessionHash string
}

// WithAnthropicLatencyModel supplies the channel-mapped model for hold lookups.
func WithAnthropicLatencyModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, anthropicLatencyModelKey{}, model)
}

func anthropicLatencyModel(ctx context.Context, requestedModel string) string {
	if model, ok := ctx.Value(anthropicLatencyModelKey{}).(string); ok {
		return model
	}
	return requestedModel
}

// WithAnthropicAttemptWarm carries sticky-owner evidence into Forward.
func WithAnthropicAttemptWarm(ctx context.Context, selection *AccountSelectionResult, priorFailure bool) context.Context {
	warm := selection != nil && selection.stickySessionHit && !selection.StickyCapacitySpillover && !priorFailure
	return context.WithValue(ctx, anthropicAttemptWarmKey{}, warm)
}

func anthropicAttemptWarmFromContext(ctx context.Context) bool {
	warm, _ := ctx.Value(anthropicAttemptWarmKey{}).(bool)
	return warm
}

func (s *GatewayService) getAnthropicLatencyHoldStateLocked(generation uint64) *openAIAccountLatencyHoldState {
	state := s.anthropicLatencyHold.Load()
	if state == nil {
		state = newOpenAIAccountLatencyHoldState(openAIModelTransientDefaultMax)
		s.anthropicLatencyHold.Store(state)
		s.anthropicLatencyHoldReset.Store(generation)
	}
	if generation != s.anthropicLatencyHoldReset.Load() {
		state.mu.Lock()
		state.entries = make(map[openAIAccountModelKey]openAIAccountLatencyHoldEntry)
		state.latestHoldUntilUnixNano.Store(0)
		state.mu.Unlock()
		s.anthropicStickyMigration = nil
		s.anthropicLatencyHoldReset.Store(generation)
	}
	return state
}

// anthropicLatencyStateForRead reads generation before settings. The second
// generation read rejects a concurrent disable/re-enable publication.
func (s *GatewayService) anthropicLatencyStateForRead() (*openAIAccountLatencyHoldState, bool) {
	if s == nil {
		return nil, false
	}
	generation := resilience.AnthropicLatencyHoldGeneration()
	settings := resilience.Current()
	if !settings.AnthropicLatencyHoldOn() {
		return nil, false
	}
	state := s.anthropicLatencyHold.Load()
	if state == nil {
		return nil, false
	}
	if s.anthropicLatencyHoldReset.Load() != generation {
		s.anthropicLatencyHoldMu.Lock()
		if s.anthropicLatencyHoldReset.Load() != generation {
			state = s.getAnthropicLatencyHoldStateLocked(generation)
		}
		s.anthropicLatencyHoldMu.Unlock()
	}
	return state, true
}

func (s *GatewayService) recordAnthropicAccountLatencySample(account *Account, model string, sample time.Duration, now time.Time) {
	if s == nil || account == nil {
		return
	}
	s.anthropicLatencyHoldMu.Lock()
	defer s.anthropicLatencyHoldMu.Unlock()
	generation := resilience.AnthropicLatencyHoldGeneration()
	settings := resilience.Current()
	if !settings.AnthropicLatencyHoldOn() || resilience.AnthropicLatencyHoldGeneration() != generation {
		return
	}
	state := s.getAnthropicLatencyHoldStateLocked(generation)
	entry, started, count, fraction := state.record(account.ID, openAIAccountModelTransientModel(model), sample, now)
	// If disable/re-enable raced with the write, discard all evidence from the
	// prior generation before it can be observed by a later request.
	latestGeneration := resilience.AnthropicLatencyHoldGeneration()
	if latestGeneration != generation || !resilience.Current().AnthropicLatencyHoldOn() {
		s.getAnthropicLatencyHoldStateLocked(latestGeneration)
		return
	}
	if started {
		slog.Warn("anthropic_account_latency_hold_started", "platform", account.Platform,
			"account_id", account.ID, "model", openAIAccountModelTransientModel(model),
			"sample_count", count, "slow_fraction", fraction,
			"hold_seconds", int(openAIAccountLatencyHoldDuration/time.Second), "hold_until", entry.holdUntil)
	}
}

func (s *GatewayService) recordAnthropicAttemptSuccess(ctx context.Context, account *Account) {
	if s == nil || account == nil || account.Platform != PlatformAnthropic || !resilience.Current().AnthropicLatencyHoldOn() {
		return
	}
	st := resilience.StateFrom(ctx)
	if st == nil || st.AccountFailed(account.ID) {
		return
	}
	profile := st.AttemptProfile()
	if profile.AccountID != account.ID || !profile.Stream || !profile.Warm {
		return
	}
	outputAt := st.SemanticOutputAt()
	if outputAt.IsZero() || profile.AttemptStart.IsZero() || !outputAt.After(profile.AttemptStart) {
		return
	}
	s.recordAnthropicAccountLatencySample(account, profile.Model, outputAt.Sub(profile.AttemptStart), time.Now())
}

func (s *GatewayService) anthropicLatencyHoldSnapshot(account *Account, requestedModel string, now time.Time) openAIAccountLatencyHoldSnapshot {
	if s == nil || account == nil {
		return openAIAccountLatencyHoldSnapshot{}
	}
	model, _ := resolveAnthropicOutboundModel(account, requestedModel)
	return s.anthropicLatencyHoldSnapshotForOutboundModel(account, model, now)
}

func (s *GatewayService) anthropicLatencyHoldSnapshotForOutboundModel(account *Account, model string, now time.Time) openAIAccountLatencyHoldSnapshot {
	if account == nil {
		return openAIAccountLatencyHoldSnapshot{}
	}
	state, active := s.anthropicLatencyStateForRead()
	if !active {
		return openAIAccountLatencyHoldSnapshot{}
	}
	return state.snapshot(account.ID, openAIAccountModelTransientModel(model), now)
}

func (s *GatewayService) anthropicLatencyHoldActive() bool {
	state, active := s.anthropicLatencyStateForRead()
	return active && state.latestHoldUntilUnixNano.Load() > time.Now().UnixNano()
}

func (s *GatewayService) anthropicStickyMigrationCoolingDown(groupID int64, sessionHash string, now time.Time) bool {
	if sessionHash == "" {
		return false
	}
	state, active := s.anthropicLatencyStateForRead()
	if !active || state == nil {
		return false
	}
	key := anthropicStickyMigrationKey{groupID: groupID, sessionHash: sessionHash}
	s.anthropicLatencyHoldMu.Lock()
	defer s.anthropicLatencyHoldMu.Unlock()
	generation := resilience.AnthropicLatencyHoldGeneration()
	if !resilience.Current().AnthropicLatencyHoldOn() {
		return false
	}
	if s.anthropicLatencyHoldReset.Load() != generation {
		s.getAnthropicLatencyHoldStateLocked(generation)
	}
	last, ok := s.anthropicStickyMigration[key]
	if !ok {
		return false
	}
	if now.Sub(last) >= anthropicStickyMigrationCooldown {
		delete(s.anthropicStickyMigration, key)
		return false
	}
	return true
}

func (s *GatewayService) noteAnthropicStickyMigration(groupID int64, sessionHash string, at time.Time) {
	if s == nil || sessionHash == "" {
		return
	}
	s.anthropicLatencyHoldMu.Lock()
	defer s.anthropicLatencyHoldMu.Unlock()
	generation := resilience.AnthropicLatencyHoldGeneration()
	if !resilience.Current().AnthropicLatencyHoldOn() || s.anthropicLatencyHoldReset.Load() != generation {
		return
	}
	if s.anthropicStickyMigration == nil {
		s.anthropicStickyMigration = make(map[anthropicStickyMigrationKey]time.Time)
	}
	key := anthropicStickyMigrationKey{groupID: groupID, sessionHash: sessionHash}
	if _, exists := s.anthropicStickyMigration[key]; !exists && len(s.anthropicStickyMigration) >= anthropicStickyMigrationMax {
		var oldestKey anthropicStickyMigrationKey
		var oldest time.Time
		found := false
		for candidateKey, migratedAt := range s.anthropicStickyMigration {
			if !found || migratedAt.Before(oldest) {
				oldestKey, oldest, found = candidateKey, migratedAt, true
			}
		}
		if found {
			delete(s.anthropicStickyMigration, oldestKey)
		}
	}
	s.anthropicStickyMigration[key] = at
}

func (s *GatewayService) partitionAnthropicLatencyHeldAccounts(accounts []*Account, requestedModel string) []*Account {
	if s == nil || !resilience.Current().AnthropicLatencyHoldOn() || len(accounts) < 2 {
		return accounts
	}
	if !s.anthropicLatencyHoldActive() {
		return accounts
	}
	held := make(map[int64]openAIAccountLatencyHoldSnapshot, len(accounts))
	for _, account := range accounts {
		model, _ := resolveAnthropicOutboundModel(account, requestedModel)
		if snap := s.anthropicLatencyHoldSnapshotForOutboundModel(account, model, time.Now()); snap.Held {
			held[account.ID] = snap
		}
	}
	return partitionLatencyHeldSegments(accounts, func(account *Account) *Account { return account }, held, func(account *Account) int {
		if account == nil {
			return 0
		}
		return account.Priority
	})
}

func (s *GatewayService) partitionAnthropicLatencyHeldLoad(items []accountWithLoad, requestedModel string) []accountWithLoad {
	if s == nil || !resilience.Current().AnthropicLatencyHoldOn() || len(items) < 2 {
		return items
	}
	if !s.anthropicLatencyHoldActive() {
		return items
	}
	held := make(map[int64]openAIAccountLatencyHoldSnapshot, len(items))
	for _, item := range items {
		model, _ := resolveAnthropicOutboundModel(item.account, requestedModel)
		if snap := s.anthropicLatencyHoldSnapshotForOutboundModel(item.account, model, time.Now()); snap.Held {
			held[item.account.ID] = snap
		}
	}
	return partitionLatencyHeldSegments(items, func(item accountWithLoad) *Account { return item.account }, held, func(account *Account) int {
		if account == nil {
			return 0
		}
		return account.Priority
	})
}

type anthropicStickyOwnerDelete interface {
	DeleteSessionAccountIDIfOwnerAndReport(context.Context, int64, string, int64) (bool, error)
}

func (s *GatewayService) migrateAnthropicLatencyHeldOwner(ctx context.Context, groupID *int64, sessionHash, requestedModel string, stickyID int64,
	accounts []*Account, routingIDs []int64, excluded map[int64]struct{}, eligible func(*Account, bool) bool, stickySnapshot openAIAccountLatencyHoldSnapshot) (int64, bool) {
	if stickyID <= 0 || sessionHash == "" || s == nil || !resilience.Current().AnthropicLatencyHoldOn() || !stickySnapshot.Held {
		return stickyID, false
	}
	var owner *Account
	for _, account := range accounts {
		if account.ID == stickyID {
			owner = account
			break
		}
	}
	if owner == nil {
		return stickyID, false
	}
	var alternativeID int64
	keep := func(reason string) (int64, bool) {
		model, _ := resolveAnthropicOutboundModel(owner, requestedModel)
		slog.Info("anthropic_account_latency_sticky_kept", "platform", PlatformAnthropic, "group_id", derefGroupID(groupID),
			"model", model, "from_account_id", owner.ID, "alternative_account_id", alternativeID,
			"reason", reason, "session", shortSessionHash(sessionHash))
		return stickyID, false
	}
	if !resilience.Current().SessionOn() {
		return keep("session_off")
	}
	if _, blocked := excluded[stickyID]; blocked {
		return stickyID, false
	}
	if len(routingIDs) > 0 && !containsInt64(routingIDs, stickyID) {
		return stickyID, false
	}
	if s.anthropicStickyMigrationCoolingDown(derefGroupID(groupID), sessionHash, time.Now()) {
		return keep("cooldown")
	}
	var targets []*Account
	for _, candidate := range accounts {
		if candidate.ID == stickyID || candidate.Priority != owner.Priority ||
			(len(routingIDs) > 0 && !containsInt64(routingIDs, candidate.ID)) || !eligible(candidate, false) ||
			s.anthropicLatencyHoldSnapshot(candidate, requestedModel, time.Now()).Held {
			continue
		}
		targets = append(targets, candidate)
	}
	if len(targets) == 0 {
		return keep("no_target")
	}
	if s.concurrencyService == nil {
		return keep("no_free_slot")
	}
	loadRequests := make([]AccountWithConcurrency, 0, len(targets))
	for _, candidate := range targets {
		loadRequests = append(loadRequests, AccountWithConcurrency{ID: candidate.ID, MaxConcurrency: candidate.Concurrency})
	}
	loads, err := s.concurrencyService.GetAccountsLoadBatchFresh(ctx, loadRequests)
	if err != nil {
		return keep("no_free_slot")
	}
	var target *Account
	for _, candidate := range targets {
		load := loads[candidate.ID]
		if load != nil && (candidate.Concurrency <= 0 || load.CurrentConcurrency < candidate.Concurrency) {
			target = candidate
			break
		}
	}
	if target == nil {
		return keep("no_free_slot")
	}
	alternativeID = target.ID
	cache, ok := s.cache.(anthropicStickyOwnerDelete)
	if !ok {
		return keep("owner_changed")
	}
	deleted, err := cache.DeleteSessionAccountIDIfOwnerAndReport(ctx, derefGroupID(groupID), sessionHash, stickyID)
	if err != nil || !deleted {
		return keep("owner_changed")
	}
	s.noteAnthropicStickyMigration(derefGroupID(groupID), sessionHash, time.Now())
	resilience.StateFrom(ctx).ClearKeptStickyIfOwner(stickyID)
	model, _ := resolveAnthropicOutboundModel(owner, requestedModel)
	slog.Warn("anthropic_account_latency_sticky_migrated", "platform", PlatformAnthropic, "group_id", derefGroupID(groupID),
		"model", model, "from_account_id", stickyID, "alternative_account_id", target.ID,
		"reason", "latency_hold", "session", shortSessionHash(sessionHash))
	return 0, true
}

// ReportAnthropicAttemptSuccess records a successful Claude attempt from its
// request context before billing detaches the request state.
func (s *GatewayService) ReportAnthropicAttemptSuccess(ctx context.Context, account *Account, result *ForwardResult) {
	if result == nil || result.ClientDisconnect {
		return
	}
	s.recordAnthropicAttemptSuccess(ctx, account)
}

func markAnthropicStickyHit(selection *AccountSelectionResult, platform string) {
	if selection != nil && platform == PlatformAnthropic {
		selection.stickySessionHit = true
	}
}
