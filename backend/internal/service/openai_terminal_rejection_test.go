//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const terminalRejectionBody = `{"error":{"type":"invalid_request_error","code":"provider_request_invalid","message":"Input exceeds provider limit"}}`

func terminalRejectionTestService() (*OpenAIGatewayService, *memResilienceStore, *atomicStickyTestCache) {
	store := newMemResilienceStore()
	cache := &atomicStickyTestCache{bindings: make(map[string]int64)}
	repo := &openAIModelAvailabilityRepo{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: repo, cache: cache,
		resilience: NewResilienceService(nil, store), rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
	return svc, store, cache
}

func terminalRejectionTestRequest(t *testing.T, svc *OpenAIGatewayService, account *Account, body string, compat bool) *resilience.RequestState {
	t.Helper()
	c, recorder := newOpenAIUpstreamErrorTestContext(t)
	state := resilience.NewRequestState()
	state.SetSession(9, "v2:rejection-session", svc.openAISessionRecorder())
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
	upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamErrorResponse(400, body)}
	svc.httpUpstream = upstream
	var err error
	if compat {
		_, err = svc.handleChatCompletionsErrorResponse(upstream.resp, c, account)
	} else {
		_, err = svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"requested-model","stream":false,"input":"hello"}`))
		require.Len(t, upstream.bodies, 1, "avoidance must not replay this request")
	}
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	_, _, healthFailure := classifyOpenAIAPIKeyHealthFailure(err)
	require.False(t, healthFailure)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	return state
}

func terminalRejectionTestAccount(id int64, host string) *Account {
	account := openAIFailoverCachedBodyTestAccount(id, "account", nil)
	account.Type = AccountTypeAPIKey
	account.Credentials["api_key"] = "test-key"
	account.Credentials["base_url"] = "https://" + host + "/v1"
	account.Extra = map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff}
	return account
}

func TestOpenAITerminalRejectionAvoidsAfterThreshold(t *testing.T) {
	withResilienceSettings(t, func(cfg *resilience.Settings) { cfg.Session.StreakThreshold = 3 })
	for _, path := range []string{"native", "passthrough", "chat"} {
		t.Run(path, func(t *testing.T) {
			svc, store, cache := terminalRejectionTestService()
			account := terminalRejectionTestAccount(1, "relay-a.example")
			account.Extra["openai_passthrough"] = path == "passthrough"
			key := svc.openAISessionCacheKey("v2:rejection-session")
			cache.bindings[key] = account.ID
			for i := 1; i <= resilience.Current().Session.StreakThreshold; i++ {
				body := terminalRejectionBody
				if i == 2 {
					body = `{"error":{"type":"invalid_request_error","code":"provider_request_invalid","message":"  INPUT  exceeds\nprovider limit  "}}`
				}
				state := terminalRejectionTestRequest(t, svc, account, body, path == "chat")
				avoid, err := store.SessionAvoid(t.Context(), 9, "v2:rejection-session")
				require.NoError(t, err)
				require.Equal(t, i == resilience.Current().Session.StreakThreshold, state.Avoided(account.ID))
				if i < resilience.Current().Session.StreakThreshold {
					require.Empty(t, avoid)
					require.Equal(t, account.ID, cache.bindings[key])
				} else {
					require.Contains(t, avoid, account.ID)
					require.NotContains(t, cache.bindings, key)
				}
			}
			failures, err := store.SessionFailures(t.Context(), 9, "v2:rejection-session")
			require.NoError(t, err)
			require.Empty(t, failures, "rejections must not advance health/session failure counts")
			repo, ok := svc.accountRepo.(*openAIModelAvailabilityRepo)
			require.True(t, ok)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.Empty(t, repo.modelRateLimitCalls)
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
		})
	}
}

func TestOpenAITerminalRejectionKnownCodesAndPoliciesDoNotAvoid(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, sample := range []struct {
		status int
		body   string
	}{
		{400, `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Input exceeds limit"}}`},
		{400, `{"error":{"code":"invalid_request_error","message":"Invalid input"}}`},
		{400, `{"error":{"code":"content_policy_violation","message":"Rejected"}}`},
		{400, `{"error":{"code":"cyber_policy","message":"Rejected"}}`},
		{400, `{"error":{"code":"bio_policy","message":"Rejected"}}`},
		{400, `{"error":{"type":"invalid_request_error","code":"provider_block","message":"Content policy rejected request"}}`},
		{401, terminalRejectionBody},
		{403, terminalRejectionBody},
	} {
		t.Run(fmt.Sprintf("%d/%s", sample.status, sample.body), func(t *testing.T) {
			svc, store, _ := terminalRejectionTestService()
			state := resilience.NewRequestState()
			state.SetSession(9, "v2:rejection-session", svc.openAISessionRecorder())
			ctx := resilience.WithRequestState(t.Context(), state)
			for range resilience.Current().Session.StreakThreshold + 1 {
				svc.noteOpenAITerminalRejection(ctx, terminalRejectionTestAccount(1, "relay-a.example"), sample.status, nil, []byte(sample.body), errors.New("terminal"))
			}
			avoid, err := store.SessionAvoid(ctx, 9, "v2:rejection-session")
			require.NoError(t, err)
			require.Empty(t, avoid)
		})
	}
}

