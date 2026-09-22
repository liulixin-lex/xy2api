package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func qualityTestContext(s *OpenAIGatewayService, api, group int64, model, extra string) (*gin.Context, []byte, string) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	key := &APIKey{ID: api}
	if group != 0 {
		key.GroupID = &group
	}
	c.Set("api_key", key)
	body := []byte(fmt.Sprintf(`{"model":%q,"prompt_cache_key":"same-session","input":"hello"%s}`, model, extra))
	hash := s.GenerateSessionHash(c, body)
	return c, body, hash
}

func qualityTestAccount(id int64, provider string) Account {
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, Concurrency: 10, Priority: int(id), Credentials: map[string]any{"base_url": provider}}
}

func TestOpenAIQualityExcludesOAuth(t *testing.T) {
	s := &OpenAIGatewayService{}
	c, body, session := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	ctx := c.Request.Context()
	a := qualityTestAccount(1, "https://one.example")
	a.Type = AccountTypeOAuth
	s.qualityObserver(ctx, &a, "gpt-6-astra").ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
	q := qualityRequest(ctx)
	_, exists := s.openaiQualityStates.Load(q.scope)
	require.False(t, exists, "OAuth declarations must not invalidate quality affinity")
	// A previous API upstream may already have installed state for this session.
	q.state = OpenAIQualityState{Generation: 1, Rotation: "rotated", Avoid: map[string]OpenAIQualityAvoid{
		"1": {Provider: qualityProvider(&a)},
	}}
	require.Empty(t, s.qualityRotation(ctx, &a))
	require.Equal(t, session, s.qualityConnectionScope(ctx, &a, session))
	require.NoError(t, s.AdvanceOpenAIQualityTurn(ctx, &a, body, "gpt-6-astra"))
	require.Equal(t, uint64(1), q.state.Generation)
	// The same address with API Key credentials remains eligible for protection.
	a.Type = AccountTypeAPIKey
	q.state.Avoid["1"] = OpenAIQualityAvoid{Provider: qualityProvider(&a)}
	require.Equal(t, "rotated", s.qualityRotation(ctx, &a))
	require.NotEqual(t, session, s.qualityConnectionScope(ctx, &a, session))
}

func TestOpenAIQualityDetection(t *testing.T) {
	s := &OpenAIGatewayService{}
	for _, tc := range []struct {
		from, to string
		want     bool
	}{
		{"gpt-6-astra", "gpt-5.6-luna", true},
		{"gpt-5.6-sol", "gpt-5.6-luna-max", true},
		{"openai/gpt6_astra", "gpt-5.6-luna-2026-09-01", true},
		{"vendor/GPT 6 ASTRA", "gpt-5.6-luna-2026-09-01-max", true},
		{"gpt-6-astra", "gpt-5.6-luna-2026-02-31", false},
		{"gpt-5.6-terra-high", "gpt-5.6-luna", true},
		{"gpt-5.6-sol", "gpt-5.6-terra", false},
		{"gpt-5.6-luna", "gpt-5.6-luna-max", false},
		{"gpt-5.5", "gpt-5.6-luna", false},
		{"claude-sonnet", "gpt-5.6-luna", false},
		{"gpt-6-astra-image", "gpt-5.6-luna", false},
		{"gpt-6-astra", "gpt-5.6-luna-unknown", false},
		{"gpt-6-astra", "", false},
		{"gpt-6-astra", "luna", false},
	} {
		t.Run(tc.from+"/"+tc.to, func(t *testing.T) { require.Equal(t, tc.want, s.qualityDowngrade(tc.from, tc.to)) })
	}
	s.cfg = &config.Config{}
	s.cfg.Gateway.OpenAIQualityRouting.Rules = []config.OpenAIQualityRoutingRule{{From: "gpt-7", To: "gpt-5.6-terra"}}
	require.True(t, s.qualityDowngrade("gpt-7", "gpt-5.6-terra"))
}

