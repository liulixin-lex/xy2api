//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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
	failure := svc.LearnStreamOnlyFailure(resilience.WithCapabilityRequest(context.Background(), "responses", false), before, "responses", false, 400, []byte(`{"error":{"message":"This group only accepts streaming requests"}}`))
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
	require.Empty(t, capabilityEligibilityReason(resilience.WithCapabilityRequest(context.Background(), "chat", false), learned, "gpt-test", start), "Chat to Responses forces upstream streaming")
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
			fail := svc.LearnStreamOnlyFailure(resilience.WithCapabilityRequest(context.Background(), "chat", false), a, "chat", tc.stream, 400, []byte(`{"error":{"message":"stream must be true"}}`))
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
	require.NotNil(t, svc.LearnStreamOnlyFailure(resilience.WithCapabilityRequest(context.Background(), "responses", false), a, "responses", false, 400, []byte(`{"error":{"code":"streaming_required"}}`)))
	ctx := resilience.WithCapabilityRequest(context.Background(), "responses", false)
	selected, _, stats := svc.selectBestAccount(ctx, nil, PlatformOpenAI, []Account{*a, *b}, "gpt-test", nil, false, "", false)
	require.NotNil(t, selected)
	require.Equal(t, int64(2), selected.ID)
	require.Contains(t, stats.summary(""), "capability_stream_only=1")
	selected, _, stats = svc.selectBestAccount(ctx, nil, PlatformOpenAI, []Account{*a}, "gpt-test", nil, false, "", false)
	require.Nil(t, selected)
	err := noAvailableOpenAISelectionError("gpt-test", false, stats.summary(""), stats.capabilityOnly())
	require.True(t, IsCapabilityOnlySelectionError(err))
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

func TestCapabilityLearningRequiresExplicitNonStreamingScope(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"missing scope", context.Background()},
		{"nil context", nil},
		{"unsupported protocol", resilience.WithCapabilityRequest(context.Background(), "messages", false)},
		{"streaming client", resilience.WithCapabilityRequest(context.Background(), "responses", true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := capabilityTestAccount(1)
			repo := &capabilityRepo{accounts: map[int64]*Account{1: a}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			failure := svc.LearnStreamOnlyFailure(tc.ctx, a, "responses", false, 400, []byte(`{"error":{"code":"streaming_required"}}`))
			require.Nil(t, failure)
			require.Zero(t, repo.writes, "unscoped paths must not learn")
			require.False(t, resilience.CapabilityExcluded(tc.ctx, a.ID))
		})
	}
}