func TestOpenAITerminalRejectionNormalizesVariableRequestIDs(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, store, _ := terminalRejectionTestService()
	account := terminalRejectionTestAccount(1, "relay-a.example")
	ctx := resilience.WithRequestState(t.Context(), func() *resilience.RequestState {
		state := resilience.NewRequestState()
		state.SetSession(9, "v2:rejection-session", svc.openAISessionRecorder())
		return state
	}())
	for i := range resilience.Current().Session.StreakThreshold {
		requestID, count := "req-14928", "32"
		if i%2 == 1 {
			requestID, count = "req-90371", "64"
		}
		body := []byte(fmt.Sprintf(`{"error":{"type":"invalid_request_error","code":"provider_request_invalid","message":"Input exceeds %s MiB; request id: %s"}}`, count, requestID))
		svc.noteOpenAITerminalRejection(ctx, account, http.StatusBadRequest, nil, body, errors.New("terminal"))
	}
	avoid, err := store.SessionAvoid(ctx, 9, "v2:rejection-session")
	require.NoError(t, err)
	require.Contains(t, avoid, account.ID)
}

func TestOpenAITerminalRejectionMessageOnlyContextLimitDoesNotAvoid(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, store, _ := terminalRejectionTestService()
	account := terminalRejectionTestAccount(1, "relay-a.example")
	ctx := resilience.WithRequestState(t.Context(), func() *resilience.RequestState {
		state := resilience.NewRequestState()
		state.SetSession(9, "v2:rejection-session", svc.openAISessionRecorder())
		return state
	}())
	body := []byte(`{"error":{"type":"invalid_request_error","code":"provider_request_invalid","message":"Maximum context length is 32768 tokens; request cannot continue"}}`)
	for range resilience.Current().Session.StreakThreshold + 1 {
		svc.noteOpenAITerminalRejection(ctx, account, http.StatusBadRequest, nil, body, errors.New("terminal"))
	}
	avoid, err := store.SessionAvoid(ctx, 9, "v2:rejection-session")
	require.NoError(t, err)
	require.Empty(t, avoid)
}

func TestOpenAITerminalRejectionStopsAvoidingAcrossHosts(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, store, _ := terminalRejectionTestService()
	first := terminalRejectionTestAccount(1, "relay-a.example")
	second := terminalRejectionTestAccount(2, "relay-b.example")
	for range resilience.Current().Session.StreakThreshold {
		terminalRejectionTestRequest(t, svc, first, terminalRejectionBody, false)
	}
	for range resilience.Current().Session.StreakThreshold + 1 {
		terminalRejectionTestRequest(t, svc, second, terminalRejectionBody, false)
	}
	avoid, err := store.SessionAvoid(t.Context(), 9, "v2:rejection-session")
	require.NoError(t, err)
	require.Contains(t, avoid, first.ID)
	require.NotContains(t, avoid, second.ID)
	before := store.avoid[memSessionKey(9, "v2:rejection-session")][first.ID]
	for range resilience.Current().Session.StreakThreshold {
		terminalRejectionTestRequest(t, svc, first, terminalRejectionBody, false)
	}
	require.Equal(t, before, store.avoid[memSessionKey(9, "v2:rejection-session")][first.ID], "cross-host evidence also stops extending the first account's avoidance")
}

func TestOpenAITerminalRejectionStreakResets(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, store, _ := terminalRejectionTestService()
	account := terminalRejectionTestAccount(1, "relay-a.example")
	terminalRejectionTestRequest(t, svc, account, terminalRejectionBody, false)
	svc.openAISessionRecorder().Success(context.Background(), 9, "v2:rejection-session", account.ID)
	terminalRejectionTestRequest(t, svc, account, terminalRejectionBody, false)
	terminalRejectionTestRequest(t, svc, account, `{"error":{"type":"invalid_request_error","code":"other_restriction","message":"Different limit"}}`, false)
	terminalRejectionTestRequest(t, svc, account, terminalRejectionBody, false)
	avoid, err := store.SessionAvoid(t.Context(), 9, "v2:rejection-session")
	require.NoError(t, err)
	require.Empty(t, avoid)
}

