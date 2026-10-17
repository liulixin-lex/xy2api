//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// The first-token time of the request log must not grow with the upgrade
// (user requirement, 2026-10-17). 0.3.0 passed relay keep-alive frames
// through, and they counted as the first token; 0.4.0 drops them (design
// §7.8). In semantic mode the first dropped frame counts again, from the
// start of the attempt; visible mode, and every timing decision of the
// stream, keep seeing only real events.

const (
	ttftOutputDelay = 400 * time.Millisecond
	ttftChatChunk   = "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"
)

func withOpenAITTFTMode(t *testing.T, mode string) {
	t.Helper()
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{openAITTFTMode: mode, expiresAt: time.Now().Add(time.Minute).UnixNano()})
	t.Cleanup(func() {
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{openAITTFTMode: OpenAITTFTModeSemantic, expiresAt: time.Now().Add(time.Minute).UnixNano()})
	})
}

// frameThenOutput sends one relay keep-alive frame at once and the real
// events after delay.
func frameThenOutput(delay time.Duration, after string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(relayKeepaliveFrame))
		time.Sleep(delay)
		_, _ = pw.Write([]byte(after))
		_ = pw.Close()
	}()
	return pr
}

type ttftPath struct {
	name    string
	account int64
	body    string
	run     func(t *testing.T, svc *OpenAIGatewayService, resp *http.Response, account *Account, start time.Time) (*int, string)
}

func ttftPaths() []ttftPath {
	responses := keepaliveStallCreated + keepaliveStallDelta1 + keepaliveStallDone
	return []ttftPath{
		{"responses", 7201, responses, func(t *testing.T, svc *OpenAIGatewayService, resp *http.Response, account *Account, start time.Time) (*int, string) {
			c, rec := keepaliveStallContext("/v1/responses")
			r, err := svc.handleStreamingResponse(context.Background(), resp, c, account, start, "gpt-5.1", "gpt-5.1")
			require.NoError(t, err)
			return r.firstTokenMs, rec.Body.String()
		}},
		{"passthrough", 7202, responses, func(t *testing.T, svc *OpenAIGatewayService, resp *http.Response, account *Account, start time.Time) (*int, string) {
			c, rec := keepaliveStallContext("/v1/responses")
			r, err := svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, start, "gpt-5.1", "gpt-5.1")
			require.NoError(t, err)
			return r.firstTokenMs, rec.Body.String()
		}},
		{"chat completions", 7203, responses, func(t *testing.T, svc *OpenAIGatewayService, resp *http.Response, account *Account, start time.Time) (*int, string) {
			c, rec := keepaliveStallContext("/v1/chat/completions")
			r, err := svc.handleChatStreamingResponse(resp, c, account, "gpt-5.1", "gpt-5.1", "gpt-5.1", start, 0)
			require.NoError(t, err)
			return r.FirstTokenMs, rec.Body.String()
		}},
		{"messages", 7204, responses, func(t *testing.T, svc *OpenAIGatewayService, resp *http.Response, account *Account, start time.Time) (*int, string) {
			c, rec := keepaliveStallContext("/v1/messages")
			r, err := svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-5.1", "gpt-5.1", "gpt-5.1", start)
			require.NoError(t, err)
			return r.FirstTokenMs, rec.Body.String()
		}},
		{"raw chat completions", 7205, ttftChatChunk, func(t *testing.T, svc *OpenAIGatewayService, resp *http.Response, account *Account, start time.Time) (*int, string) {
			c, rec := keepaliveStallContext("/v1/chat/completions")
			r, err := svc.streamRawChatCompletions(c, resp, account, "gpt-5.1", "gpt-5.1", "gpt-5.1", nil, nil, start, 0)
			require.NoError(t, err)
			return r.FirstTokenMs, rec.Body.String()
		}},
	}
}

func TestRelayKeepaliveFrameCountsAsTheSemanticFirstToken(t *testing.T) {
	for _, p := range ttftPaths() {
		for _, mode := range []string{OpenAITTFTModeSemantic, OpenAITTFTModeVisible} {
			t.Run(p.name+"/"+mode, func(t *testing.T) {
				withResilienceSettings(t, nil)
				withOpenAITTFTMode(t, mode)
				svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: frameThenOutput(ttftOutputDelay, p.body)}
				before := relayKeepaliveCount(p.account)
				first, body := p.run(t, svc, resp, &Account{ID: p.account, Platform: PlatformOpenAI}, time.Now())
				require.NotNil(t, first)
				if mode == OpenAITTFTModeSemantic {
					require.Less(t, *first, 150, "semantic: the dropped frame at once")
				} else {
					require.GreaterOrEqual(t, *first, 350, "visible: the real output only")
				}
				require.NotContains(t, body, "SSE-Keep-Alive", "the frame never reaches the client")
				require.Equal(t, before+1, relayKeepaliveCount(p.account), "the dropped frame is counted for the account")
			})
		}
	}
}

