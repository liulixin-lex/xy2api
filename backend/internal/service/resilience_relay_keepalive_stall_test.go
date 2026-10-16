//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// A relay that keeps a quiet stream open only with its keep-alive frames is
// alive: the filter drops the frames (design §7.8) but they still count for
// the stall detectors, as they did in 0.3.0, without counting as output.
// Production review 2026-10-16: without this, such a relay is cut as
// stalled 120 s after output (180 s before) and its account cooled down.

const (
	keepaliveStallCreated = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"
	keepaliveStallDelta1  = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"hello\"}\n\n"
	keepaliveStallDelta2  = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\" world\"}\n\n"
	keepaliveStallDone    = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n"
)

// keepaliveOnlyUpstream writes before, then only relay keep-alive frames
// every 100 ms for quiet, then after.
func keepaliveOnlyUpstream(before string, quiet time.Duration, after string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(before))
		for end := time.Now().Add(quiet); time.Now().Before(end); {
			time.Sleep(100 * time.Millisecond)
			if _, err := pw.Write([]byte(relayKeepaliveFrame)); err != nil {
				return
			}
		}
		_, _ = pw.Write([]byte(after))
		_ = pw.Close()
	}()
	return pr
}

func keepaliveStallContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, rec
}

func relayKeepaliveCount(accountID int64) int64 {
	var n int64
	for _, row := range resilience.Snapshot(time.Minute) {
		if row.Name == resilience.MetricRelayKeepalive && row.AccountID == accountID {
			n += row.Count
		}
	}
	return n
}

func TestRelayKeepaliveFramesKeepAStreamAliveAfterOutput(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stall.TextStallSeconds = 1 })
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamDataIntervalTimeout: 30, MaxLineSize: defaultMaxLineSize}}}
	c, rec := keepaliveStallContext("/v1/responses")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: keepaliveOnlyUpstream(keepaliveStallCreated+keepaliveStallDelta1, 2500*time.Millisecond, keepaliveStallDelta2+keepaliveStallDone)}
	account := &Account{ID: 7101, Platform: PlatformOpenAI}

	_, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-5.1", "gpt-5.1")
	require.NoError(t, err, "keep-alive frames are no stall")
	body := rec.Body.String()
	require.Contains(t, body, " world")
	require.Contains(t, body, "response.completed")
	require.NotContains(t, body, "SSE-Keep-Alive", "frames still never reach the client")
	require.GreaterOrEqual(t, relayKeepaliveCount(account.ID), int64(10), "dropped frames are counted per account")
}

func TestRelayKeepaliveFramesKeepAStreamAliveBeforeOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamDataIntervalTimeout: 1, MaxLineSize: defaultMaxLineSize}}}
	c, rec := keepaliveStallContext("/v1/responses")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: keepaliveOnlyUpstream(keepaliveStallCreated, 2500*time.Millisecond, keepaliveStallDelta1+keepaliveStallDone)}

	_, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 7102, Platform: PlatformOpenAI}, time.Now(), "gpt-5.1", "gpt-5.1")
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), "hello")
	require.NotContains(t, rec.Body.String(), "SSE-Keep-Alive")
}

func TestRelayKeepaliveFramesKeepAChatCompletionsStreamAlive(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamDataIntervalTimeout: 1, MaxLineSize: defaultMaxLineSize}}}
	c, rec := keepaliveStallContext("/v1/chat/completions")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: keepaliveOnlyUpstream(keepaliveStallCreated+keepaliveStallDelta1, 2500*time.Millisecond, keepaliveStallDelta2+keepaliveStallDone)}

	_, err := svc.handleChatStreamingResponse(resp, c, &Account{ID: 7103, Platform: PlatformOpenAI}, "gpt-5.1", "gpt-5.1", "gpt-5.1", time.Now(), 0)
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), " world")
	require.NotContains(t, rec.Body.String(), "SSE-Keep-Alive")
}

func TestRelayKeepaliveFramesKeepABufferedConversionAlive(t *testing.T) {
	// A non-streaming client of a Responses upstream: the gateway reads the
	// stream to its terminal event with the same interval timeout.
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamDataIntervalTimeout: 1, MaxLineSize: defaultMaxLineSize}}}
	c, rec := keepaliveStallContext("/v1/chat/completions")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: keepaliveOnlyUpstream(keepaliveStallCreated+keepaliveStallDelta1, 2500*time.Millisecond, keepaliveStallDelta2+keepaliveStallDone)}

	_, err := svc.handleChatBufferedStreamingResponse(resp, c, &Account{ID: 7104, Platform: PlatformOpenAI}, "gpt-5.1", "gpt-5.1", "gpt-5.1", time.Now())
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), "hello world")
	require.GreaterOrEqual(t, relayKeepaliveCount(7104), int64(10))
}

func TestRelayKeepaliveFramesKeepAMessagesConversionAlive(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamDataIntervalTimeout: 1, MaxLineSize: defaultMaxLineSize}}}
	c, rec := keepaliveStallContext("/v1/messages")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: keepaliveOnlyUpstream(keepaliveStallCreated+keepaliveStallDelta1, 2500*time.Millisecond, keepaliveStallDelta2+keepaliveStallDone)}

	_, err := svc.handleAnthropicStreamingResponse(resp, c, &Account{ID: 7105, Platform: PlatformOpenAI}, "gpt-5.1", "gpt-5.1", "gpt-5.1", time.Now())
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), " world")
	require.NotContains(t, rec.Body.String(), "SSE-Keep-Alive")
}
