//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/model"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type recoveryFixRepo struct {
	*resilienceAccountRepo
	reads        int
	balanceCalls int
	modelKeys    []string
}

func (r *recoveryFixRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.reads++
	return r.resilienceAccountRepo.GetByID(ctx, id)
}

func (r *recoveryFixRepo) SetBalancePauseTiered(ctx context.Context, id int64, base time.Duration, start, now time.Time, evidence string) (BalancePauseWrite, error) {
	r.balanceCalls++
	return r.resilienceAccountRepo.SetBalancePauseTiered(ctx, id, base, start, now, evidence)
}

func (r *recoveryFixRepo) SetModelRateLimit(_ context.Context, _ int64, key string, _ time.Time, _ ...string) error {
	r.modelKeys = append(r.modelKeys, key)
	return nil
}

func TestConvertedHTTPHardErrorsCarryAttemptModelAndHeaders(t *testing.T) {
	for _, entry := range []string{"gemini chat", "anthropic responses", "anthropic chat"} {
		for _, failure := range []struct {
			name   string
			status int
			body   string
		}{
			{"model", 400, `{"error":{"message":"unknown provider for model upstream"}}`},
			{"balance 400", 400, `{"error":{"message":"insufficient account balance"}}`},
			{"balance after expiry", 503, `{"error":{"message":"insufficient account balance"}}`},
		} {
			t.Run(entry+"/"+failure.name, func(t *testing.T) {
				platform := PlatformAnthropic
				if entry == "gemini chat" {
					platform = PlatformGemini
				}
				rateLimit, base, account, _ := newPenaltyFixture(t, platform)
				account.Credentials = map[string]any{"api_key": "test-key", "model_mapping": map[string]any{"public": "upstream", "upstream": "other"}}
				base.tempReason = resilience.BalancePauseReasonAtStep(1, "previous failure")
				base.tempUntil = time.Now().Add(-time.Second)
				repo := &recoveryFixRepo{resilienceAccountRepo: base}
				rateLimit.accountRepo = repo
				headers := http.Header{"Retry-After": {"17"}}
				upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: failure.status, Header: headers, Body: io.NopCloser(strings.NewReader(failure.body))}}}
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), st)
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
				body := []byte(`{"model":"public","messages":[{"role":"user","content":"hello"}]}`)
				started := time.Now()
				var err error
				if entry == "gemini chat" {
					svc := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: rateLimit}
					_, err = svc.ForwardAsChatCompletions(ctx, c, account, body)
				} else {
					svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: rateLimit, tlsFPProfileService: &TLSFingerprintProfileService{}}
					if entry == "anthropic responses" {
						_, err = svc.ForwardAsResponses(ctx, c, account, []byte(`{"model":"public","input":"hello"}`), nil)
					} else {
						_, err = svc.ForwardAsChatCompletions(ctx, c, account, body, nil)
					}
				}
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, 1, upstream.callCount)
				require.Empty(t, w.Body.String())
				require.Equal(t, account.ID, st.AttemptProfile().AccountID)
				require.False(t, st.AttemptProfile().AttemptStart.Before(started))
				require.Equal(t, "17", failover.ResponseHeaders.Get("Retry-After"))
				headers.Set("Retry-After", "90")
				require.Equal(t, "17", failover.ResponseHeaders.Get("Retry-After"), "sentinel owns its headers")
				if failure.name == "model" {
					require.Equal(t, []string{"upstream"}, repo.modelKeys, "map the request model exactly once")
				} else {
					require.Equal(t, 1, repo.balanceCalls)
					require.Equal(t, 2, resilience.BalancePauseStep(repo.tempReason))
				}
			})
		}
	}
}

