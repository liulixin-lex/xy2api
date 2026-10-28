//go:build unit

package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStageHeartbeatExhaustionWritesOneMessagesSSETerminal(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, entry := range []string{"anthropic", "openai to messages"} {
		t.Run(entry, func(t *testing.T) {
			c, w := newResilienceTestContext(t)
			service.InstallDeliveryTracking(c, time.Now())
			before := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
			c.Header("Content-Type", "text/event-stream")
			const ping = ": ping\n\n"
			n, err := c.Writer.WriteString(ping)
			require.NoError(t, err)
			c.Set("openai_stream_keepalive_bytes", n)
			require.True(t, service.NoteDeliveryHeartbeat(c, n))
			c.Writer.Flush()
			require.Equal(t, before, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
			require.True(t, c.Writer.Written())
			require.False(t, service.StopOpenAICompactSSEKeepaliveCommitted(c), "stage heartbeat has no compact object")

			failure := &service.UpstreamFailoverError{StatusCode: 529}
			if entry == "openai to messages" {
				(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, failure, false)
			} else {
				(&GatewayHandler{}).handleFailoverExhausted(c, failure, service.PlatformAnthropic, false)
			}
			require.Equal(t, http.StatusOK, w.Code)
			body := w.Body.String()
			require.True(t, strings.HasPrefix(body, ping+"event: error\n"), body)
			require.Equal(t, 1, strings.Count(body, "event: error\n"))
			events := parseSSEEvents(body)
			require.Len(t, events, 1)
			require.Equal(t, "error", events[0].name)
			require.Equal(t, "error", gjson.Get(events[0].data, "type").String())
			require.Equal(t, ping+"event: error\ndata: "+events[0].data+"\n\n", body, "no bare JSON after the heartbeat")
		})
	}
}

func TestHardFailureRetryVetoDoesNotDependOnBudget(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		withHandlerResilience(t, func(s *resilience.Settings) { s.Enabled = enabled })
		for _, ctx := range []context.Context{context.Background(), budgetCtx(t, resilience.KindText, 7)} {
			for _, failure := range []*service.UpstreamFailoverError{
				{StatusCode: 400, ResponseBody: []byte(`{"error":{"message":"insufficient balance"}}`)},
				{StatusCode: 401, ResponseBody: []byte(`{"error":{"code":"invalid_api_key"}}`)},
				{StatusCode: 400, ResponseBody: []byte(`{"error":{"message":"unknown model x"}}`)},
			} {
				failure.RetryableOnSameAccount = true
				require.Equal(t, !enabled, sameAccountRetryAllowedCtx(ctx, 1, failure, 0, 3))
			}
		}
		for _, status := range []int{502, 529, 504} {
			require.True(t, sameAccountRetryAllowedCtx(context.Background(), 1, &service.UpstreamFailoverError{StatusCode: status, RetryableOnSameAccount: true}, 0, 3))
		}
	}
}

func TestGeminiExhaustionHonorsErrorsSwitchAndSuppression(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.Enabled = enabled })
		c, w := newResilienceTestContext(t)
		if enabled {
			resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true, Source: "turn_cap"})
		}
		(&GatewayHandler{}).handleGeminiFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: 502, ResponseHeaders: http.Header{"Retry-After": {"90"}},
		})
		wantStatus := 502
		if enabled {
			wantStatus = 503
		}
		require.Equal(t, wantStatus, w.Code)
		require.Empty(t, w.Header().Get("Retry-After"))
	}
}

func TestGoogleConcurrencyRetryHintsPreserveExistingAndSuppression(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name     string
		err      error
		existing string
		suppress bool
		want     string
	}{
		{"queue", &WaitQueueFullError{SlotType: "account"}, "", false, "5"},
		{"user", &ConcurrencyError{SlotType: "user"}, "", false, "5"},
		{"account existing", &ConcurrencyError{SlotType: "account"}, "8", false, "8"},
		{"suppressed", &ConcurrencyError{SlotType: "user"}, "", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, w := newResilienceTestContext(t)
			if tc.existing != "" {
				c.Header("Retry-After", tc.existing)
			}
			if tc.suppress {
				resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true})
			}
			googleConcurrencyError(c, tc.err, "user")
			require.Equal(t, http.StatusTooManyRequests, w.Code)
			require.Equal(t, tc.want, w.Header().Get("Retry-After"))
		})
	}
}
