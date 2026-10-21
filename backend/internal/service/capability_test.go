//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type capabilityRepo struct {
	AccountRepository
	mu       sync.Mutex
	accounts map[int64]*Account
	writes   int
}

func (r *capabilityRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.accounts[id]
	if a == nil {
		return nil, ErrAccountNotFound
	}
	copy := *a
	copy.Extra = map[string]any{}
	for k, v := range a.Extra {
		copy.Extra[k] = v
	}
	return &copy, nil
}
func (r *capabilityRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.accounts[id]
	if a == nil {
		return ErrAccountNotFound
	}
	if a.Extra == nil {
		a.Extra = map[string]any{}
	}
	for k, v := range updates {
		a.Extra[k] = v
	}
	r.writes++
	return nil
}
func capabilityTestAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example", "api_protocol": APIProtocolAdaptive}, Extra: map[string]any{"unrelated_setting": true}}
}

func TestCapabilityLearningLifecycleAndProtocolScope(t *testing.T) {
	withResilienceSettings(t, nil)
	a := capabilityTestAccount(1)
	repo := &capabilityRepo{accounts: map[int64]*Account{1: a}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	before, _ := repo.GetByID(context.Background(), 1)
	start := time.Now()
	failure := svc.LearnStreamOnlyFailure(context.Background(), before, "responses", false, 400, []byte(`{"error":{"message":"This group only accepts streaming requests"}}`))
	require.NotNil(t, failure)
	require.True(t, failure.IsCapabilityStreamOnly())
	require.True(t, failure.ShouldRetryNextAccount())
	require.False(t, failure.ShouldReportAccountScheduleFailure())
	require.False(t, failure.RetryableOnSameAccount)
	require.False(t, ClassifyFailoverError(failure).Category.CountsForSession())
	require.False(t, ClassifyFailoverError(failure).Category.AccountScoped())
	require.Empty(t, before.CapabilityLearned(time.Now()), "do not mutate a published scheduler snapshot")
	learned, _ := repo.GetByID(context.Background(), 1)
	records := learned.CapabilityLearned(start)
	require.Len(t, records, 1)
	require.Equal(t, "responses", records[0].Protocol)
	require.Equal(t, "stream_only", records[0].Mode)
	require.WithinDuration(t, start.Add(6*time.Hour), records[0].ExpiresAt, time.Second)
	require.True(t, learned.Extra["unrelated_setting"].(bool))
	ctx := resilience.WithCapabilityRequest(context.Background(), "responses", false)
	require.Equal(t, resilience.ReasonCapabilityStreamOnly, capabilityEligibilityReason(ctx, learned, "gpt-test", start))
	require.Empty(t, capabilityEligibilityReason(resilience.WithCapabilityRequest(context.Background(), "responses", true), learned, "gpt-test", start))
	require.Equal(t, resilience.ReasonCapabilityStreamOnly, capabilityEligibilityReason(resilience.WithCapabilityRequest(context.Background(), "chat", false), learned, "gpt-test", start), "a chat client routed to upstream Responses uses its Responses observation")
	require.Empty(t, capabilityEligibilityReason(context.Background(), learned, "gpt-test", start))
	require.Empty(t, learned.CapabilityLearned(records[0].ExpiresAt))
	require.Empty(t, capabilityEligibilityReason(ctx, learned, "gpt-test", records[0].ExpiresAt))
	changed := *learned
	changed.Credentials = map[string]any{"api_key": "new-test-key", "base_url": "https://upstream.example", "api_protocol": APIProtocolAdaptive}
	require.Empty(t, changed.CapabilityLearned(start), "a changed upstream identity invalidates old evidence")
	admin := &adminServiceImpl{accountRepo: repo}
	require.NoError(t, admin.ClearAccountCapabilityLearning(context.Background(), 1))
	cleared, _ := repo.GetByID(context.Background(), 1)
	require.Empty(t, cleared.CapabilityLearned(start))
	require.True(t, cleared.Extra["unrelated_setting"].(bool))
	require.ErrorIs(t, admin.ClearAccountCapabilityLearning(context.Background(), 999), ErrAccountNotFound)
}

func TestCapabilityLearningSwitchesAndUpstreamStreamMode(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		learning, decision, stream bool
		wantWrites                 int
		wantFailover               bool
	}{
		{"learning off", false, true, false, 0, false},
		{"decision off still learns", true, false, false, 1, false},
		{"upstream streaming unchanged", true, true, true, 0, false},
		{"enabled", true, true, false, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) {
				s.Capability.StreamOnlyLearning = tc.learning
				s.Failure.DecisionV2 = tc.decision
				s.Capability.LearnTTLSeconds = 600
			})
			a := capabilityTestAccount(1)
			repo := &capabilityRepo{accounts: map[int64]*Account{1: a}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			fail := svc.LearnStreamOnlyFailure(context.Background(), a, "chat", tc.stream, 400, []byte(`{"error":{"message":"stream must be true"}}`))
			require.Equal(t, tc.wantFailover, fail != nil)
			require.Equal(t, tc.wantWrites, repo.writes)
			if tc.wantWrites > 0 {
				require.WithinDuration(t, time.Now().Add(10*time.Minute), a.CapabilityLearned(time.Now())[0].ExpiresAt, time.Second)
			}
		})
	}
}