func TestOpenAITerminalRejectionEvidenceExpiresAndIsBounded(t *testing.T) {
	var tracker openAITerminalRejectionTracker
	cfg := resilience.Defaults().Session
	cfg.StreakWindowSeconds, cfg.AvoidSeconds = 1, 2
	now := time.Now()
	digest, eligible := openAITerminalRejectionDigest(400, nil, []byte(terminalRejectionBody))
	require.True(t, eligible)
	_, avoid := tracker.record(9, "session", 1, "relay-a.example", digest, now, cfg)
	require.False(t, avoid)
	n, avoid := tracker.record(9, "session", 1, "relay-a.example", digest, now.Add(1500*time.Millisecond), cfg)
	require.Equal(t, 1, n)
	require.False(t, avoid)
	_, avoid = tracker.record(9, "session", 2, "relay-b.example", digest, now.Add(2*time.Second), cfg)
	require.False(t, avoid)
	_, avoid = tracker.record(9, "session", 2, "relay-b.example", digest, now.Add(2500*time.Millisecond), cfg)
	require.False(t, avoid, "host guard lasts for at least the avoidance period")
	for i := range openAITerminalRejectionCapacity {
		tracker.record(9, strconv.Itoa(i), 1, "relay-a.example", digest, now, cfg)
	}
	require.Len(t, tracker.entries, openAITerminalRejectionCapacity)
	require.Equal(t, openAITerminalRejectionCapacity, tracker.lru.Len())
}

func TestOpenAITerminalRejectionScopeKeepsParentAffinityAvailable(t *testing.T) {
	withResilienceSettings(t, nil)
	groupID := int64(102091)
	parentID := "parent-session-id"
	childID := "child-session-id"
	accounts := []Account{
		{ID: 39101, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "team"}},
		{ID: 39102, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "team"}},
	}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	store := newMemResilienceStore()
	repo := schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}
	cache := &schedulerTestGatewayCache{sessionBindings: make(map[string]int64)}
	svc := &OpenAIGatewayService{
		cfg: cfg, accountRepo: repo, cache: cache, resilience: NewResilienceService(nil, store),
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("false", "false"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{39101: true, 39102: true}}),
	}

	child, _ := newOpenAIUpstreamErrorTestContext(t)
	child.Request.Header.Set("session-id", parentID)
	child.Request.Header.Set(openAIWSThreadIDHeader, childID)
	child.Request.Header.Set(codexParentThreadIDHeader, parentID)
	child.Request.Header.Set(openAISubagentHeader, "collab_spawn")
	body := []byte(`{"model":"gpt-6-sol","input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"cipher"}]}]}`)
	parentHash := svc.GenerateSessionHash(child, body)
	cache.sessionBindings[svc.openAISessionCacheKey(parentHash)] = 39101
	ctx := svc.WithOpenAIGuardianParentAffinity(child.Request.Context(), child, body, "gpt-6-sol")
	scope := openAIRejectionScope(ctx).session
	require.NotEmpty(t, scope)
	require.NotEqual(t, parentHash, scope)
	svc.AvoidOpenAIStateAccount(ctx, groupID, scope, 39101)

	selectedChild, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", parentHash, "gpt-6-sol",
		nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selectedChild)
	require.Equal(t, int64(39102), selectedChild.Account.ID,
		"the child scope must carry avoidance into parent-affinity selection")
	require.Equal(t, int64(39101), cache.sessionBindings[svc.openAISessionCacheKey(parentHash)],
		"the child rejection must not delete or replace the shared parent binding")
	if selectedChild.ReleaseFunc != nil {
		selectedChild.ReleaseFunc()
	}

	parent, _ := newOpenAIUpstreamErrorTestContext(t)
	parent.Request.Header.Set("session-id", parentID)
	parentCtx := parent.Request.Context()
	selectedParent, _, err := svc.SelectAccountWithScheduler(parentCtx, &groupID, "", parentHash, "gpt-6-sol",
		nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selectedParent)
	require.Equal(t, int64(39101), selectedParent.Account.ID,
		"the parent must continue to select its healthy sticky owner")
	if selectedParent.ReleaseFunc != nil {
		selectedParent.ReleaseFunc()
	}

	root, _ := newOpenAIUpstreamErrorTestContext(t)
	root.Request.Header.Set("session-id", "root-session-id")
	root.Request.Header.Set(openAIWSThreadIDHeader, "root-thread-id")
	rootBody := []byte(`{"model":"gpt-6-sol","input":"parent request"}`)
	rootHash := svc.GenerateSessionHash(root, rootBody)
	rootCtx := WithOpenAITerminalRejectionScope(root.Request.Context(), root, rootBody)
	rootScope := openAIRejectionScope(rootCtx).session
	require.NotEmpty(t, rootScope)
	require.False(t, openAIRejectionScope(rootCtx).preserveBinding)
	cache.sessionBindings[svc.openAISessionCacheKey(rootHash)] = 39101
	svc.AvoidOpenAIStateAccount(rootCtx, groupID, rootScope, 39101)
	selectedRoot, _, err := svc.SelectAccountWithScheduler(rootCtx, &groupID, "", rootHash, "gpt-6-sol",
		nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selectedRoot)
	require.Equal(t, int64(39102), selectedRoot.Account.ID,
		"a root thread's own rejection must rebind its session owner")
	require.NotEqual(t, int64(39101), cache.sessionBindings[svc.openAISessionCacheKey(rootHash)])
	if selectedRoot.ReleaseFunc != nil {
		selectedRoot.ReleaseFunc()
	}
}

