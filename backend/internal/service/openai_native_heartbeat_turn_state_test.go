//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func nativeTurnStateService(keepalive, timeout int) *OpenAIGatewayService {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval: keepalive, OpenAIFirstOutputTimeoutSeconds: timeout,
		MaxLineSize: defaultMaxLineSize,
	}}
	// Explicitly allow a Codex usage header so the test proves staging keeps
	// it private at heartbeat time, rather than relying on default filtering.
	cfg.Security.ResponseHeaders = config.ResponseHeaderConfig{
		Enabled: true, AdditionalAllowed: []string{"x-codex-primary-used-percent"},
	}
	return &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
}

func nativeTurnStateContext() (*gin.Context, *gwWriter) {
	w := newGWWriter()
	c := ptContext(w)
	c.Request.Header.Set("session_id", "sess-turn")
	c.Set("api_key", &APIKey{ID: 31})
	return c, w
}

type nativeTurnStateAttempt struct {
	done   chan struct{}
	result *openaiStreamingResult
	err    error
}

func runNativeTurnState(t *testing.T, svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, id int64) *nativeTurnStateAttempt {
	t.Helper()
	a := &nativeTurnStateAttempt{done: make(chan struct{})}
	account := ptTurnStateAccount(id)
	account.Extra = nil // Default OAuth routing is native Responses, without tickets.
	go func() {
		defer close(a.done)
		a.result, a.err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.2", "gpt-5.2")
	}()
	t.Cleanup(func() {
		_ = resp.Body.Close()
		a.await(t)
	})
	return a
}

func (a *nativeTurnStateAttempt) await(t *testing.T) {
	t.Helper()
	select {
	case <-a.done:
	case <-time.After(6 * time.Second):
		t.Fatal("native turn-state attempt did not stop")
	}
}

func awaitNativeTurnStateHeartbeat(t *testing.T, w *gwWriter) {
	t.Helper()
	select {
	case body := <-w.flushed:
		require.True(t, strings.HasPrefix(body, ":"), body)
		require.NotContains(t, body, "response.created", "the opening stays staged")
	case <-time.After(4 * time.Second):
		t.Fatal("native stream did not flush a heartbeat")
	}
}

func nativeTurnStateWireHeaders(w *gwWriter) http.Header {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Result uses the snapshot captured by WriteHeader, not the mutable Header map.
	return w.rec.Result().Header.Clone()
}

func requireNativeTurnStateHeaders(t *testing.T, w *gwWriter, state string) {
	t.Helper()
	h := nativeTurnStateWireHeaders(w)
	require.Equal(t, "text/event-stream", h.Get("Content-Type"))
	require.Equal(t, "no-cache", h.Get("Cache-Control"))
	require.Equal(t, "keep-alive", h.Get("Connection"))
	require.Equal(t, "no", h.Get("X-Accel-Buffering"))
	require.Equal(t, state, h.Get(openAICodexTurnStateHeader))
	require.Empty(t, h.Get("X-Request-Id"))
	require.Empty(t, h.Get("X-Codex-Primary-Used-Percent"))
	for key := range h {
		if strings.HasPrefix(strings.ToLower(key), "x-codex-") {
			require.Equal(t, openAICodexTurnStateHeader, strings.ToLower(key), "only turn-state may accompany a heartbeat")
		}
	}
}

