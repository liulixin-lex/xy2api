//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Review G1: the Chat Completions and Messages conversions answered by an
// OpenAI Responses upstream build their own request and stream loop, so
// the response-header guard and the stall bookkeeping of Forward did not
// reach them.

// silentUpstream answers like an upstream that goes quiet: with headers
// false it never sends them; otherwise it sends 200 and before, then
// nothing. Either way it ends when the request context does.
type silentUpstream struct {
	headers bool
	before  string
	calls   atomic.Int32
}

func (u *silentUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls.Add(1)
	ctx := req.Context()
	if !u.headers {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(u.before))
		<-ctx.Done()
		_ = pw.CloseWithError(ctx.Err())
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: pr, Request: req}, nil
}

func (u *silentUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func compatStreamService(upstream HTTPUpstream, firstOutput, interval, keepalive int) *OpenAIGatewayService {
	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway = config.GatewayConfig{
		OpenAIFirstOutputTimeoutSeconds: firstOutput,
		StreamDataIntervalTimeout:       interval,
		StreamKeepaliveInterval:         keepalive,
		MaxLineSize:                     defaultMaxLineSize,
	}
	return &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
}

func compatStreamContext(path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

const (
	compatMessagesStreamBody = `{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`
	compatChatStreamBody     = `{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`
)

func requireFirstOutputFailover(t *testing.T, err error) {
	t.Helper()
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "a silent attempt before any output is switched: %v", err)
	require.Contains(t, string(failoverErr.ResponseBody), "first_output_timeout")
}

func TestCompatConversionsSwitchWhenTheUpstreamHeadersDoNotCome(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct{ path, body string }{
		{"/v1/messages", compatMessagesStreamBody},
		{"/v1/chat/completions", compatChatStreamBody},
	} {
		t.Run(tc.path, func(t *testing.T) {
			svc := compatStreamService(&silentUpstream{}, 1, 30, 0)
			c, rec := compatStreamContext(tc.path, tc.body)
			start := time.Now()
			var err error
			if tc.path == "/v1/messages" {
				_, err = svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), []byte(tc.body), "", "")
			} else {
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), []byte(tc.body), "", "")
			}
			requireFirstOutputFailover(t, err)
			require.Less(t, time.Since(start), 10*time.Second, "the tier ends the header wait")
			require.Empty(t, rec.Body.String(), "nothing reached the client")
		})
	}
}

func TestCompatConversionsSwitchWhenTheFirstEventDoesNotCome(t *testing.T) {
	// Headers, then nothing. The 1 s stall check leaves the attempt to the
	// 3 s first-output tier; the 1 s downstream pings in between are
	// heartbeats, so the handler may still switch accounts.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.PreludeStaging = false })
	for _, tc := range []struct{ path, body string }{
		{"/v1/messages", compatMessagesStreamBody},
		{"/v1/chat/completions", compatChatStreamBody},
	} {
		t.Run(tc.path, func(t *testing.T) {
			svc := compatStreamService(&silentUpstream{headers: true}, 3, 1, 1)
			c, rec := compatStreamContext(tc.path, tc.body)
			before := OpenAICompactKeepaliveAdjustedWrittenSize(c)
			start := time.Now()
			var err error
			if tc.path == "/v1/messages" {
				_, err = svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), []byte(tc.body), "", "")
			} else {
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), []byte(tc.body), "", "")
			}
			requireFirstOutputFailover(t, err)
			require.GreaterOrEqual(t, time.Since(start), 2500*time.Millisecond, "the tier, not the stall check, ends the attempt")
			require.NotEmpty(t, rec.Body.String(), "the downstream keepalive wrote heartbeats meanwhile")
			require.Equal(t, before, OpenAICompactKeepaliveAdjustedWrittenSize(c), "only heartbeats were written: the handler may switch")
		})
	}
}

func compatStallState() (context.Context, *recordingSessions) {
	rec := newRecordingSessions()
	return postOutputRequest(rec), rec
}

