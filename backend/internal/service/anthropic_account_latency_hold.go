package service

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type anthropicAttemptWarmKey struct{}
type anthropicLatencyModelKey struct{}

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

func (s *GatewayService) getAnthropicLatencyHoldStateLocked(reset uint64) *openAIAccountLatencyHoldState {
	if s.anthropicLatencyHold == nil {
		s.anthropicLatencyHold = newOpenAIAccountLatencyHoldState(openAIModelTransientDefaultMax)
		s.anthropicLatencyHoldReset = reset
	}
	if reset != s.anthropicLatencyHoldReset {
		s.anthropicLatencyHold.mu.Lock()
		s.anthropicLatencyHold.entries = make(map[openAIAccountModelKey]openAIAccountLatencyHoldEntry)
		s.anthropicLatencyHold.latestHoldUntilUnixNano.Store(0)
		s.anthropicLatencyHold.mu.Unlock()
		s.anthropicLatencyHoldReset = reset
	}
	return s.anthropicLatencyHold
}

func (s *GatewayService) recordAnthropicAccountLatencySample(account *Account, model string, sample time.Duration, now time.Time) {
	if s == nil || account == nil {
		return
	}
	resilience.WithAnthropicLatencyHoldSnapshot(func(settings *resilience.Settings, reset uint64) {
		if !settings.AnthropicLatencyHoldOn() {
			return
		}
		s.anthropicLatencyHoldMu.Lock()
		defer s.anthropicLatencyHoldMu.Unlock()
		state := s.getAnthropicLatencyHoldStateLocked(reset)
		entry, started, count, fraction := state.record(account.ID, openAIAccountModelTransientModel(model), sample, now)
		if started {
			slog.Warn("anthropic_account_latency_hold_started", "platform", account.Platform,
				"account_id", account.ID, "model", openAIAccountModelTransientModel(model),
				"sample_count", count, "slow_fraction", fraction,
				"hold_seconds", int(openAIAccountLatencyHoldDuration/time.Second), "hold_until", entry.holdUntil)
		}
	})
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
	var snapshot openAIAccountLatencyHoldSnapshot
	resilience.WithAnthropicLatencyHoldSnapshot(func(settings *resilience.Settings, reset uint64) {
		if !settings.AnthropicLatencyHoldOn() {
			return
		}
		s.anthropicLatencyHoldMu.Lock()
		defer s.anthropicLatencyHoldMu.Unlock()
		state := s.getAnthropicLatencyHoldStateLocked(reset)
		snapshot = state.snapshot(account.ID, openAIAccountModelTransientModel(model), now)
	})
	return snapshot
}

func (s *GatewayService) anthropicLatencyFreshSampleCount(account *Account, requestedModel string, now time.Time) float64 {
	if s == nil || account == nil {
		return 0
	}
	model, _ := resolveAnthropicOutboundModel(account, requestedModel)
	key, ok := openAIAccountModelTransientKey(account.ID, model)
	if !ok {
		return 0
	}
	var samples float64
	resilience.WithAnthropicLatencyHoldSnapshot(func(settings *resilience.Settings, reset uint64) {
		if !settings.AnthropicLatencyHoldOn() {
			return
		}
		s.anthropicLatencyHoldMu.Lock()
		defer s.anthropicLatencyHoldMu.Unlock()
		state := s.getAnthropicLatencyHoldStateLocked(reset)
		state.mu.Lock()
		defer state.mu.Unlock()
		entry, exists := state.entries[key]
		if !exists || entry.lastSample.IsZero() {
			return
		}
		age := now.Sub(entry.lastSample)
		if age <= 0 {
			samples = entry.weightedSamples
			return
		}
		samples = entry.weightedSamples * math.Exp(-float64(age)/float64(openAIAccountLatencyHoldDecayTau))
	})
	return samples
}

func (s *GatewayService) anthropicLatencyHoldActive() bool {
	if s == nil {
		return false
	}
	active := false
	resilience.WithAnthropicLatencyHoldSnapshot(func(settings *resilience.Settings, reset uint64) {
		if !settings.AnthropicLatencyHoldOn() {
			return
		}
		s.anthropicLatencyHoldMu.Lock()
		defer s.anthropicLatencyHoldMu.Unlock()
		active = s.getAnthropicLatencyHoldStateLocked(reset).latestHoldUntilUnixNano.Load() > time.Now().UnixNano()
	})
	return active
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
		if snap := s.anthropicLatencyHoldSnapshot(account, requestedModel, time.Now()); snap.Held {
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
	accounts := make([]*Account, 0, len(items))
	for _, item := range items {
		accounts = append(accounts, item.account)
	}
	held := make(map[int64]openAIAccountLatencyHoldSnapshot, len(accounts))
	for _, account := range accounts {
		if snap := s.anthropicLatencyHoldSnapshot(account, requestedModel, time.Now()); snap.Held {
			held[account.ID] = snap
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
	var targets []*Account
	insufficientSamples := false
	for _, candidate := range accounts {
		if candidate.ID == stickyID || candidate.Priority != owner.Priority || !eligible(candidate, false) ||
			(len(routingIDs) > 0 && !containsInt64(routingIDs, candidate.ID)) ||
			s.anthropicLatencyHoldSnapshot(candidate, requestedModel, time.Now()).Held {
			continue
		}
		if s.anthropicLatencyFreshSampleCount(candidate, requestedModel, time.Now()) < float64(openAIAccountLatencyMinSamples) {
			insufficientSamples = true
			continue
		}
		targets = append(targets, candidate)
	}
	if len(targets) == 0 {
		if insufficientSamples {
			return keep("insufficient_samples")
		}
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