func TestOpenAINativeHeartbeatCommitsOnlyTurnStateAndProvenance(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run("resilience_enabled="+fmtBool(enabled), func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = enabled })
			svc := nativeTurnStateService(1, 0)
			c, w := nativeTurnStateContext()
			up := newGWUpstream(context.Background())
			a := runNativeTurnState(t, svc, c, ptTurnStateResponse("A", true, up), 51)
			up.send(t, ptCreated)
			awaitNativeTurnStateHeartbeat(t, w)
			requireNativeTurnStateHeaders(t, w, "blob-A")
			require.Equal(t, int64(51), ptTurnStateOrigin(t, svc), "provenance exists at the heartbeat, before semantic output")
			// A later heartbeat must not refresh the origin or append another value.
			origin, _ := svc.openaiCodexTurnStateOrigins.Load(ptTurnStateSeed)
			awaitNativeTurnStateHeartbeat(t, w)
			after, _ := svc.openaiCodexTurnStateOrigins.Load(ptTurnStateSeed)
			require.Equal(t, origin, after)
			up.send(t, ptDelta, ptCompleted)
			_ = up.pw.Close()
			a.await(t)
			require.NoError(t, a.err)
			require.Equal(t, 5, a.result.usage.OutputTokens)
			require.True(t, strings.HasPrefix(w.Body(), ":\n\n"))
			require.Contains(t, w.Body(), "hello")
			require.Contains(t, w.Body(), "response.completed")
			requireNativeTurnStateHeaders(t, w, "blob-A")
			require.Equal(t, []string{"blob-A"}, nativeTurnStateWireHeaders(w).Values(openAICodexTurnStateHeader))
		})
	}
}

func TestOpenAINativeHeartbeatFailoverKeepsTurnStateOriginAndGuardsEcho(t *testing.T) {
	for _, bMints := range []bool{false, true} {
		t.Run("b_mints_turn_state="+fmtBool(bMints), func(t *testing.T) {
			withResilienceSettings(t, nil)
			svc := nativeTurnStateService(1, 2)
			c, w := nativeTurnStateContext()
			up := newGWUpstream(context.Background())
			a := runNativeTurnState(t, svc, c, ptTurnStateResponse("A", true, up), 51)
			up.send(t, ptCreated)
			awaitNativeTurnStateHeartbeat(t, w)
			a.await(t)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, a.err, &failover)
			require.Equal(t, http.StatusGatewayTimeout, failover.StatusCode)
			require.True(t, failover.SafeToFailoverAfterWrite)
			require.NotContains(t, w.Body(), "resp_pt")
			origin, _ := svc.openaiCodexTurnStateOrigins.Load(ptTurnStateSeed)
			// Drain A's flush notifications before checking B's own heartbeat.
			for len(w.flushed) > 0 {
				<-w.flushed
			}
			upB := newGWUpstream(context.Background())
			b := runNativeTurnState(t, svc, c, ptTurnStateResponse("B", bMints, upB), 52)
			upB.send(t, ptCreated)
			awaitNativeTurnStateHeartbeat(t, w)
			upB.send(t, ptDelta, ptCompleted)
			_ = upB.pw.Close()
			b.await(t)
			require.NoError(t, b.err)
			require.Contains(t, w.Body(), "hello")
			requireNativeTurnStateHeaders(t, w, "blob-A")
			require.Equal(t, int64(51), ptTurnStateOrigin(t, svc))
			after, _ := svc.openaiCodexTurnStateOrigins.Load(ptTurnStateSeed)
			require.Equal(t, origin, after, "B must not replace or refresh A's provenance")
			require.Empty(t, ptTurnStateNextEcho(svc, "blob-A", 52))
			require.Equal(t, "blob-A", ptTurnStateNextEcho(svc, "blob-A", 51))
		})
	}
}

type nativeTurnStateReadObserver struct {
	io.ReadCloser
	beforeRead func()
}

func (b *nativeTurnStateReadObserver) Read(p []byte) (int, error) {
	if b.beforeRead != nil {
		b.beforeRead()
		b.beforeRead = nil
	}
	return b.ReadCloser.Read(p)
}

func TestOpenAINativeTurnStateWithoutHeartbeatCommitsAllHeadersAtOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := nativeTurnStateService(0, 0)
	c, w := nativeTurnStateContext()
	var prematureOrigin bool
	body := &nativeTurnStateReadObserver{
		ReadCloser: io.NopCloser(strings.NewReader(ptTurnStateStream)),
		beforeRead: func() { _, prematureOrigin = svc.openaiCodexTurnStateOrigins.Load(ptTurnStateSeed) },
	}
	a := runNativeTurnState(t, svc, c, ptTurnStateResponse("A", true, body), 51)
	a.await(t)
	require.NoError(t, a.err)
	require.False(t, prematureOrigin, "staging alone must not record provenance")
	h := nativeTurnStateWireHeaders(w)
	require.Equal(t, "blob-A", h.Get(openAICodexTurnStateHeader))
	require.Equal(t, "req_A", h.Get("X-Request-Id"))
	require.Equal(t, "A", h.Get("X-Codex-Primary-Used-Percent"))
	require.Equal(t, int64(51), ptTurnStateOrigin(t, svc))
	require.True(t, strings.HasPrefix(w.Body(), "event: response.created\n"))
}

