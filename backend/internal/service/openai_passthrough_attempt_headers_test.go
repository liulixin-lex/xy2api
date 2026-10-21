//go:build unit

package service

import (
	"context"
	"errors"
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
)

// INTEG-P2-1: a passthrough heartbeat before the first output commits 200
// with this gateway's stream headers only. The attempt's own headers
// (x-request-id, x-codex-*) wait for its first semantic output, as on the
// native path, and its turn-state provenance is recorded only when they
// reach the client; an attempt that is switched leaves no trace.

const ptHeadersSeed = "31\x00sess-headers"

func ptHeadersAccount(id int64) *Account {
	return &Account{
		ID: id, Name: "acc", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}
}

func ptHeadersResponse(attempt string, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{
		"Content-Type":                 []string{"text/event-stream"},
		"X-Request-Id":                 []string{"req_" + attempt},
		"X-Codex-Turn-State":           []string{"blob-" + attempt},
		"X-Codex-Primary-Used-Percent": []string{attempt},
	}}
}

func ptHeadersContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	c.Request.Header.Set("session_id", "sess-headers")
	c.Set("api_key", &APIKey{ID: 31})
	return c, rec
}

func ptHeadersForward(t *testing.T, svc *OpenAIGatewayService, c *gin.Context, account *Account) error {
	t.Helper()
	_, err := svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"gpt-5.2","stream":true,"instructions":"local-test-instructions","input":[{"type":"text","text":"hi"}]}`))
	return err
}

func ptHeadersOrigin(svc *OpenAIGatewayService) (int64, bool) {
	raw, ok := svc.openaiCodexTurnStateOrigins.Load(ptHeadersSeed)
	if !ok {
		return 0, false
	}
	return raw.(openAICodexTurnStateOrigin).accountID, true
}

const ptHeadersStream = ptCreated + ptDelta + ptCompleted

func TestOpenAIPassthroughHeartbeatCommitsOnlyStableStreamHeaders(t *testing.T) {
	withResilienceSettings(t, nil)
	c, rec := ptHeadersContext()
	silentA, silentWriter := io.Pipe()
	defer func() { _ = silentWriter.Close() }()
	go func() { _, _ = io.WriteString(silentWriter, ptComment) }()
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		ptHeadersResponse("A", silentA),
		ptHeadersResponse("B", io.NopCloser(strings.NewReader(ptHeadersStream))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIFirstOutputTimeoutSeconds: 1}}}

	err := ptHeadersForward(t, svc, c, ptHeadersAccount(51))
	var failover *UpstreamFailoverError
	require.True(t, errors.As(err, &failover), "attempt A misses its first-output deadline: %v", err)
	require.True(t, strings.HasPrefix(rec.Body.String(), ":"), "a heartbeat committed 200 before the switch")
	require.NoError(t, ptHeadersForward(t, svc, c, ptHeadersAccount(52)))
	require.Contains(t, rec.Body.String(), "hello")

	committed := rec.Result().Header
	require.Equal(t, "text/event-stream", committed.Get("Content-Type"))
	require.Equal(t, "no-cache", committed.Get("Cache-Control"))
	require.Empty(t, committed.Get("X-Request-Id"), "attempt A's request id must not reach the client")
	require.Empty(t, committed.Get("X-Codex-Turn-State"))
	require.Empty(t, committed.Get("X-Codex-Primary-Used-Percent"))
	_, recorded := ptHeadersOrigin(svc)
	require.False(t, recorded, "no turn-state reached the client, so none is recorded")
}

// Without a heartbeat the first semantic output carries the attempt's
// headers, and its turn-state provenance is recorded.
func TestOpenAIPassthroughFirstOutputCarriesAttemptHeaders(t *testing.T) {
	withResilienceSettings(t, nil)
	c, rec := ptHeadersContext()
	upstream := &httpUpstreamRecorder{responses: []*http.Response{ptHeadersResponse("A", io.NopCloser(strings.NewReader(ptHeadersStream)))}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIFirstOutputTimeoutSeconds: 5}}}

	require.NoError(t, ptHeadersForward(t, svc, c, ptHeadersAccount(51)))
	committed := rec.Result().Header
	require.Equal(t, "text/event-stream", committed.Get("Content-Type"))
	require.Equal(t, "req_A", committed.Get("X-Request-Id"))
	require.Equal(t, "blob-A", committed.Get("X-Codex-Turn-State"))
	require.Equal(t, "A", committed.Get("X-Codex-Primary-Used-Percent"))
	origin, recorded := ptHeadersOrigin(svc)
	require.True(t, recorded)
	require.Equal(t, int64(51), origin)
}

// With stream.strict_terminal off the passthrough keeps 0.4.4: the headers
// are set when the stream starts and the keepalive commits them.
func TestOpenAIPassthroughAttemptHeadersSwitchOff(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.StrictTerminal = false })
	c, rec := ptHeadersContext()
	slow, slowWriter := io.Pipe()
	go func() {
		time.Sleep(1300 * time.Millisecond)
		_, _ = io.WriteString(slowWriter, ptHeadersStream)
		_ = slowWriter.Close()
	}()
	upstream := &httpUpstreamRecorder{responses: []*http.Response{ptHeadersResponse("A", slow)}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1}}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	require.NoError(t, ptHeadersForward(t, svc, c, ptHeadersAccount(51)))
	require.True(t, strings.HasPrefix(rec.Body.String(), ":"), "the keepalive committed 200 first")
	committed := rec.Result().Header
	require.Equal(t, "req_A", committed.Get("X-Request-Id"))
	require.Equal(t, "blob-A", committed.Get("X-Codex-Turn-State"))
	origin, recorded := ptHeadersOrigin(svc)
	require.True(t, recorded)
	require.Equal(t, int64(51), origin)
}