func TestOpenAITerminalRejectionScopeFreezesOriginalIdentity(t *testing.T) {
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	c.Request.Header.Set(openAIWSThreadIDHeader, "child-thread")
	c.Request.Header.Set(codexParentThreadIDHeader, "parent-thread")
	original := WithOpenAITerminalRejectionScope(c.Request.Context(), c, nil)
	require.NotEmpty(t, openAIRejectionScope(original).session)
	require.True(t, openAIRejectionScope(original).preserveBinding)
	c.Request.Header.Set(openAIWSThreadIDHeader, "account-namespaced-thread")
	c.Request.Header.Del(codexParentThreadIDHeader)
	require.Same(t, original, WithOpenAITerminalRejectionScope(original, c, nil))
}

func TestOpenAITerminalRejectionCanceledRequestDoesNotAvoid(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, store, _ := terminalRejectionTestService()
	account := terminalRejectionTestAccount(1, "relay-a.example")
	state := resilience.NewRequestState()
	state.SetSession(9, "v2:canceled-session", svc.openAISessionRecorder())
	ctx, cancel := context.WithCancel(resilience.WithRequestState(t.Context(), state))
	cancel()
	for range resilience.Current().Session.StreakThreshold {
		svc.noteOpenAITerminalRejection(ctx, account, http.StatusBadRequest, nil, []byte(terminalRejectionBody), ctx.Err())
	}
	avoid, err := store.SessionAvoid(t.Context(), 9, "v2:canceled-session")
	require.NoError(t, err)
	require.Empty(t, avoid)
}

func TestOpenAITerminalRejectionStreakUsesChildThreadScope(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, store, cache := terminalRejectionTestService()
	account := terminalRejectionTestAccount(1, "relay-a.example")
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	c.Request.Header.Set("session-id", "parent-session-id")
	c.Request.Header.Set(openAIWSThreadIDHeader, "child-thread-id")
	c.Request.Header.Set(codexParentThreadIDHeader, "parent-thread-id")
	c.Request.Header.Set(openAISubagentHeader, "collab_spawn")
	body := []byte(`{"model":"gpt-6-sol","input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"cipher"}]}]}`)
	parentSession := svc.GenerateSessionHash(c, body)
	state := resilience.NewRequestState()
	state.SetSession(9, parentSession, svc.openAISessionRecorder())
	ctx := resilience.WithRequestState(c.Request.Context(), state)
	ctx = WithOpenAITerminalRejectionScope(ctx, c, body)
	ctx = svc.WithOpenAIGuardianParentAffinity(ctx, c, body, "gpt-6-sol")
	c.Request = c.Request.WithContext(ctx)
	cache.bindings[svc.openAISessionCacheKey(parentSession)] = account.ID

	for range resilience.Current().Session.StreakThreshold {
		svc.noteOpenAITerminalRejection(ctx, account, http.StatusBadRequest, nil, []byte(terminalRejectionBody), errors.New("terminal"))
	}
	scope := openAIRejectionScope(ctx).session
	require.NotEmpty(t, scope)
	require.NotEqual(t, parentSession, scope)
	threadAvoid, err := store.SessionAvoid(t.Context(), 9, scope)
	require.NoError(t, err)
	require.Contains(t, threadAvoid, account.ID)
	parentAvoid, err := store.SessionAvoid(t.Context(), 9, parentSession)
	require.NoError(t, err)
	require.Empty(t, parentAvoid, "child rejection must not avoid the parent session")
	require.Equal(t, account.ID, cache.bindings[svc.openAISessionCacheKey(parentSession)],
		"child rejection must keep the parent's healthy sticky owner")
}