func TestOpenAINativeHeartbeatWithoutTurnStateDoesNotRecordOrigin(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := nativeTurnStateService(1, 0)
	c, w := nativeTurnStateContext()
	up := newGWUpstream(context.Background())
	a := runNativeTurnState(t, svc, c, ptTurnStateResponse("A", false, up), 51)
	up.send(t, ptCreated)
	awaitNativeTurnStateHeartbeat(t, w)
	requireNativeTurnStateHeaders(t, w, "")
	_, noted := svc.openaiCodexTurnStateOrigins.Load(ptTurnStateSeed)
	require.False(t, noted)
	up.send(t, ptDelta, ptCompleted)
	_ = up.pw.Close()
	a.await(t)
	require.NoError(t, a.err)
	requireNativeTurnStateHeaders(t, w, "")
	_, noted = svc.openaiCodexTurnStateOrigins.Load(ptTurnStateSeed)
	require.False(t, noted)
}

func TestOpenAINativeHeartbeatAfterEarlierCommitDoesNotPublishTurnState(t *testing.T) {
	for _, prior := range []struct{ name, frame string }{{"cdn_first_byte", ":\n\n"}, {"queue_ping", ": ping\n\n"}} {
		t.Run(prior.name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			svc := nativeTurnStateService(1, 0)
			c, w := nativeTurnStateContext()
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Header("X-Accel-Buffering", "no")
			_, err := c.Writer.Write([]byte(prior.frame))
			require.NoError(t, err)
			c.Writer.Flush()
			<-w.flushed
			up := newGWUpstream(context.Background())
			a := runNativeTurnState(t, svc, c, ptTurnStateResponse("A", true, up), 51)
			up.send(t, ptCreated)
			awaitNativeTurnStateHeartbeat(t, w)
			up.send(t, ptDelta, ptCompleted)
			_ = up.pw.Close()
			a.await(t)
			require.NoError(t, a.err)
			requireNativeTurnStateHeaders(t, w, "")
			require.Empty(t, c.Writer.Header().Get(openAICodexTurnStateHeader), "do not even mutate the headers after a commit")
			_, noted := svc.openaiCodexTurnStateOrigins.Load(ptTurnStateSeed)
			require.False(t, noted)
		})
	}
}

func TestOpenAINativeHeartbeatTurnStatePreservesErrorRuleTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := nativeTurnStateService(1, 0)
	c, w := nativeTurnStateContext()
	bindPassthroughRule(c, "openai", []string{"context_length_exceeded"}, 400)
	up := newGWUpstream(context.Background())
	a := runNativeTurnState(t, svc, c, ptTurnStateResponse("A", true, up), 51)
	up.send(t, ptCreated)
	awaitNativeTurnStateHeartbeat(t, w)
	up.send(t, buildContextLengthFailedSSE())
	_ = up.pw.Close()
	a.await(t)
	require.Error(t, a.err)
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, a.err, &failover)
	requireNativeTurnStateHeaders(t, w, "blob-A")
	require.Equal(t, int64(51), ptTurnStateOrigin(t, svc))
	body := w.Body()
	require.NotContains(t, body, "\n{")
	require.NotContains(t, body, "resp_pt")
	require.Equal(t, 1, strings.Count(body, "event: response.created\n"))
	require.Equal(t, 1, strings.Count(body, "event: response.failed\n"))
	require.Less(t, strings.Index(body, "event: response.created"), strings.Index(body, "event: response.failed"))
	require.Contains(t, body, "context window")
}
