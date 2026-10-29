//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAINativeForwardFailureKeepsOnlyPostOutputResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withResilienceSettings(t, nil)
	failed := "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"broke\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":3}}}\n\n"
	for _, output := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_output", true: "after_output"}[output], func(t *testing.T) {
			stream := failed
			if output {
				stream = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" + stream
			}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			body := []byte(`{"model":"gpt-5.4","stream":true,"input":"hello"}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			account := &Account{ID: 811, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example"}}
			result, err := svc.Forward(context.Background(), c, account, body)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			if !output {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failover)
				return
			}
			require.False(t, errors.As(err, &failover))
			require.NotNil(t, result)
			require.Equal(t, 11, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.OutputTokens)
		})
	}
}

func TestOpenAINativePartialFailurePreservesPenalty(t *testing.T) {
	rl, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	account.Credentials = map[string]any{"api_key": "test-key", "base_url": "https://upstream.example"}
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"insufficient_balance\",\"message\":\"insufficient account balance\"},\"usage\":{\"input_tokens\":11}}}\n\n"
	svc := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: rl, accountRepo: repo,
		httpUpstream: &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}}}
	now := time.Now()
	svc.getOpenAIAccountModelTransientState().recordFailure(account.ID, "gpt-5.4", now.Add(-time.Second))
	svc.getOpenAIAccountModelTransientState().recordFailure(account.ID, "gpt-5.4", now)
	// A newer pause is already persisted after this attempt's account snapshot.
	repo.tempReason = resilience.BalancePauseReasonAtStep(1, "newer failure")
	repo.tempUntil = now.Add(time.Minute)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","stream":true,"input":"hello"}`))
	require.Error(t, err)
	require.NotNil(t, result)
	require.True(t, svc.getOpenAIAccountModelTransientState().isBlocked(account.ID, "gpt-5.4", time.Now()))
	require.True(t, resilience.IsBalancePause(repo.tempReason), repo.tempReason)
	require.Zero(t, repo.clearCalls)
}
