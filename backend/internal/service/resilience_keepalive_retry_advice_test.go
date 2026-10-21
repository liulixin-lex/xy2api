//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSyntheticFailedFramesShareRetryEligibility(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, kind := range []string{"keepalive", "compact", "native"} {
		for _, tc := range []struct {
			name, errType, code string
			status              int
			eligible            bool
		}{
			{"invalid_request", "invalid_request_error", "invalid_request", 400, false},
			{"context_length", "upstream_error", "context_length_exceeded", 400, false},
			{"model_not_found", "upstream_error", "model_not_found", 404, false},
			{"unknown_400", "upstream_error", "vendor_request_error", 400, false},
			{"model_without_status", "model_not_found", "model_not_found", 0, false},
			{"server_failure", "api_error", "upstream_error", 503, true},
			{"overloaded", "overloaded_error", "server_is_overloaded", 529, true},
			{"overloaded_without_status", "overloaded_error", "server_is_overloaded", 0, true},
			{"rate_limit", "rate_limit_error", "rate_limit_exceeded", 429, true},
		} {
			for _, adviceSource := range []string{"provider", "explicit"} {
				t.Run(kind+"/"+tc.name+"/"+adviceSource, func(t *testing.T) {
					c, rec := newKeepaliveTestContext(t)
					var providerCalls atomic.Int32
					ctx := resilience.WithRetryAdviceProvider(c.Request.Context(), func(context.Context) resilience.RetryAdvice {
						providerCalls.Add(1)
						return resilience.RetryAdvice{Seconds: 1, Source: "healthy_candidate"}
					})
					if adviceSource == "explicit" {
						resilience.SetRetryAdvice(ctx, resilience.RetryAdvice{Seconds: 1, Source: "healthy_candidate"})
					}
					c.Request = c.Request.WithContext(ctx)
					const message = "Upstream rejected the request. Please try again in 1s."
					errorBody, err := json.Marshal(gin.H{"error": gin.H{"type": tc.errType, "code": tc.code, "status_code": tc.status, "message": message}})
					require.NoError(t, err)
					var frame string
					switch kind {
					case "keepalive":
						frame = string(preOutputTerminalFrame(ctx, PreOutputProtocolResponses, tc.status, errorBody))
					case "compact":
						writeOpenAICompactSSEFailureMessage(c, tc.status, tc.code, message)
						frame = rec.Body.String()
					default:
						frame = buildOpenAIResponseFailedSSE(ctx, "resp_eligibility", "model", errorBody, "")
					}
					var failed gjson.Result
					for _, line := range strings.Split(frame, "\n") {
						if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "response.failed" {
							failed = gjson.Parse(data)
						}
					}
					require.True(t, failed.Exists(), frame)
					if tc.eligible {
						require.Equal(t, "1", failed.Get("response.error.headers.retry-after").String())
						require.Contains(t, failed.Get("response.error.message").String(), "Please try again in 1s.")
					} else {
						require.False(t, failed.Get("response.error.headers").Exists(), frame)
						require.NotContains(t, failed.Get("response.error.message").String(), "try again")
						require.True(t, resilience.RetryAdviceFrom(ctx).Suppress)
						require.Equal(t, "non_retryable", resilience.RetryAdviceFrom(ctx).Source)
						require.Zero(t, providerCalls.Load(), "terminal rejection must precede candidate lookup")
					}
				})
			}
		}
	}
}

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