func TestOpenAIQualityObserverEvidence(t *testing.T) {
	for _, payload := range []string{
		`{"model":"gpt-5.6-luna","model":"gpt-6-astra"}`,
		`{"response":{"model":"gpt-5.6-luna"},"model":"gpt-6-astra"}`,
		`{"response":{"model":"gpt-5.6-luna"},"response":{"model":"gpt-6-astra"}}`,
		`{"response":{"model":null}}`, `{"delta":"gpt-5.6-luna"}`, `{"model":"gpt-5.6-luna"`,
	} {
		s := &OpenAIGatewayService{}
		c, _, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
		a := qualityTestAccount(1, "https://one.example")
		o := s.qualityObserver(c.Request.Context(), &a, "gpt-6-astra")
		o.ObserveOpenAI([]byte(payload), "response.created")
		_, exists := s.openaiQualityStates.Load(qualityRequest(c.Request.Context()).scope)
		require.False(t, exists, payload)
	}
	for _, protocol := range []string{"json", "sse", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			s := &OpenAIGatewayService{}
			c, _, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
			a := qualityTestAccount(1, "https://one.example")
			o := s.qualityObserver(c.Request.Context(), &a, "gpt-6-astra")
			for i := 0; i < 3; i++ {
				switch protocol {
				case "json":
					o.ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
				case "sse":
					o.ObserveOpenAI([]byte(`{"response":{"model":"gpt-5.6-luna"}}`), "response.created")
				case "messages":
					o.ObserveAnthropic([]byte(`{"message":{"model":"gpt-5.6-luna"}}`))
				}
			}
			o.ObserveOpenAI([]byte(`{"response":{"model":"gpt-6-astra"}}`), "response.completed")
			local := s.qualityLocal(qualityRequest(c.Request.Context()).scope)
			require.Equal(t, uint64(1), local.state.Generation)
			require.True(t, o.Conflict())
		})
	}
}

func TestOpenAIQualityRoutingSchedulersAndIsolation(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%v/weighted=%v", advanced, weighted), func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
				accounts := []Account{qualityTestAccount(1, "https://bad.example/v1"), qualityTestAccount(2, "https://bad.example/v1/"), qualityTestAccount(3, "https://good.example/v1")}
				cache := &stubGatewayCache{}
				s := &OpenAIGatewayService{accountRepo: stubOpenAIAccountRepo{accounts: accounts}, cache: cache, cfg: &config.Config{}}
				s.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = true
				s.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
				settings := &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
					openAIAdvancedSchedulerSettingKey: fmt.Sprint(advanced), SettingKeyOpenAIAdvancedSchedulerStickyWeightedEnabled: fmt.Sprint(weighted),
				}}
				s.rateLimitService = &RateLimitService{settingService: NewSettingService(settings, s.cfg)}
				c, _, hash := qualityTestContext(s, 10, 0, "gpt-6-astra", "")
				cache.sessionBindings = map[string]int64{s.openAILegacySessionCacheKey(c.Request.Context(), hash): 1}
				o := s.qualityObserver(c.Request.Context(), &accounts[0], "gpt-6-astra")
				o.ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
				choose := func(api int64, model string) int64 {
					c, _, hash := qualityTestContext(s, api, 0, model, "")
					if api == 10 && model == "gpt-6-astra" {
						cache.sessionBindings["openai:quality-fixture-parent"] = 1
						c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{currentSessionHash: "quality-fixture-parent"}))
					}
					selection, _, err := s.SelectAccountWithScheduler(c.Request.Context(), nil, "", hash, model, nil, OpenAIUpstreamTransportHTTPSSE, false)
					require.NoError(t, err)
					require.NotNil(t, selection)
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
					return selection.Account.ID
				}
				require.Equal(t, int64(3), choose(10, "gpt-6-astra"), "other provider beats same-provider credential")
				require.Equal(t, int64(3), choose(10, "gpt-6-astra"), "replacement stays sticky")
				if !weighted || !advanced {
					require.Equal(t, int64(1), choose(11, "gpt-6-astra"))
					require.Equal(t, int64(1), choose(10, "gpt-5.6-sol"))
				}
				require.Empty(t, cache.deletedSessions, "shared old keys must not be deleted")
				for _, group := range []int64{0, 9} {
					cc, _, _ := qualityTestContext(s, 10, group, "gpt-6-astra", "")
					s.loadQuality(cc.Request.Context(), qualityRequest(cc.Request.Context()))
					if group != 0 {
						require.Zero(t, qualityRequest(cc.Request.Context()).state.Generation)
					}
				}
			})
		}
	}
}

