//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIRecentSessionSuccessBoundsAndExpiry(t *testing.T) {
	svc := &OpenAIGatewayService{}
	history := svc.recentSessionSuccess()
	key := openAIRecentSessionKey{71, "session", "gpt-5.4"}
	now := time.Now()
	history.record(key, 1, now)
	history.record(key, 2, now.Add(30*time.Minute))
	require.True(t, history.lastSuccess(key, 1, now.Add(time.Hour)).IsZero())
	require.Equal(t, now.Add(30*time.Minute), history.lastSuccess(key, 2, now.Add(time.Hour)), "expiry belongs to each account")
	history.record(key, 3, now.Add(time.Hour))
	history.record(key, 4, now.Add(time.Hour+time.Minute))
	history.record(key, 5, now.Add(time.Hour+2*time.Minute))
	require.True(t, history.lastSuccess(key, 2, now.Add(time.Hour+2*time.Minute)).IsZero(), "keep only three most recent accounts")
	require.Len(t, history.entries[key].Value.(*openAIRecentSessionEntry).accounts, 3)
	for _, other := range []openAIRecentSessionKey{{72, key.session, key.model}, {71, key.session, "gpt-5.5"}, {71, "another", key.model}} {
		require.True(t, history.lastSuccess(other, 5, now).IsZero())
	}
	// Touching the first key must evict the second key at the fixed capacity.
	history = (&OpenAIGatewayService{}).recentSessionSuccess()
	keyFor := func(i int) openAIRecentSessionKey { return openAIRecentSessionKey{71, fmt.Sprint(i), "gpt-5.4"} }
	for i := 0; i < openAIRecentSessionCapacity; i++ {
		history.record(keyFor(i), 1, now)
	}
	require.False(t, history.lastSuccess(keyFor(0), 1, now).IsZero())
	history.record(keyFor(openAIRecentSessionCapacity), 1, now)
	require.Len(t, history.entries, 16384)
	require.Equal(t, 16384, history.lru.Len())
	require.True(t, history.lastSuccess(keyFor(1), 1, now).IsZero())
	require.False(t, history.lastSuccess(keyFor(0), 1, now).IsZero())
}

func TestOpenAIRecentSessionSuccessRegistration(t *testing.T) {
	for _, name := range []string{"completed", "partial", "cancel", "disconnected", "opening", "empty", "failed", "ws"} {
		t.Run(name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := resilience.NewRequestState()
			ctx = resilience.WithRequestState(ctx, st)
			st.InitDelivery(ctx, time.Now(), nil)
			st.SetRecoveryOutcome("completed")
			if name != "opening" && name != "empty" {
				st.NoteSemanticOutput(time.Now())
			}
			result := &OpenAIForwardResult{UpstreamModel: "gpt-5.4", PartialFailure: name == "partial", ClientDisconnect: name == "disconnected", OpenAIWSMode: name == "ws"}
			if name == "cancel" {
				cancel()
			}
			if name == "failed" {
				st.SetRecoveryOutcome("upstream_failure")
			}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.4": "gpt-5.5"}}}
			group := int64(71)
			svc.RecordOpenAIRecentSessionSuccess(ctx, &group, "session", account, result.UpstreamModel, result)
			at := svc.recentSessionSuccess().lastSuccess(openAIRecentSessionKey{group, "session", "gpt-5.4"}, account.ID, time.Now())
			require.Equal(t, name == "completed", !at.IsZero())
			require.True(t, svc.recentSessionSuccess().lastSuccess(openAIRecentSessionKey{group, "session", "gpt-5.5"}, account.ID, time.Now()).IsZero(), "canonical outbound model must not be mapped again")
		})
	}
}

func TestOpenAIRecentSessionMappedForwardAndReplacementOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withResilienceSettings(t, nil)
	group, session := int64(71), "mapped-session"
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example", "model_mapping": map[string]any{"alias": "gpt-5.4", "gpt-5.4": "gpt-5.5"}}}
	b := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
	InstallDeliveryTracking(c, time.Now())
	result, err := svc.Forward(c.Request.Context(), c, a, []byte(`{"model":"alias","stream":true,"input":"hi"}`))
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", result.UpstreamModel)
	st := resilience.StateFrom(c.Request.Context())
	require.NotNil(t, st.EndToEndFirstOutputMs())
	st.SetRecoveryOutcome("completed")
	svc.RecordOpenAIRecentSessionSuccess(c.Request.Context(), &group, session, a, result.UpstreamModel, result)
	order := func() int64 {
		items := []accountWithLoad{{account: b, loadInfo: &AccountLoadInfo{}}, {account: a, loadInfo: &AccountLoadInfo{LoadRate: 20}}}
		svc.preferOpenAIReplacementSession(context.Background(), items, &group, session, "alias", openAILegacyUpstreamRateOrder{}, false, OpenAIEndpointCapabilityResponses)
		return items[0].account.ID
	}
	require.Equal(t, a.ID, order(), "request alias must find the successful outbound model")
	// Give B a later success, then return to A. Successful time precedes HRW.
	future := time.Now().Add(time.Second)
	svc.recentSessionSuccess().record(openAIRecentSessionKey{group, session, "alias"}, b.ID, future)
	require.Equal(t, b.ID, order())
	svc.recentSessionSuccess().record(openAIRecentSessionKey{group, session, "gpt-5.4"}, a.ID, future.Add(time.Second))
	require.Equal(t, a.ID, order())
	a.Credentials["model_mapping"] = map[string]any{"alias": "gpt-5.5"}
	require.Equal(t, b.ID, order(), "changing the mapping must not reuse the old model's history")
	require.False(t, OpenAIAttemptWarm(OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerLoadBalance}, &AccountSelectionResult{Account: a}, true))
}