func TestCapabilitySelectionSkipsLearnedAndReportsNoAccountReason(t *testing.T) {
	withResilienceSettings(t, nil)
	a, b := capabilityTestAccount(1), capabilityTestAccount(2)
	b.Priority = 1
	repo := &capabilityRepo{accounts: map[int64]*Account{1: a, 2: b}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	require.NotNil(t, svc.LearnStreamOnlyFailure(context.Background(), a, "responses", false, 400, []byte(`{"error":{"code":"streaming_required"}}`)))
	ctx := resilience.WithCapabilityRequest(context.Background(), "responses", false)
	selected, _, stats := svc.selectBestAccount(ctx, nil, PlatformOpenAI, []Account{*a, *b}, "gpt-test", nil, false, "", false)
	require.NotNil(t, selected)
	require.Equal(t, int64(2), selected.ID)
	require.Contains(t, stats.summary(""), "capability_stream_only=1")
	selected, _, stats = svc.selectBestAccount(ctx, nil, PlatformOpenAI, []Account{*a}, "gpt-test", nil, false, "", false)
	require.Nil(t, selected)
	err := noAvailableOpenAISelectionError("gpt-test", false, stats.summary(""))
	require.True(t, errors.Is(err, ErrNoAvailableAccounts))
	require.Contains(t, err.Error(), "capability_stream_only")
	advanced := &defaultOpenAIAccountScheduler{service: svc}
	compatible, reason := advanced.isAccountRequestCompatibleReason(ctx, a, OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-test"})
	require.False(t, compatible)
	require.Equal(t, resilience.ReasonCapabilityStreamOnly, reason)
	selected, _, _ = svc.selectBestAccount(resilience.WithCapabilityRequest(context.Background(), "responses", true), nil, PlatformOpenAI, []Account{*a, *b}, "gpt-test", nil, false, "", false)
	require.NotNil(t, selected)
	require.Equal(t, int64(1), selected.ID)
}

func TestCapabilityManagedFieldsPreserveLatestDatabaseState(t *testing.T) {
	current := map[string]any{capabilityLearnedResponsesKey: map[string]any{"fingerprint": "current"}}
	stale := map[string]any{capabilityLearnedResponsesKey: map[string]any{"fingerprint": "stale"}, capabilityLearnedChatKey: map[string]any{"fingerprint": "injected"}, "unrelated_setting": true}
	merged := MergeCapabilityLearningExtra(stale, current)
	require.Equal(t, current[capabilityLearnedResponsesKey], merged[capabilityLearnedResponsesKey])
	require.NotContains(t, merged, capabilityLearnedChatKey)
	require.Equal(t, true, merged["unrelated_setting"])
	require.Equal(t, "stale", stale[capabilityLearnedResponsesKey].(map[string]any)["fingerprint"])
	imported := MergeCapabilityLearningExtra(stale, nil)
	require.NotContains(t, imported, capabilityLearnedResponsesKey)
	require.NotContains(t, imported, capabilityLearnedChatKey)
	duplicate, err := duplicateAccountExtra(stale)
	require.NoError(t, err)
	require.NotContains(t, duplicate, capabilityLearnedResponsesKey)
	require.NotContains(t, duplicate, capabilityLearnedChatKey)
}

func TestCapabilityNegativeCacheSeparatesStreamMode(t *testing.T) {
	withResilienceSettings(t, nil)
	group := int64(7)
	nonstream := resilience.WithCapabilityRequest(context.Background(), "responses", false)
	stream := resilience.WithCapabilityRequest(context.Background(), "responses", true)
	chat := resilience.WithCapabilityRequest(context.Background(), "chat", false)
	require.NotEqual(t, selectionNegativeKey(nonstream, &group, "model", "openai"), selectionNegativeKey(stream, &group, "model", "openai"))
	require.NotEqual(t, selectionNegativeKey(nonstream, &group, "model", "openai"), selectionNegativeKey(chat, &group, "model", "openai"))
}
