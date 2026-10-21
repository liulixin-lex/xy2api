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
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSyntheticFailedFramesResolveRetryAdvice(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, kind := range []string{"keepalive", "compact", "native", "passthrough"} {
		for _, advice := range []resilience.RetryAdvice{{Seconds: 1, Source: "healthy_candidate"}, {Seconds: 7, Source: "recovery"}, {Source: "unknown"}, {Suppress: true, Source: "turn_cap"}} {
			t.Run(kind+"/"+advice.Source, func(t *testing.T) {
				c, rec := newKeepaliveTestContext(t)
				c.Request = c.Request.WithContext(resilience.WithRetryAdviceProvider(c.Request.Context(), func(context.Context) resilience.RetryAdvice { return advice }))
				InstallDeliveryTracking(c, time.Now())
				const message = "busy. Please try again in 30s."
				switch kind {
				case "keepalive":
					stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, 0, time.Hour)
					defer stop()
					waitCommitted(t, c)
					c.JSON(503, gin.H{"error": gin.H{"message": message}})
				case "compact":
					writeOpenAICompactSSEFailureMessage(c, 503, "upstream_error", message)
				default:
					body := keepaliveStallCreated + keepaliveStallDelta1 + `data: {"type":"error","error":{"type":"server_error","code":"upstream_error","message":"` + message + `"}}` + "\n\n"
					resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
					account := rawChatCompletionsTestAccount()
					account.Type = AccountTypeOAuth
					svc := compatStreamService(nil, 0, 0, 0)
					if kind == "native" {
						_, _ = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
					} else {
						_, _ = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
					}
				}
				var failed gjson.Result
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "response.failed" {
						failed = gjson.Parse(data)
					}
				}
				require.True(t, failed.Exists(), rec.Body.String())
				require.Equal(t, advice.Headers()["retry-after"], failed.Get("response.error.headers.retry-after").String())
				require.Equal(t, advice.Message(message), failed.Get("response.error.message").String())
			})
		}
	}
}

func TestCommittedAnthropicGateway503UsesAPIError(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		status     int
		body, want string
	}{
		{503, `{"error":{"message":"no accounts"}}`, "api_error"},
		{503, `{"error":{"type":"overloaded_error","message":"real upstream overload"}}`, "overloaded_error"},
		{529, `{"error":{"type":"overloaded_error","message":"real upstream overload"}}`, "overloaded_error"},
	} {
		frame := string(preOutputTerminalFrame(context.Background(), PreOutputProtocolAnthropic, tc.status, []byte(tc.body)))
		require.Contains(t, frame, `"type":"`+tc.want+`"`)
	}
}

func TestSyntheticFailedContentRejectionSuppressesEarlierAdvice(t *testing.T) {
	withResilienceSettings(t, nil)
	ctx := resilience.WithRetryAdvice(context.Background(), resilience.RetryAdvice{Seconds: 7, Source: "recovery"})
	frame := string(preOutputTerminalFrame(ctx, PreOutputProtocolResponses, 400, []byte(`{"error":{"code":"content_policy_violation","message":"blocked. Please try again in 7s."}}`)))
	require.NotContains(t, frame, "retry-after")
	require.NotContains(t, frame, "try again")
	require.True(t, resilience.RetryAdviceFrom(ctx).Suppress)
}

func TestSyntheticFailedTypedContentRejection(t *testing.T) {
	withResilienceSettings(t, nil)
	ctx := resilience.WithRetryAdvice(context.Background(), resilience.RetryAdvice{Seconds: 7})
	frame := buildOpenAIResponseFailedSSE(ctx, "resp_a", "model", []byte(`{"error":{"type":"content_policy_violation","message":"blocked"}}`), "")
	require.NotContains(t, frame, "retry-after")
	require.NotContains(t, frame, "try again")
	require.True(t, resilience.RetryAdviceFrom(ctx).Suppress)
}

func TestSyntheticFailedRetryAdviceDisabled(t *testing.T) {
	for _, gate := range []string{"master", "errors", "failed_frame"} {
		t.Run(gate, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) {
				switch gate {
				case "master":
					s.Enabled = false
				case "errors":
					s.Errors.Enabled = false
				case "failed_frame":
					s.Errors.FailedFrameRetryAfter = false
				}
			})
			ctx := resilience.WithRetryAdviceProvider(context.Background(), func(context.Context) resilience.RetryAdvice {
				t.Error("disabled advice was queried")
				return resilience.RetryAdvice{Seconds: 7}
			})
			frame := string(preOutputTerminalFrame(ctx, PreOutputProtocolResponses, 503, []byte(`{"error":{"message":"busy. Please try again in 30s."}}`)))
			require.NotContains(t, frame, "retry-after")
			require.Contains(t, frame, "Please try again in 30s.")
		})
	}
}

func TestPreOutputRetryAdviceSeesLateSuppression(t *testing.T) {
	withResilienceSettings(t, nil)
	c, rec := newKeepaliveTestContext(t)
	c.Request = c.Request.WithContext(resilience.WithRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Seconds: 7}))
	InstallDeliveryTracking(c, time.Now())
	stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, 0, time.Hour)
	defer stop()
	waitCommitted(t, c)
	resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true, Source: "turn_cap"})
	c.JSON(503, gin.H{"error": gin.H{"message": "busy. Please try again in 7s."}})
	require.NotContains(t, rec.Body.String(), "retry-after")
	require.NotContains(t, rec.Body.String(), "try again")
}