func TestGeminiChatBalance429AppliesOnePenalty(t *testing.T) {
	rateLimit, base, account, _ := newPenaltyFixture(t, PlatformGemini)
	account.Credentials = map[string]any{"api_key": "test-key"}
	repo := &recoveryFixRepo{resilienceAccountRepo: base}
	rateLimit.accountRepo = repo
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(balance403Body))}}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	svc := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: rateLimit}
	_, err := svc.ForwardAsChatCompletions(c.Request.Context(), c, account, []byte(`{"model":"gemini-x","messages":[{"role":"user","content":"hello"}]}`))
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, 1, upstream.callCount)
	require.Equal(t, 1, repo.balanceCalls)
}

func TestHardFailureInPlaceRetryFollowsMasterInRealForwarders(t *testing.T) {
	for _, entry := range []string{"anthropic native", "anthropic passthrough", "gemini chat"} {
		for _, enabled := range []bool{false, true} {
			t.Run(entry+"/master_"+fmtBool(enabled), func(t *testing.T) {
				withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = enabled })
				platform := PlatformAnthropic
				if entry == "gemini chat" {
					platform = PlatformGemini
				}
				rateLimit, _, account, _ := newPenaltyFixtureKeepSettings(t, platform)
				account.Credentials = map[string]any{"api_key": "test-key"}
				upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{
					{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(balance403Body))},
					{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_api_key","message":"credentials rejected"}}`))},
				}}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
				var err error
				if platform == PlatformGemini {
					svc := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: rateLimit}
					require.True(t, svc.shouldRetryGeminiUpstreamError(account, 503), "legacy retry branch is reachable")
					_, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, account, body)
				} else {
					account.Credentials["custom_error_codes_enabled"] = true
					account.Credentials["custom_error_codes"] = []any{float64(401)}
					account.Extra = map[string]any{"anthropic_passthrough": entry == "anthropic passthrough"}
					svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: rateLimit, tlsFPProfileService: &TLSFingerprintProfileService{}}
					require.True(t, svc.shouldRetryUpstreamError(account, 503), "custom codes exclude 503 so the legacy retry branch is reachable")
					require.Equal(t, entry == "anthropic passthrough", account.IsAnthropicAPIKeyPassthroughEnabled())
					parsed, parseErr := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
					require.NoError(t, parseErr)
					_, err = svc.Forward(c.Request.Context(), c, account, parsed)
				}
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				wantCalls, wantStatus := 2, 401
				if enabled {
					wantCalls, wantStatus = 1, 503
				}
				require.Equal(t, wantCalls, upstream.callCount)
				require.Equal(t, wantStatus, failover.StatusCode)
			})
		}
	}
}

func TestHealthyAccountSuccessSkipsBalanceLookup(t *testing.T) {
	rateLimit, base, account, r := newPenaltyFixture(t, PlatformOpenAI)
	repo := &recoveryFixRepo{resilienceAccountRepo: base}
	rateLimit.accountRepo = repo
	st := resilience.NewRequestState()
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: time.Now()})
	r.NoteAccountSuccess(resilience.WithRequestState(context.Background(), st), account)
	require.Zero(t, repo.reads)
}