func TestOpenAIQualityRotationAndMappedModel(t *testing.T) {
	s := &OpenAIGatewayService{}
	a := qualityTestAccount(1, "https://bad.example/v1")
	c, body, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	c.Request.Header.Set("session_id", "original")
	req, err := s.buildUpstreamRequest(c.Request.Context(), c, &a, body, "fixture", false, "same-session", false)
	require.NoError(t, err)
	actual, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, body, actual)
	o := upstreamResponseModelObserverFromContext(c)
	o.ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
	c2, body2, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	s.loadQuality(c2.Request.Context(), qualityRequest(c2.Request.Context()))
	rot := s.qualityRotation(c2.Request.Context(), &a)
	require.NotEmpty(t, rot)
	require.Equal(t, qualityRotateBody(body2, rot, true), qualityRotateBody(body2, rot, true))
	b := qualityTestAccount(2, "https://healthy.example/v1")
	require.Empty(t, s.qualityRotation(c2.Request.Context(), &b))
	pinned, pinnedBody, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", `,"previous_response_id":"resp_owned"`)
	s.loadQuality(pinned.Request.Context(), qualityRequest(pinned.Request.Context()))
	require.Empty(t, s.qualityRotation(pinned.Request.Context(), &a))
	require.NotEmpty(t, pinnedBody)

	s2 := &OpenAIGatewayService{}
	c3, _, _ := qualityTestContext(s2, 1, 0, "gpt-5.6-sol", "")
	o2 := s2.qualityObserver(c3.Request.Context(), &a, "gpt-5.6-terra")
	o2.ObserveOpenAI([]byte(`{"model":"gpt-5.6-terra"}`), "")
	_, exists := s2.openaiQualityStates.Load(qualityRequest(c3.Request.Context()).scope)
	require.False(t, exists)
}

func TestOpenAIQualityGenerationsAndConcurrency(t *testing.T) {
	s := &OpenAIGatewayService{}
	c, _, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	a := qualityTestAccount(1, "https://one.example")
	observers := make([]*upstreamResponseModelObserver, 32)
	for i := range observers {
		observers[i] = s.qualityObserver(c.Request.Context(), &a, "gpt-6-astra")
	}
	var wg sync.WaitGroup
	for _, o := range observers {
		wg.Add(1)
		go func() { defer wg.Done(); o.ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "") }()
	}
	wg.Wait()
	scope := qualityRequest(c.Request.Context()).scope
	local := s.qualityLocal(scope)
	require.Equal(t, uint64(1), local.state.Generation)
	st := s.qualityUpdate(scope, OpenAIQualityChange{Kind: "bind", Generation: 1, AccountID: 2, Now: time.Now().UnixMilli()})
	require.Equal(t, int64(2), st.Binding)
	st = s.qualityUpdate(scope, OpenAIQualityChange{Kind: "bind", Generation: 0, AccountID: 1, Now: time.Now().UnixMilli()})
	require.Equal(t, int64(2), st.Binding)
	st = s.qualityUpdate(scope, OpenAIQualityChange{Kind: "bind", Generation: 1, AccountID: 3, Now: time.Now().UnixMilli()})
	require.Equal(t, int64(2), st.Binding)
}

func TestOpenAIQualityExpiredStateGetsFreshIdentity(t *testing.T) {
	s := &OpenAIGatewayService{}
	a := qualityTestAccount(1, "https://one.example")
	c, _, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	s.qualityObserver(c.Request.Context(), &a, "gpt-6-astra").ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
	local := s.qualityLocal(qualityRequest(c.Request.Context()).scope)
	local.mu.Lock()
	firstRotation := local.state.Rotation
	local.state.Expires = time.Now().UnixMilli() - 1
	local.mu.Unlock()
	next, _, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	s.qualityObserver(next.Request.Context(), &a, "gpt-6-astra").ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
	local.mu.Lock()
	defer local.mu.Unlock()
	require.Equal(t, uint64(1), local.state.Generation)
	require.NotEmpty(t, local.state.Rotation)
	require.NotEqual(t, firstRotation, local.state.Rotation)
}

type qualityFailureStore struct{ stubGatewayCache }

