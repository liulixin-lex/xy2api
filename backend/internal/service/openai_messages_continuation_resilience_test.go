package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Pooled relays reject previous_response_id with vague errors; found on the
// staging server with real relays, where every turn after the first failed.

func TestOpenAICompatContinuationRejected(t *testing.T) {
	t.Parallel()
	body := func(s string) []byte { return []byte(s) }
	require.True(t, isOpenAICompatContinuationRejected(http.StatusBadRequest,
		body(`{"error":{"message":"Upstream request failed","type":"invalid_request_error"}}`)))
	require.True(t, isOpenAICompatContinuationRejected(http.StatusBadRequest,
		body(`{"error":{"code":"invalid_request","message":"The request could not be processed. Please check the request parameters.","type":"invalid_request_error"}}`)))
	require.True(t, isOpenAICompatContinuationRejected(http.StatusNotFound, body(`not found`)))
	require.False(t, isOpenAICompatContinuationRejected(http.StatusBadRequest,
		body(`{"error":{"code":"context_length_exceeded","message":"input too long"}}`)), "a full replay is longer still")
	require.False(t, isOpenAICompatContinuationRejected(http.StatusBadRequest,
		body(`{"error":{"message":"Your request was rejected by the safety system"}}`)))
	require.False(t, isOpenAICompatContinuationRejected(http.StatusBadRequest,
		body(`{"error":{"code":"model_not_found","message":"The model does not exist"}}`)))
	require.False(t, isOpenAICompatContinuationRejected(http.StatusUnauthorized, body(`{"error":{"message":"bad key"}}`)))
	require.False(t, isOpenAICompatContinuationRejected(http.StatusTooManyRequests, body(`{"error":{"message":"slow down"}}`)))
	require.False(t, isOpenAICompatContinuationRejected(http.StatusBadGateway, body(`{"error":{"message":"Upstream request failed"}}`)))
}

func TestForwardAsAnthropic_ContinuationRejectedReplaysAndStaysOff(t *testing.T) {
	t.Parallel()

	rejected := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Upstream request failed","type":"invalid_request_error"}}`)),
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		rejected,
		openAICompatSSECompletedResponse("resp_replayed", "gpt-6-astra"),
		openAICompatSSECompletedResponse("resp_next", "gpt-6-astra"),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://relay.example/v1"}}
	svc.bindOpenAICompatSessionResponseID(context.Background(), nil, account, "stable-cache-key", "resp_elsewhere")
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"}],"stream":false}`)

	forward := func() *OpenAIForwardResult {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "stable-cache-key", "gpt-6-astra")
		require.NoError(t, err)
		require.NotNil(t, result)
		return result
	}

	require.Equal(t, "resp_replayed", forward().ResponseID)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "resp_elsewhere", gjson.GetBytes(upstream.bodies[0], "previous_response_id").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "previous_response_id").Exists(), "the turn is replayed in full")
	require.Contains(t, string(upstream.bodies[1]), "first")

	// Later turns of the session no longer chain on this account.
	require.Equal(t, "resp_next", forward().ResponseID)
	require.Len(t, upstream.requests, 3)
	require.False(t, gjson.GetBytes(upstream.bodies[2], "previous_response_id").Exists())
}

func TestOpenAICompatSessionBindingsAreSwept(t *testing.T) {
	svc := &OpenAIGatewayService{}
	now := time.Now()
	svc.openaiCompatSessionResponses.Store("gone", openAICompatSessionResponseBinding{ResponseID: "r1", ExpiresAt: now.Add(-time.Second)})
	svc.openaiCompatSessionResponses.Store("live", openAICompatSessionResponseBinding{ResponseID: "r2", ExpiresAt: now.Add(time.Hour)})
	for i := 0; i < openAICompatSessionSweepEvery; i++ {
		svc.sweepOpenAICompatSessionResponses(now)
	}
	_, gone := svc.openaiCompatSessionResponses.Load("gone")
	_, live := svc.openaiCompatSessionResponses.Load("live")
	require.False(t, gone, "a session that never returns does not stay in memory")
	require.True(t, live)
}
