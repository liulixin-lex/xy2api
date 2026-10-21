//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicGatewayFailuresUseAPIError(t *testing.T) {
	for _, viaOpenAI := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name   string
				status int
				body   string
				want   string
			}{
				{"transient", 503, `{"error":{"code":"server_error","message":"unavailable"}}`, "api_error"},
				{"numeric_overload", 529, `{}`, "api_error"},
				{"upstream_overload", 529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, "overloaded_error"},
				{"bad_gateway", 502, `{"error":{"message":"unavailable"}}`, "api_error"},
			} {
				t.Run(tc.name+boolName(viaOpenAI)+boolName(stream), func(t *testing.T) {
					withHandlerResilience(t, nil)
					c, rec := newResilienceTestContext(t)
					failure := &service.UpstreamFailoverError{StatusCode: tc.status, ResponseBody: []byte(tc.body)}
					if viaOpenAI {
						(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, failure, stream)
					} else {
						(&GatewayHandler{}).handleFailoverExhausted(c, failure, service.PlatformAnthropic, stream)
					}
					body := rec.Body.String()
					if stream {
						events := parseSSEEvents(body)
						require.Len(t, events, 1, body)
						body = events[0].data
					}
					require.Equal(t, tc.want, gjson.Get(body, "error.type").String(), body)
				})
			}
		}
	}
}

func TestAnthropicGatewayTypeSwitchOffRestoresLegacy(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.Enabled = false })
	c, rec := newResilienceTestContext(t)
	(&GatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{StatusCode: 529}, service.PlatformAnthropic, false)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "overloaded_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
}