func TestOpenAIRecentSessionSelectionKeepsEligibility(t *testing.T) {
	group := int64(73)
	for _, name := range []string{"replacement", "healthy owner", "no session", "outside load band", "full", "excluded", "model hard gate", "slot lost"} {
		t.Run(name, func(t *testing.T) {
			accounts := []Account{}
			for id := int64(1); id <= 3; id++ {
				accounts = append(accounts, Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{group}})
			}
			cfg := &config.Config{RunMode: config.RunModeStandard}
			cfg.Gateway.Scheduling.LoadBatchEnabled = true
			svc := &OpenAIGatewayService{cfg: cfg, accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}}
			session := "recent"
			svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:" + session: 1}}
			svc.recentSessionSuccess().record(openAIRecentSessionKey{group, session, "gpt-test"}, 3, time.Now())
			loads := map[int64]*AccountLoadInfo{1: {}, 2: {}, 3: {LoadRate: 20}}
			excluded := map[int64]struct{}{1: {}}
			want := int64(2)
			acquire := map[int64]bool{1: true, 2: true, 3: true}
			switch name {
			case "replacement":
				want = 3
			case "healthy owner":
				excluded = nil
				want = 1
			case "no session":
				session = ""
			case "outside load band":
				loads[3].LoadRate = 21
			case "full":
				loads[3].LoadRate = 100
			case "excluded":
				excluded[3] = struct{}{}
			case "model hard gate":
				accounts[2].Credentials = map[string]any{"model_mapping": map[string]any{"different": "different"}}
			case "slot lost":
				acquire[3] = false
			}
			svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: loads, acquireResults: acquire})
			selected, err := svc.SelectAccountWithLoadAwareness(context.Background(), &group, session, "gpt-test", excluded)
			require.NoError(t, err)
			require.True(t, selected.Acquired)
			defer selected.ReleaseFunc()
			require.Equal(t, want, selected.Account.ID)
		})
	}
}

func TestOpenAIRecentSessionPassthroughModelsMatchEndpoints(t *testing.T) {
	for _, protocol := range []string{"responses", "chat", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			withResilienceSettings(t, nil)
			group := int64(71)
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Extra: map[string]any{"openai_passthrough": true},
				Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example",
					"model_mapping": map[string]any{"alias": "gpt-5.4", "gpt-5.4": "gpt-5.5"}}}
			other := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			session := ""
			for i := 0; i < 100; i++ {
				key := fmt.Sprintf("mapping-session-%d", i)
				if openAIReplacementSessionHash(group, key, other.ID) > openAIReplacementSessionHash(group, key, account.ID) {
					session = key
					break
				}
			}
			require.NotEmpty(t, session)
			stream := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_review\",\"model\":\"gpt-5.4\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_review\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\n"
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil).WithContext(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
			InstallDeliveryTracking(c, time.Now())
			var result *OpenAIForwardResult
			var err error
			switch protocol {
			case "responses":
				result, err = svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"alias","stream":true,"input":"hi"}`))
			case "chat":
				result, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, account, []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"hi"}]}`), "", "")
			case "messages":
				result, err = svc.ForwardAsAnthropic(c.Request.Context(), c, account, []byte(`{"model":"alias","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`), "", "")
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requestBodies, 1)
			sentModel := gjson.GetBytes(upstream.requestBodies[0], "model").String()
			scheduleModel := result.UpstreamModel
			if protocol == "responses" {
				require.Empty(t, result.UpstreamModel, "unchanged passthrough model keeps the usage NULL contract")
				scheduleModel = ResolveOpenAIAccountUpstreamModelForRequest(account, "alias", false)
			}
			require.Equal(t, sentModel, scheduleModel)
			st := resilience.StateFrom(c.Request.Context())
			require.NotNil(t, st.EndToEndFirstOutputMs(), "fixture really delivered content")
			st.SetRecoveryOutcome("completed")
			svc.RecordOpenAIRecentSessionSuccess(c.Request.Context(), &group, session, account, scheduleModel, result)
			require.False(t, svc.recentSessionSuccess().lastSuccess(openAIRecentSessionKey{group, session, sentModel}, account.ID, time.Now()).IsZero(), "write uses actual successful outbound model")
			capability := OpenAIEndpointCapabilityChatCompletions
			wantModel := "gpt-5.4"
			if protocol == "responses" {
				capability = OpenAIEndpointCapabilityResponses
				wantModel = "alias"
			}
			require.Equal(t, wantModel, sentModel, "canonical outbound model is not mapped again")
			items := []accountWithLoad{{account: other, loadInfo: &AccountLoadInfo{}}, {account: account, loadInfo: &AccountLoadInfo{LoadRate: 20}}}
			svc.preferOpenAIReplacementSession(c.Request.Context(), items, &group, session, "alias", openAILegacyUpstreamRateOrder{}, false, capability)
			require.Equal(t, account.ID, items[0].account.ID, "recent success must outrank the chosen HRW control winner")
			if protocol != "responses" {
				account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-5.5"}
				svc.preferOpenAIReplacementSession(c.Request.Context(), items, &group, session, "alias", openAILegacyUpstreamRateOrder{}, false, capability)
				require.Equal(t, other.ID, items[0].account.ID, "changed outbound model must not reuse old history")
			}
		})
	}
}
