//go:build unit

package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM8ExplicitRequestErrorExits(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, payload, code, param string
		status, wantStatus         int
	}{
		{"expired", `{"error":{"code":"previous_response_not_found","param":"previous_response_id","message":"state is gone"}}`, "upstream_continuation_expired", "previous_response_id", 400, 410},
		{"unsupported", `{"error":{"code":"unsupported_parameter","param":"previous_response_id","message":"not supported"}}`, "http_continuation_unsupported", "previous_response_id", 400, 400},
		{"encrypted_terminal", `{"error":{"code":"encrypted_content_rejected","param":"input[1].encrypted_content","message":"Remove the identified encrypted item rs_bad"}}`, "encrypted_content_rejected", "input[1].encrypted_content", 502, 400},
	} {
		for _, route := range []string{"native", "chat", "chat_after_account", "anthropic"} {
			for _, committed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/committed=%t", tc.name, route, committed), func(t *testing.T) {
					req := newWebSearchReplayRequest(t)
					req.c.Header("Retry-After", "310")
					if committed {
						req.c.Header("Content-Type", "text/event-stream")
						_, err := req.c.Writer.WriteString(": heartbeat\n\n")
						require.NoError(t, err)
						req.c.Writer.Flush()
					}
					repo := &openAIModelAvailabilityRepo{}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: repo,
						rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
					resp := newOpenAIUpstreamErrorResponse(tc.status, tc.payload)
					account := webSearchReplayAccount(false)
					var err error
					switch route {
					case "chat":
						_, err = svc.handleChatCompletionsErrorResponse(resp, req.c, account)
					case "chat_after_account":
						_, err = svc.handleChatCompletionsErrorResponseAfterAccountHandling(resp, req.c, account)
					case "anthropic":
						_, err = svc.handleAnthropicErrorResponse(resp, req.c, account)
					default:
						_, err = svc.handleErrorResponse(req.ctx, resp, req.c, account, nil)
					}
					require.Error(t, err)
					var failover *UpstreamFailoverError
					require.NotErrorAs(t, err, &failover)
					output := req.rec.Body.String()
					if committed {
						require.Equal(t, 200, req.rec.Code)
						require.Equal(t, 1, strings.Count(output, `"error":`))
						require.False(t, WriteTargetProtocolError(req.c, PreOutputProtocolResponses, 500, "server_error", "", "", "second"))
						for _, line := range strings.Split(output, "\n") {
							if data, ok := strings.CutPrefix(line, "data: "); ok {
								if gjson.Get(data, "response.error").Exists() {
									output = gjson.Get(data, "response").Raw
									break
								}
								if gjson.Get(data, "error").Exists() {
									output = data
									break
								}
							}
						}
					} else {
						require.Equal(t, tc.wantStatus, req.rec.Code)
						require.Empty(t, req.rec.Header().Get("Retry-After"))
						require.Equal(t, "false", req.rec.Header().Get("x-should-retry"))
					}
					if route == "anthropic" {
						require.Equal(t, "error", gjson.Get(output, "type").String())
						require.False(t, gjson.Get(output, "error.code").Exists())
					} else {
						require.Equal(t, tc.code, gjson.Get(output, "error.code").String())
						require.Equal(t, tc.param, gjson.Get(output, "error.param").String())
					}
					require.Equal(t, "invalid_request_error", gjson.Get(output, "error.type").String())
					require.True(t, resilience.RetryAdviceFrom(req.ctx).Suppress)
					require.Empty(t, req.session.failures)
					require.Zero(t, repo.setErrorCalls)
					require.Zero(t, repo.tempCalls)
					require.Empty(t, repo.modelRateLimitCalls)
					require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
				})
			}
		}
	}
	for _, status := range []int{400, 404, 409} {
		t.Run(fmt.Sprintf("ordinary_%d", status), func(t *testing.T) {
			req := newWebSearchReplayRequest(t)
			body := `{"error":{"message":"previous attempt failed","param":"input"}}`
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			_, err := svc.handleErrorResponse(req.ctx, newOpenAIUpstreamErrorResponse(status, body), req.c, webSearchReplayAccount(false), nil)
			require.Error(t, err)
			var continuation *HTTPContinuationError
			require.NotErrorAs(t, err, &continuation)
			require.NotContains(t, req.rec.Body.String(), "upstream_continuation_expired")
			require.NotContains(t, req.rec.Body.String(), "http_continuation_unsupported")
		})
	}
	for _, source := range []resilience.Outcome{
		{Source: resilience.SourceHTTP, StatusCode: 502, Body: []byte(`{"error":{"code":"encrypted_content_rejected"}}`)},
		{Source: resilience.SourceStreamEvent, StatusCode: 200, Body: []byte(`{"type":"response.failed","response":{"error":{"code":"encrypted_content_rejected"}}}`)},
	} {
		result := classifyUpstreamOutcome(source)
		require.Equal(t, resilience.CategoryClientError, result.Category)
		require.Equal(t, resilience.ReasonEncryptedContent, result.Reason)
		require.NotEqual(t, resilience.StateNone, result.StateFamily)
	}
}