func compatStallMetric(accountID int64) int64 {
	var n int64
	for _, row := range resilience.Snapshot(time.Minute) {
		if row.Name == resilience.MetricStall && row.AccountID == accountID {
			n += row.Count
		}
	}
	return n
}

func TestCompatConversionsStallAfterOutputIsCounted(t *testing.T) {
	// Output, then silence: the 1 s text stall threshold (not the 30 s
	// interval) ends the stream; the account gets the stall bookkeeping of
	// the Responses path (cooldown, session failure, failed turn).
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stall.TextStallSeconds = 1 })
	for i, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			svc := compatStreamService(nil, 0, 30, 0)
			ctx, sessions := compatStallState()
			c, _ := compatStreamContext(path, "")
			c.Request = c.Request.WithContext(ctx)
			account := &Account{ID: int64(7200 + i), Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			pr, pw := io.Pipe()
			defer func() { _ = pw.Close() }()
			go func() { _, _ = pw.Write([]byte(keepaliveStallCreated + keepaliveStallDelta1)) }()
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}
			before := compatStallMetric(account.ID)

			start := time.Now()
			var err error
			if path == "/v1/messages" {
				_, err = svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", start)
			} else {
				_, err = svc.handleChatStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", start, 0)
			}
			require.ErrorContains(t, err, "stream data interval timeout")
			require.Less(t, time.Since(start), 10*time.Second, "the text stall threshold applies after output")
			require.Equal(t, before+1, compatStallMetric(account.ID))
			require.True(t, resilience.StateFrom(ctx).TurnFailed())
			require.Equal(t, []resilience.Category{resilience.CategoryStall}, sessions.of(account.ID))
		})
	}
}

func TestCompatBufferedConversionStallIsCounted(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := compatStreamService(nil, 0, 1, 0)
	ctx, sessions := compatStallState()
	c, _ := compatStreamContext("/v1/chat/completions", "")
	c.Request = c.Request.WithContext(ctx)
	account := &Account{ID: 7210, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	go func() { _, _ = pw.Write([]byte(keepaliveStallCreated)) }()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	_, err := svc.handleChatBufferedStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
	require.ErrorContains(t, err, "stream data interval timeout")
	require.True(t, resilience.StateFrom(ctx).TurnFailed())
	require.Equal(t, []resilience.Category{resilience.CategoryStall}, sessions.of(account.ID))
}

func TestOtherStreamStallExitsAreCounted(t *testing.T) {
	// The Anthropic API-key passthrough, Bedrock and the CN Anthropic-native
	// Messages route end a stalled stream like the others now: STALL
	// cooldown, session failure and failed turn (their thresholds stay).
	withResilienceSettings(t, nil)
	cfg := &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1, MaxLineSize: defaultMaxLineSize}}
	for i, name := range []string{"passthrough", "bedrock", "native"} {
		t.Run(name, func(t *testing.T) {
			ctx, sessions := compatStallState()
			c, _ := compatStreamContext("/v1/messages", "")
			c.Request = c.Request.WithContext(ctx)
			account := &Account{ID: int64(7220 + i), Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
			pr, pw := io.Pipe()
			defer func() { _ = pw.Close() }()
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: pr}
			var err error
			switch name {
			case "passthrough":
				_, err = (&GatewayService{cfg: cfg}).handleStreamingResponseAnthropicAPIKeyPassthrough(ctx, resp, c, account, time.Now(), "claude-sonnet-5-5")
			case "bedrock":
				_, err = (&GatewayService{cfg: cfg}).handleBedrockStreamingResponse(ctx, resp, c, account, time.Now(), "claude-sonnet-5-5")
			case "native":
				account.Platform = PlatformOpenAI
				_, err = (&OpenAIGatewayService{cfg: cfg}).handleNativeAnthropicStreamingResponse(ctx, resp, c, account, "glm-5", "glm-5", "glm-5", nil, time.Now())
			}
			require.ErrorContains(t, err, "stream data interval timeout")
			require.True(t, resilience.StateFrom(ctx).TurnFailed())
			require.Equal(t, []resilience.Category{resilience.CategoryStall}, sessions.of(account.ID))
		})
	}
}