// The chat fallbacks drop the frames and count them for the account too.
// Their first token stays the first output chunk in both modes: 0.3.0 passed
// the frame through there but never counted it, the chunk has no choices.
func TestRelayKeepaliveFramesInTheChatFallbacks(t *testing.T) {
	paths := []ttftPath{
		{"responses", 7211, ttftChatChunk, func(t *testing.T, svc *OpenAIGatewayService, resp *http.Response, account *Account, start time.Time) (*int, string) {
			c, rec := keepaliveStallContext("/v1/responses")
			r, err := svc.streamChatCompletionsAsResponses(c, resp, account, "gpt-5.1", nil, nil, false, nil, "gpt-5.1", "gpt-5.1", nil, nil, start)
			require.NoError(t, err)
			return r.FirstTokenMs, rec.Body.String()
		}},
		{"messages", 7212, ttftChatChunk, func(t *testing.T, svc *OpenAIGatewayService, resp *http.Response, account *Account, start time.Time) (*int, string) {
			c, rec := keepaliveStallContext("/v1/messages")
			r, err := svc.streamChatCompletionsAsAnthropic(c, resp, account, "gpt-5.1", "gpt-5.1", "gpt-5.1", nil, nil, start)
			require.NoError(t, err)
			return r.FirstTokenMs, rec.Body.String()
		}},
	}
	for _, p := range paths {
		for _, mode := range []string{OpenAITTFTModeSemantic, OpenAITTFTModeVisible} {
			t.Run(p.name+"/"+mode, func(t *testing.T) {
				withResilienceSettings(t, nil)
				withOpenAITTFTMode(t, mode)
				svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: frameThenOutput(ttftOutputDelay, p.body)}
				before := relayKeepaliveCount(p.account)
				first, body := p.run(t, svc, resp, &Account{ID: p.account, Platform: PlatformOpenAI}, time.Now())
				require.NotNil(t, first)
				require.GreaterOrEqual(t, *first, 350, "the first output chunk, as in 0.3.0")
				require.Contains(t, body, "hello")
				require.NotContains(t, body, "SSE-Keep-Alive")
				require.Equal(t, before+1, relayKeepaliveCount(p.account))
			})
		}
	}
}

// The early first token is a record only: the first-output timer of the
// attempt still waits for real output, so the attempt is abandoned at the
// deadline although frames kept arriving.
func TestRelayKeepaliveFramesStillDoNotStopTheFirstOutputTimer(t *testing.T) {
	withResilienceSettings(t, nil)
	withOpenAITTFTMode(t, OpenAITTFTModeSemantic)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: defaultMaxLineSize, OpenAIFirstOutputTimeoutSeconds: 1}}}
	c, rec := keepaliveStallContext("/v1/responses")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: keepaliveOnlyUpstream("", 1500*time.Millisecond, keepaliveStallCreated+keepaliveStallDelta1+keepaliveStallDone)}
	start := time.Now()
	_, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 7206, Platform: PlatformOpenAI}, start, "gpt-5.1", "gpt-5.1")
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr, "the first-output deadline still applies")
	require.Less(t, time.Since(start), 1400*time.Millisecond)
	require.NotContains(t, rec.Body.String(), "SSE-Keep-Alive")
}

// relayLiveness records the first dropped frame once.
func TestRelayLivenessKeepsTheFirstDrop(t *testing.T) {
	start := time.Now()
	l := newRelayLiveness(context.Background(), &Account{ID: 7207, Platform: PlatformOpenAI})
	l.frameDropped()
	first := l.firstDrop.Load()
	time.Sleep(20 * time.Millisecond)
	l.frameDropped()
	require.Equal(t, first, l.firstDrop.Load())
	recorded := 300
	got := l.firstTokenMs(&recorded, start, OpenAITTFTModeSemantic)
	require.NotNil(t, got)
	require.Less(t, *got, 20)
	require.Equal(t, &recorded, l.firstTokenMs(&recorded, start, OpenAITTFTModeVisible))
	var none *relayLiveness
	require.Equal(t, &recorded, none.firstTokenMs(&recorded, start, OpenAITTFTModeSemantic))
}