func TestBedrockHardFailureSkipsRetryAndAppliesFinalModelPenalty(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		enabled   bool
		wantCalls int
	}{
		{"model", 400, true, 1},
		{"auth", 529, true, 1},
		{"auth master off", 529, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = tc.enabled })
			repo := &modelNotFoundAccountRepoStub{}
			account := &Account{ID: 81, Platform: PlatformAnthropic, Type: AccountTypeBedrock, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "test-key"}}
			body := []byte(`{"error":{"message":"unknown model upstream"}}`)
			var signer *BedrockSigner
			if tc.status == 529 {
				// Custom error codes apply to API-key accounts. Exercise the
				// Bedrock executor's retry branch with signed API-key input.
				account.Type = AccountTypeAPIKey
				account.Credentials["custom_error_codes_enabled"] = true
				account.Credentials["custom_error_codes"] = []any{float64(401)}
				signer = NewBedrockSigner("test-id", "test-secret", "", "us-east-1")
				body = []byte(`{"error":{"code":"invalid_api_key","message":"credentials rejected"}}`)
			}
			finalBody := []byte(`{"error":{"code":"invalid_api_key","message":"final authentication failure"}}`)
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{
				{StatusCode: tc.status, Header: http.Header{"Retry-After": {"17"}}, Body: io.NopCloser(strings.NewReader(string(body)))},
				{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(finalBody)))},
			}}
			svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: &RateLimitService{accountRepo: repo}}
			if tc.status == 529 {
				require.True(t, svc.shouldRetryUpstreamError(account, 529), "legacy retry branch is reachable")
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			resp, err := svc.executeBedrockUpstream(c.Request.Context(), c, account, []byte(`{}`), "upstream", "us-east-1", false, signer, "test-key", "")
			require.NoError(t, err)
			require.Equal(t, tc.wantCalls, upstream.callCount)
			defer func() { _ = resp.Body.Close() }()
			// The final path handles AUTH once and writes one model penalty.
			_, err = svc.handleBedrockUpstreamErrors(c.Request.Context(), resp, c, account, "upstream")
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			if tc.wantCalls == 2 {
				body = finalBody
			}
			require.Equal(t, body, failover.ResponseBody)
			if tc.status == 400 {
				require.Len(t, repo.modelRateLimitCalls, 1)
			}
		})
	}
}

func TestStaleBalanceSuccessCannotClearANewerPenalty(t *testing.T) {
	for _, reason := range []string{"auth:invalid_key", resilience.BalancePauseReasonAtStep(2, "new failure")} {
		_, repo, account, r := newPenaltyFixture(t, PlatformOpenAI)
		oldUntil := time.Now().Add(-time.Minute)
		account.TempUnschedulableReason = resilience.BalancePauseReasonAtStep(1, "old failure")
		account.TempUnschedulableUntil = &oldUntil
		repo.tempUntil, repo.tempReason = time.Now().Add(time.Minute), reason
		st := resilience.NewRequestState()
		st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: time.Now()})
		r.NoteAccountSuccess(resilience.WithRequestState(context.Background(), st), account)
		require.Equal(t, reason, repo.tempReason)
		require.Zero(t, repo.clearCalls)
	}
}

func TestHTTPErrorProjectionSanitizesOnlyMessages(t *testing.T) {
	body := []byte(`{"error":{"message":"invalid schema for org-AbC123456","code":"org-code123","type":"invalid_request_error","param":"project-param123"},"response_id":"resp_keep","tool_call_id":"call_keep"}`)
	account := &Account{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	_, err := (&GatewayService{}).handleErrorResponse(c.Request.Context(), &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, c, account)
	require.Error(t, err)
	require.NotContains(t, gjson.GetBytes(w.Body.Bytes(), "error.message").String(), "org-AbC123456")
	for _, field := range []string{"error.code", "error.type", "error.param", "response_id", "tool_call_id"} {
		require.Equal(t, gjson.GetBytes(body, field).String(), gjson.GetBytes(w.Body.Bytes(), field).String())
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	require.Error(t, (&GeminiMessagesCompatService{}).writeGeminiChatCompletionsMappedError(c, account, 400, "req", body))
	require.NotContains(t, w.Body.String(), "org-AbC123456")
}

func TestPassthroughRuleSanitizesClientProjection(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		rule := newNonFailoverPassthroughRule(400, "invalid schema", 400, "invalid schema for org-AbC123456")
		rule.PassthroughBody = passthrough
		svc := &ErrorPassthroughService{}
		svc.setLocalCache([]*model.ErrorPassthroughRule{rule})
		BindErrorPassthroughService(c, svc)
		body := []byte(`{"error":{"message":"invalid schema for org-AbC123456"}}`)
		_, _, message, matched := applyErrorPassthroughRule(c, PlatformAnthropic, 400, body, 502, "upstream_error", "fallback")
		require.True(t, matched)
		require.NotContains(t, message, "org-AbC123456")
		require.Contains(t, string(body), "org-AbC123456", "raw classifier input stays intact")
	}
}