func TestCapabilityForwardFailureSelectsAnotherAccountAndSucceeds(t *testing.T) {
	withResilienceSettings(t, nil)
	a, b := capabilityTestAccount(1), capabilityTestAccount(2)
	a.Platform, b.Platform = PlatformGrok, PlatformGrok
	b.Priority = 1
	repo := &capabilityRepo{accounts: map[int64]*Account{1: a, 2: b}}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"This group only accepts streaming requests"}}`))},
		{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_b","object":"response","model":"grok-4.6","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1}}`))},
	}}
	svc := &OpenAIGatewayService{accountRepo: repo, httpUpstream: upstream}
	body := []byte(`{"model":"grok-4.6","input":"hello","stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := resilience.WithCapabilityRequest(resilience.WithRequestState(context.Background(), resilience.NewRequestState()), "responses", false)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	c.Set("api_key", &APIKey{ID: 7})
	selected, _, _ := svc.selectBestAccount(ctx, nil, PlatformGrok, []Account{*a, *b}, "grok-4.6", nil, false, "", false)
	require.Equal(t, a.ID, selected.ID)
	_, err := svc.forwardGrokResponses(ctx, c, selected, body, "grok-4.6", false, time.Now())
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.True(t, failure.IsCapabilityStreamOnly())
	require.False(t, c.Writer.Written(), "the retryable 400 must not commit output")
	require.False(t, resilience.StateFrom(ctx).AccountFailed(a.ID))
	require.Nil(t, a.TempUnschedulableUntil)
	require.Nil(t, a.RateLimitResetAt)
	require.Len(t, a.CapabilityLearned(time.Now()), 1)
	selected, _, _ = svc.selectBestAccount(ctx, nil, PlatformGrok, []Account{*a, *b}, "grok-4.6", map[int64]struct{}{a.ID: {}}, false, "", false)
	require.Equal(t, b.ID, selected.ID)
	result, err := svc.forwardGrokResponses(ctx, c, selected, body, "grok-4.6", false, time.Now())
	require.NoError(t, err)
	require.Equal(t, "resp_b", result.ResponseID)
	require.Equal(t, 200, recorder.Code)
	require.Len(t, upstream.requests, 2)
	next := resilience.WithCapabilityRequest(context.Background(), "responses", false)
	selected, _, _ = svc.selectBestAccount(next, nil, PlatformGrok, []Account{*a, *b}, "grok-4.6", nil, false, "", false)
	require.Equal(t, b.ID, selected.ID)
	stream := resilience.WithCapabilityRequest(context.Background(), "responses", true)
	selected, _, _ = svc.selectBestAccount(stream, nil, PlatformGrok, []Account{*a, *b}, "grok-4.6", nil, false, "", false)
	require.Equal(t, a.ID, selected.ID)
}

func TestCapabilityLearningRejectsEchoedOutboundPhrase(t *testing.T) {
	withResilienceSettings(t, nil)
	a := capabilityTestAccount(1)
	repo := &capabilityRepo{accounts: map[int64]*Account{1: a}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := resilience.WithCapabilityRequest(context.Background(), "responses", false)
	failure := svc.LearnStreamOnlyFailure(ctx, a, "responses", false, 400,
		[]byte(`{"error":{"message":"Invalid value: 'only accepts streaming requests'"}}`),
		[]byte(`{"stream":false,"input":"ONLY ACCEPTS STREAMING REQUESTS"}`))
	require.Nil(t, failure)
	require.Zero(t, repo.writes)
}

func TestCapabilityFilteringRequiresSameUpstreamProtocol(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, platform, kind, inbound, upstream, learned string
		want                                             bool
	}{
		{"native Responses", PlatformOpenAI, AccountTypeAPIKey, "responses", APIProtocolResponses, "responses", true},
		{"raw Chat", PlatformOpenAI, AccountTypeAPIKey, "chat", APIProtocolChatCompletions, "chat", true},
		{"Chat to Responses streams", PlatformOpenAI, AccountTypeAPIKey, "chat", APIProtocolResponses, "responses", false},
		{"Responses to raw Chat conversion excluded from scope", PlatformOpenAI, AccountTypeAPIKey, "responses", APIProtocolChatCompletions, "chat", false},
		{"Grok native Responses", PlatformGrok, AccountTypeOAuth, "responses", "", "responses", true},
		{"Grok Chat bridge can force stream", PlatformGrok, AccountTypeOAuth, "chat", "", "chat", false},
		{"Grok API key raw Chat", PlatformGrok, AccountTypeAPIKey, "chat", "", "chat", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := capabilityTestAccount(1)
			a.Platform, a.Type = tc.platform, tc.kind
			a.Credentials["api_protocol"] = tc.upstream
			a.Extra["openai_responses_supported"] = tc.upstream != APIProtocolChatCompletions
			a.Extra[capabilityExtraKey(tc.learned)] = map[string]any{"fingerprint": capabilityFingerprint(a), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
			ctx := resilience.WithCapabilityRequest(context.Background(), tc.inbound, false)
			require.Equal(t, tc.want, a.CapabilityExclusion(ctx, "grok-4.6", time.Now()) != nil)
		})
	}
}

func TestCapabilityLegacyMixedExclusionsKeepGenericNoAccount(t *testing.T) {
	withResilienceSettings(t, nil)
	a, b := capabilityTestAccount(1), capabilityTestAccount(2)
	a.Extra["model_rate_limits"] = map[string]any{"gpt-test": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
	b.Schedulable = false
	svc := &OpenAIGatewayService{}
	selected, compactBlocked, stats := svc.selectBestAccount(context.Background(), nil, PlatformOpenAI, []Account{*a, *b}, "gpt-test", nil, false, "", false)
	require.Nil(t, selected)
	require.False(t, compactBlocked)
	require.Equal(t, "pool=2, filtered: ineligible=2", stats.summary(""))
	err := noAvailableOpenAISelectionError("gpt-test", compactBlocked, stats.summary(""))
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.NotContains(t, err.Error(), "model_rate_limited=", "the handler would otherwise turn a mixed 503 into 429")
}

func TestCapabilityOnlySelectionMarkerRequiresWholePool(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pool    int
		reasons map[string]int
		want    bool
	}{
		{"empty", 0, nil, false},
		{"all capability", 2, map[string]int{resilience.ReasonCapabilityStreamOnly: 2}, true},
		{"mixed", 2, map[string]int{resilience.ReasonCapabilityStreamOnly: 1, "ineligible": 1}, false},
		{"other gate also failed", 2, map[string]int{resilience.ReasonCapabilityStreamOnly: 2, "runtime_blocked": 1}, false},
		{"incomplete snapshot", 3, map[string]int{resilience.ReasonCapabilityStreamOnly: 2}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := openAISelectionFilterStats{pool: tc.pool, reasons: tc.reasons}
			err := noAvailableOpenAISelectionError("model", false, stats.summary(""), stats.capabilityOnly())
			require.Equal(t, tc.want, IsCapabilityOnlySelectionError(fmt.Errorf("selection: %w", err)))
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
		})
	}
	require.False(t, IsCapabilityOnlySelectionError(nil))
}
