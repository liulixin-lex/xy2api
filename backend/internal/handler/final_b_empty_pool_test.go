//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestFinalBEmptyPoolReturns503AcrossEntries(t *testing.T) {
	for _, tc := range []struct{ name, route, platform string }{
		{"chat", "/v1/chat/completions", service.PlatformOpenAI},
		{"responses", "/v1/responses", service.PlatformOpenAI},
		{"embeddings", "/v1/embeddings", service.PlatformOpenAI},
		{"images", "/v1/images/generations", service.PlatformOpenAI},
		{"gemini_count_tokens", "/v1/messages/count_tokens", service.PlatformGemini},
		{"anthropic_count_tokens", "/v1/messages/count_tokens", service.PlatformAnthropic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHandlerResilience(t, nil)
			c, response := runModelRejectHandler(t, tc.route, tc.platform, &modelRejectAccountRepo{}, nil)
			t.Logf("empty pool route=%s platform=%s status=%d body=%s", tc.route, tc.platform, response.Code, response.Body.String())
			require.Equal(t, http.StatusServiceUnavailable, response.Code)
			require.Equal(t, "30", response.Header().Get("Retry-After"))
			parsed := parseOpsErrorResponse(response.Body.Bytes())
			phase, limited, owner, source := classifyOpsErrorLog(c, parsed.ErrorType, parsed.Message, parsed.Code, response.Code)
			require.Equal(t, "routing", phase)
			require.False(t, limited)
			require.Equal(t, "platform", owner)
			require.Equal(t, "gateway", source)
		})
	}
}
