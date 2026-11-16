//go:build unit

package handler

import (
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestM7ReviewErrorSourceMatrix(t *testing.T) {
	for _, protocol := range []string{"chat", "messages", "responses"} {
		for _, source := range []string{"balance", "queue", "exhaustion", "mapping", "cyber"} {
			for _, enabled := range []bool{false, true} {
				for _, streamOn := range []bool{false, true} {
					for _, started := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/errors=%t/stream=%t/started=%t", protocol, source, enabled, streamOn, started), func(t *testing.T) {
							withHandlerResilience(t, func(s *resilience.Settings) {
								s.Errors.Enabled = enabled
								s.Stream.PreludeStaging = streamOn
								s.Stream.StrictTerminal = streamOn
								s.Keepalive.Enabled = streamOn
							})
							path := protocol
							if path == "chat" {
								path = "chat/completions"
							}
							c, rec := newGinContextForEndpoint(t, "/v1/"+path)
							c.Header("Content-Type", "text/event-stream")
							_, err := c.Writer.WriteString(": heartbeat\n\n")
							require.NoError(t, err)
							h := &GatewayHandler{}
							switch source {
							case "balance":
								status, code, message, _ := billingErrorDetails(service.NewInflightBalanceReservedError(0.8, 0.74))
								h.handleStreamingAwareError(c, status, code, message, started)
							case "queue":
								h.handleConcurrencyError(c, &WaitQueueFullError{SlotType: "user"}, "user", started)
							case "mapping":
								require.True(t, handleUnsupportedProtocolConversion(c, &apicompat.UnsupportedConversionError{Field: "response_format", Reason: "unsupported format"}, protocol, started))
							default:
								failure := &service.UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte(`{"error":{"message":"fixture upstream failure"}}`)}
								if source == "cyber" {
									failure.StatusCode = 400
									failure.ResponseBody = []byte(`{"error":{"code":"cyber_policy","message":"blocked by cyber policy"}}`)
								}
								switch protocol {
								case "chat":
									h.handleCCFailoverExhausted(c, failure, started)
								case "responses":
									h.handleResponsesFailoverExhausted(c, failure, started)
								default:
									h.handleFailoverExhausted(c, failure, service.PlatformAnthropic, started)
								}
							}
							before := rec.Body.String()
							require.True(t, service.ProtocolTerminalClaimed(c))
							require.Equal(t, 200, rec.Code)
							h.ensureForwardErrorResponse(c, !started)
							require.Equal(t, before, rec.Body.String())
							switch protocol {
							case "chat":
								require.Equal(t, 1, strings.Count(before, "[DONE]"))
								require.NotContains(t, before, "event: error")
							case "messages":
								require.Equal(t, 1, strings.Count(before, "event: error"))
								require.NotContains(t, before, "[DONE]")
							case "responses":
								require.Equal(t, 1, strings.Count(before, "event: response.created"))
								require.Equal(t, 1, strings.Count(before, "event: response.failed"))
							}
						})
					}
				}
			}
		}
	}
}
