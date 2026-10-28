//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
