//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGrokModelUnsupportedCooldownUsesMappedUpstreamModelKey(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	account := &Account{
		ID:       8,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"public-grok": "grok-upstream", "grok-upstream": "grok-next"},
		},
	}
	svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
	ctx := withGrokTeamRateLimitModel(context.Background(), "grok-upstream")
	svc.handleGrokAccountUpstreamError(ctx, account, http.StatusBadRequest, nil,
		[]byte(`{"error":{"type":"invalid_request_error","message":"unknown provider for model grok-upstream"}}`))

	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "grok-upstream", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, upstreamModelUnsupported400Reason, repo.modelRateLimitCalls[0].reason)
}

func TestGrokProjectionAfterRegisteredFailureDoesNotAppendAgain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	account := &Account{ID: 7, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	body := []byte(`{"error":{"type":"invalid_request_error","message":"invalid request"}}`)
	resp := &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{UpstreamStatusCode: resp.StatusCode, Kind: "http_error"})
	svc := &OpenAIGatewayService{}

	_, err := svc.handleGrokErrorResponseAfterAccountHandling(context.Background(), resp, c, account, nil, "grok-4")
	require.Error(t, err)
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	require.Len(t, raw.([]*OpsUpstreamErrorEvent), 1)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestGrokRawChatErrorRegistersOnceAndSkipsCompatSideEffects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, customErrorCodes := range []bool{false, true} {
		name := "default error codes"
		if customErrorCodes {
			name = "custom error code early return"
		}
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hello"}],"stream":false}`)
			repo := &grokQuotaAccountRepo{}
			account := rawChatCompletionsTestAccount()
			account.Platform = PlatformGrok
			account.Credentials["temp_unschedulable_enabled"] = true
			account.Credentials["temp_unschedulable_rules"] = []any{map[string]any{
				"error_code":       float64(http.StatusBadRequest),
				"keywords":         []any{"bad request"},
				"duration_minutes": float64(1),
			}}
			if customErrorCodes {
				account.Credentials["custom_error_codes_enabled"] = true
				account.Credentials["custom_error_codes"] = []any{float64(http.StatusTooManyRequests)}
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"grok-chat-error"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"bad request"}}`)),
			}}
			service := &OpenAIGatewayService{
				cfg:              rawChatCompletionsTestConfig(),
				httpUpstream:     upstream,
				accountRepo:      repo,
				rateLimitService: NewRateLimitService(repo, nil, nil, nil, nil),
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

			_, err := service.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
			require.Error(t, err)
			require.Zero(t, repo.tempUnschedCalls)
			raw, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			require.Len(t, raw.([]*OpsUpstreamErrorEvent), 1)
		})
	}
}
