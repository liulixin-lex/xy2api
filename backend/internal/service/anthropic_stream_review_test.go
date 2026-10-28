//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicQueueHeartbeatStillAllowsOpeningFailover(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name        string
		passthrough bool
	}{
		{"native", false},
		{"passthrough", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/messages", "{}")
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
			InstallDeliveryTracking(c, time.Now())
			c.Header("Content-Type", "text/event-stream")
			// Queue heartbeats register only with delivery tracking, as in the handler.
			const ping = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
			n, err := io.WriteString(c.Writer, ping)
			require.NoError(t, err)
			require.True(t, NoteDeliveryHeartbeat(c, n))
			c.Writer.Flush()
			require.GreaterOrEqual(t, OpenAICompactKeepaliveAdjustedWrittenSize(c), 0, "queue bytes are absent from the compact keepalive counter")
			require.True(t, openAIStreamClientOutputStarted(c, true), "an explicit local commit still wins")

			body := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n" +
				"event: error\ndata: " + streamErrorEvent("overloaded_error") + "\n\n"
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			svc.rateLimitService = NewRateLimitService(&streamErrorRepo{}, nil, svc.cfg, nil, nil)
			account := newAnthropicOAuthAccountForPartialUsageTest()
			if tc.passthrough {
				account = newAnthropicAPIKeyAccountForTest()
			}
			before := postOutputMetric(account.ID, postOutputEvent)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte("{\"model\":\"claude-sonnet-4-5\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}")), PlatformAnthropic)
			require.NoError(t, err)
			result, err := svc.Forward(c.Request.Context(), c, account, parsed)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Nil(t, result)
			require.False(t, openAIStreamClientOutputStarted(c, false))
			require.Equal(t, ping, rec.Body.String(), "the failed opening never reached the client")
			require.False(t, resilience.StateFrom(c.Request.Context()).StreamFailureReported(account.ID))
			require.Equal(t, before, postOutputMetric(account.ID, postOutputEvent))
		})
	}
}

func TestAnthropicCapacityProjectionFollowsErrorsSwitch(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run("errors_"+fmtBool(enabled), func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Errors.Enabled = enabled })
			for _, outlet := range []string{"native", "passthrough", "http_mapper"} {
				t.Run(outlet, func(t *testing.T) {
					c, rec := compatStreamContext("/v1/messages", "{}")
					const errorJSON = "{\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy: {\\\"type\\\":\\\"overloaded_error\\\"} https://upstream.test/?key=private-marker\"}}"
					body := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n" +
						"data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_1\",\"name\":\"lookup\",\"input\":{}}}\n\n" +
						"event: error\ndata: " + errorJSON + "\n\n"
					upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}
					svc := newForwardPartialUsageServiceForTest(upstream)
					account := newAnthropicOAuthAccountForPartialUsageTest()
					wantType := "overloaded_error"
					if enabled {
						wantType = "api_error"
					}
					if outlet == "http_mapper" {
						svc.rateLimitService = nil // Exercise the fallback mapper after account effects.
						_, err := svc.handleErrorResponse(c.Request.Context(), &http.Response{StatusCode: 529, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(errorJSON))}, c, account)
						require.Error(t, err)
						require.Equal(t, http.StatusServiceUnavailable, rec.Code)
						require.Equal(t, wantType, gjson.Get(rec.Body.String(), "error.type").String())
						return
					}
					if outlet == "passthrough" {
						account = newAnthropicAPIKeyAccountForTest()
					}
					parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte("{\"model\":\"claude-sonnet-4-5\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}")), PlatformAnthropic)
					require.NoError(t, err)
					result, err := svc.Forward(c.Request.Context(), c, account, parsed)
					var failover *UpstreamFailoverError
					require.Error(t, err)
					require.False(t, errors.As(err, &failover))
					require.NotNil(t, result)
					errorOffset := strings.Index(rec.Body.String(), "event: error\n")
					require.NotEqual(t, -1, errorOffset)
					_, data := anthropicSSEEvent(rec.Body.String()[errorOffset:])
					require.Equal(t, wantType, gjson.Get(data, "error.type").String())
					require.Contains(t, gjson.Get(data, "error.message").String(), wantType)
					require.NotContains(t, data, "private-marker", "Errors off must still sanitize client strings")
					if enabled {
						require.NotContains(t, data, "overloaded_error")
					}
				})
			}
		})
	}
}

func TestAnthropicReleasedHeadersKeepStableSSEValues(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name        string
		passthrough bool
	}{
		{"native", false},
		{"passthrough", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/messages", "{}")
			body := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n"
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{
				"Content-Type": {"text/event-stream"}, "Cache-Control": {"public, max-age=3600"}, "X-Request-Id": {"winner"},
			}, Body: io.NopCloser(strings.NewReader(body))}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			account := newAnthropicOAuthAccountForPartialUsageTest()
			if tc.passthrough {
				account = newAnthropicAPIKeyAccountForTest()
			}
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte("{\"model\":\"claude-sonnet-4-5\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}")), PlatformAnthropic)
			require.NoError(t, err)
			_, err = svc.Forward(c.Request.Context(), c, account, parsed)
			require.NoError(t, err)
			response := rec.Result()
			defer func() { _ = response.Body.Close() }()
			for key, want := range map[string]string{
				"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "Connection": "keep-alive", "X-Accel-Buffering": "no",
			} {
				require.Equal(t, []string{want}, response.Header.Values(key), key)
			}
			require.Equal(t, "winner", response.Header.Get("x-request-id"))
		})
	}
}
