//go:build unit

package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestFinalB04BufferedBillingEligibility(t *testing.T) {
	for _, entry := range []struct{ name, body string }{
		{"chat", `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"fixture"}],"stream":false}`},
		{"responses", `{"model":"claude-opus-4-8","input":"fixture","stream":false}`},
	} {
		for _, tc := range []struct {
			name, wire           string
			billed, success      bool
			input, output, cache int
		}{
			{"error_without_usage", gwUsageOverloaded, false, false, 0, 0, 0},
			{"content_without_usage", strings.Replace(gwUsageStart, `,"usage":{"input_tokens":100,"cache_read_input_tokens":40}`, "", 1) + gwUsageOverloaded, false, false, 0, 0, 0},
			{"partial_usage_error", gwUsageStart + gwUsageOverloaded, true, false, 100, 0, 40},
			{"cache_only_error", strings.Replace(gwUsageStart, `"input_tokens":100,`, "", 1) + gwUsageOverloaded, true, false, 0, 0, 40},
			{"success", scopeAnthropicSSE, true, true, 1, 1, 0},
		} {
			t.Run(entry.name+"/"+tc.name, func(t *testing.T) {
				withHandlerResilience(t, nil)
				f := newHandlerCompatFixture(t)
				f.response = func(uint64) string { return tc.wire }
				response := httptest.NewRecorder()
				c := f.call(entry.name, entry.body, response)
				id := resilience.StateFrom(c.Request.Context()).GatewayRequestID()
				require.NotEmpty(t, id)
				t.Logf("status=%d bills=%d unbilled=%d", response.Code, len(f.usage.logs), len(f.usage.unbilled))
				if tc.success {
					require.Equal(t, 200, response.Code)
				} else {
					require.Equal(t, 502, response.Code)
				}
				if !tc.billed {
					require.Empty(t, f.usage.logs, "a failure without usage must not create a bill")
					require.Len(t, f.usage.unbilled, 1)
					row := f.usage.unbilled[0]
					require.Equal(t, id, row.RequestID)
					require.Equal(t, resilience.RequestFailed, row.Result)
					require.Equal(t, new(false), row.UsageObserved)
					require.Equal(t, new("missing"), row.UsageSource)
					require.Equal(t, new(true), row.TerminalEventSeen)
					require.Equal(t, new(true), row.TerminalEventValid)
					require.Equal(t, new("sse"), row.ResponseBodyType)
					return
				}
				require.Empty(t, f.usage.unbilled)
				require.Len(t, f.usage.logs, 1)
				row := f.usage.logs[0]
				require.Equal(t, new(id), row.GatewayRequestID)
				require.Equal(t, tc.input, row.InputTokens)
				require.Equal(t, tc.output, row.OutputTokens)
				require.Equal(t, tc.cache, row.CacheReadTokens)
				require.Equal(t, new(true), row.UsageObserved)
				require.Equal(t, new("upstream_sse_progressive"), row.UsageSource)
			})
		}
	}
}
