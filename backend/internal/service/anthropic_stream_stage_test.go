//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestAnthropicOpeningAttemptsDoNotLeak(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmtBool(passthrough), func(t *testing.T) {
			c, rec := compatStreamContext("/v1/messages", `{}`)
			upstream := &anthropicHTTPUpstreamRecorder{}
			svc := newForwardPartialUsageServiceForTest(upstream)
			repo := &streamErrorRepo{}
			svc.rateLimitService = NewRateLimitService(repo, nil, svc.cfg, nil, nil)
			account := newAnthropicOAuthAccountForPartialUsageTest()
			if passthrough {
				account = newAnthropicAPIKeyAccountForTest()
			}
			run := func(id, events string) (*ForwardResult, error) {
				upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {id}}, Body: io.NopCloser(strings.NewReader(events))}
				parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
				require.NoError(t, err)
				return svc.Forward(c.Request.Context(), c, account, parsed)
			}
			opening := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"failed_attempt\",\"usage\":{\"input_tokens\":999}}}\n\n" +
				"data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
			result, err := run("failed-header", opening+"event: error\ndata: "+streamErrorEvent("rate_limit_error")+"\n\n")
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Nil(t, result)
			require.Empty(t, rec.Body.String())
			require.Empty(t, c.Writer.Header().Get("x-request-id"))
			require.Len(t, repo.rateLimitedUntil, 1)
			// An empty successful message still releases its opening, exactly once.
			result, err = run("winning-header", strings.ReplaceAll(strings.ReplaceAll(opening, "failed_attempt", "winner"), "999", "7")+"data: {\"type\":\"message_stop\"}\n\n")
			require.NoError(t, err)
			require.Equal(t, 7, result.Usage.InputTokens)
			require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"message_start"`))
			require.NotContains(t, rec.Body.String(), "failed_attempt")
			require.Equal(t, "winning-header", rec.Header().Get("x-request-id"))
		})
	}
}

func TestAnthropicToolStartCommitsAndKeepsPartialUsage(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmtBool(passthrough), func(t *testing.T) {
			c, rec := compatStreamContext("/v1/messages", `{}`)
			body := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n" +
				"data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_1\",\"name\":\"lookup\",\"input\":{}}}\n\n" +
				"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy: {\\\"type\\\":\\\"overloaded_error\\\"}\"}}\n\n"
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			account := newAnthropicOAuthAccountForPartialUsageTest()
			if passthrough {
				account = newAnthropicAPIKeyAccountForTest()
			}
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
			require.NoError(t, err)
			result, err := svc.Forward(c.Request.Context(), c, account, parsed)
			var failover *UpstreamFailoverError
			require.Error(t, err)
			require.False(t, errors.As(err, &failover))
			require.NotNil(t, result)
			require.Equal(t, 7, result.Usage.InputTokens)
			require.NotNil(t, result.FirstTokenMs)
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error"))
			require.Contains(t, rec.Body.String(), "tool_1")
			require.NotContains(t, rec.Body.String(), "overloaded_error")
			require.Contains(t, rec.Body.String(), "api_error")
		})
	}
}

func TestAnthropicStagingUsesRemainingBudget(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmtBool(passthrough), func(t *testing.T) {
			c, rec := compatStreamContext("/v1/messages", `{}`)
			settings := resilience.Defaults()
			settings.Budget.PreOutputBudgetSeconds = 1
			state := resilience.NewRequestState()
			state.SetBudget(resilience.NewBudget(c.Request.Context(), &settings, resilience.KindText, time.Now().Add(-850*time.Millisecond)))
			ctx := resilience.WithRequestState(c.Request.Context(), state)
			c.Request = c.Request.WithContext(ctx)
			reader, writer := io.Pipe()
			defer func() { _ = writer.Close() }()
			resp := &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"must-not-leak"}}, Body: reader}
			svc := newStreamingResponseTestGatewayService()
			go func() {
				_, _ = io.WriteString(writer, "data: {\"type\":\"message_start\"}\n\n")
			}()
			started := time.Now()
			var err error
			if passthrough {
				_, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(ctx, resp, c, &Account{ID: 1}, started, "model")
			} else {
				_, err = svc.handleStreamingResponse(ctx, resp, c, &Account{ID: 1}, started, "model", "model", false)
			}
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Less(t, time.Since(started), 750*time.Millisecond)
			require.Empty(t, rec.Body.String())
			require.Empty(t, c.Writer.Header().Get("x-request-id"))
		})
	}
	// The extra semantic deadline never applies to images or calls without a budget.
	for _, kind := range []resilience.RequestKind{resilience.KindText, resilience.KindImage} {
		state := resilience.NewRequestState()
		if kind == resilience.KindImage {
			settings := resilience.Defaults()
			state.SetBudget(resilience.NewBudget(context.Background(), &settings, kind, time.Now()))
		}
		stage := newAnthropicStreamStage(resilience.WithRequestState(context.Background(), state), &gin.Context{}, func() {})
		require.Nil(t, stage.timeout)
		stage.close()
	}
}

type anthropicShortWriter struct{ gin.ResponseWriter }

func (w *anthropicShortWriter) Write(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return w.ResponseWriter.Write(p)
}

func (w *anthropicShortWriter) WriteString(p string) (int, error) { return w.Write([]byte(p)) }

func TestAnthropicStageBoundsAndDeliveryAccounting(t *testing.T) {
	withResilienceSettings(t, nil)
	c, rec := compatStreamContext("/v1/messages", `{}`)
	c.Header("Content-Type", "text/event-stream")
	stage := newAnthropicStreamStage(c.Request.Context(), c, func() { c.Header("x-request-id", "attempt") })
	defer stage.close()
	require.NoError(t, stage.heartbeat(""))
	require.Equal(t, ": ping\n\n", rec.Body.String())
	require.False(t, openAIStreamClientOutputStarted(c, false))
	require.Empty(t, c.Writer.Header().Get("x-request-id"))
	opening := "data: {\"type\":\"message_start\"}\n\n"
	stage.stage.limit = int64(len(opening)*2 - 1)
	_, err := stage.submit(opening)
	require.NoError(t, err)
	_, err = stage.submit(opening)
	require.ErrorIs(t, err, errOpenAIFirstOutputStageLimit)
	require.NotContains(t, rec.Body.String(), "message_start")
	require.LessOrEqual(t, stage.stage.Buffered(), int64(64))

	// A partial event release is irreversible even when only its opening bytes fit.
	c2, _ := compatStreamContext("/v1/messages", `{}`)
	c2.Writer = &anthropicShortWriter{c2.Writer}
	c2.Header("Content-Type", "text/event-stream")
	c2.Request = c2.Request.WithContext(resilience.WithRequestState(c2.Request.Context(), resilience.NewRequestState()))
	InstallDeliveryTracking(c2, time.Now())
	stage2 := newAnthropicStreamStage(c2.Request.Context(), c2, func() {})
	defer stage2.close()
	_, err = stage2.submit(opening)
	require.NoError(t, err)
	_, err = stage2.submit("data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\"}}\n\n")
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.True(t, openAIStreamClientOutputStarted(c2, false))
	require.Positive(t, deliveryWriterFrom(c2).deliveredBytes())

	// An incomplete comment cannot be replayed safely either.
	c3, _ := compatStreamContext("/v1/messages", `{}`)
	c3.Writer = &anthropicShortWriter{c3.Writer}
	stage3 := newAnthropicStreamStage(c3.Request.Context(), c3, func() {})
	defer stage3.close()
	require.ErrorIs(t, stage3.heartbeat(""), io.ErrShortWrite)
	require.True(t, openAIStreamClientOutputStarted(c3, false))

	c4, rec4 := compatStreamContext("/v1/messages", `{}`)
	ctx, cancel := context.WithCancel(c4.Request.Context())
	stage4 := newAnthropicStreamStage(ctx, c4, func() {})
	defer stage4.close()
	_, err = stage4.submit(opening)
	require.NoError(t, err)
	cancel()
	_, err = stage4.submit("event: error\ndata: " + streamErrorEvent("overloaded_error") + "\n\n")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, rec4.Body.String())
}