func (*qualityFailureStore) ReadOpenAIQualityRouting(context.Context, string, int64, []string) (*OpenAIQualityState, []int64, error) {
	return nil, nil, errors.New("fixture outage")
}
func (*qualityFailureStore) UpdateOpenAIQualityRouting(ctx context.Context, _ string, _ OpenAIQualityChange, _ time.Duration) (*OpenAIQualityState, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestOpenAIQualityRedisTimeoutAndCancellation(t *testing.T) {
	s := &OpenAIGatewayService{cache: &qualityFailureStore{}}
	c, _, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	ctx, cancel := context.WithCancel(c.Request.Context())
	a := qualityTestAccount(1, "https://one.example")
	o := s.qualityObserver(ctx, &a, "gpt-6-astra")
	cancel()
	start := time.Now()
	o.ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
	require.Less(t, time.Since(start), 200*time.Millisecond)
	next, _, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	q := qualityRequest(next.Request.Context())
	s.loadQuality(next.Request.Context(), q)
	require.Equal(t, uint64(1), q.state.Generation)
}

func TestOpenAIQualityModesAndWSTurnBoundary(t *testing.T) {
	for _, mode := range []string{"off", "observe", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			s := &OpenAIGatewayService{cfg: &config.Config{}}
			s.cfg.Gateway.OpenAIQualityRouting.Mode = mode
			c, body, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
			a := qualityTestAccount(1, "https://one.example")
			o := s.qualityObserver(c.Request.Context(), &a, "gpt-6-astra")
			o.ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
			err := s.AdvanceOpenAIQualityTurn(c.Request.Context(), &a, body, "gpt-6-astra")
			if mode == "enforce" {
				var reroute *OpenAIQualityRerouteError
				require.ErrorAs(t, err, &reroute)
				require.Equal(t, body, reroute.Payload)
			} else {
				require.NoError(t, err)
			}
		})
	}
	s := &OpenAIGatewayService{}
	c, _, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	a := qualityTestAccount(1, "https://one.example")
	s.qualityObserver(c.Request.Context(), &a, "gpt-6-astra").ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
	require.NoError(t, s.AdvanceOpenAIQualityTurn(c.Request.Context(), &a, []byte(`{"previous_response_id":"resp_1","input":"followup"}`), "gpt-6-astra"))
	require.True(t, qualityRequest(c.Request.Context()).pinned)
	require.True(t, qualityIncompleteTools([]byte(`{"input":[{"type":"function_call_output","call_id":"missing"}]}`)))
	require.True(t, qualityIncompleteTools([]byte(`{"input":[{"type":"compaction","encrypted_content":"opaque-history"}]}`)))
}

func TestOpenAIQualityAllAvoidedOldestEligible(t *testing.T) {
	s := &OpenAIGatewayService{cache: &stubGatewayCache{}, accountRepo: stubOpenAIAccountRepo{accounts: []Account{qualityTestAccount(1, "https://a.example"), qualityTestAccount(2, "https://b.example")}}}
	c, _, hash := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	q := qualityRequest(c.Request.Context())
	now := time.Now().UnixMilli()
	a, b := qualityTestAccount(1, "https://a.example"), qualityTestAccount(2, "https://b.example")
	st := OpenAIQualityState{Generation: 2, Rotation: "stable", Expires: now + 3600000, Avoid: map[string]OpenAIQualityAvoid{
		"1": {Provider: qualityProvider(&a), At: now - 1000, Until: now + 300000},
		"2": {Provider: qualityProvider(&b), At: now, Until: now + 300000},
	}}
	s.qualityLocal(q.scope).state = st
	selected, _, err := s.SelectAccountWithScheduler(c.Request.Context(), nil, "", hash, "gpt-6-astra", nil, OpenAIUpstreamTransportHTTPSSE, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), selected.Account.ID)
}

func TestOpenAIQualityWSCompleteReplayAndPinnedRecovery(t *testing.T) {
	s := &OpenAIGatewayService{}
	c, _, _ := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
	a := qualityTestAccount(1, "https://one.example")
	ctx := c.Request.Context()
	s.qualityObserver(ctx, &a, "gpt-6-astra").ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
	pinned := []byte(`{"model":"gpt-6-astra","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call_1","output":"42"}]}`)
	rememberOpenAIQualityWSTurn(ctx, []byte(`{"input":"initial request"}`), "resp_1", nil, false)
	require.False(t, qualityRequest(ctx).replayComplete)
	require.NoError(t, s.AdvanceOpenAIQualityTurn(ctx, &a, pinned, "gpt-6-astra"))
	rememberOpenAIQualityWSTurn(ctx, []byte(`{"input":[{"role":"user","content":"run tool"}]}`), "resp_1", []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"tool","arguments":"{}"}`)}, true)
	err := s.AdvanceOpenAIQualityTurn(ctx, &a, pinned, "gpt-6-astra")
	var reroute *OpenAIQualityRerouteError
	require.ErrorAs(t, err, &reroute)
	require.NotContains(t, string(reroute.Payload), "previous_response_id")
	require.Contains(t, string(reroute.Payload), "run tool")
	require.False(t, qualityIncompleteTools(reroute.Payload))
}
