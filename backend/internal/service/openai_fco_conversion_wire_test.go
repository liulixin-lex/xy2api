//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFCOConversionRetryCapturesFinalWire(t *testing.T) {
	for _, mode := range []string{resilience.FCOCompatModeProactive, resilience.FCOCompatModeReactive} {
		t.Run(mode, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, "chat_input", mode)
			req.ctx = WithOpenAIConversionRetry(req.ctx)
			req.ctx = WithGroupSystemPrompt(req.ctx, GroupSystemPromptConfig{Prompt: "group fixture"}, "gpt-5.1")
			req.c.Request = req.c.Request.WithContext(req.ctx)
			up.reply = func(n int, request *http.Request) (*http.Response, error) {
				state := openAIConversionRetryFrom(request.Context())
				require.NotNil(t, state)
				require.False(t, state.used, "FCO replay must leave the conversion retry available")
				require.Equal(t, up.bodies[n-1], state.wire, "conversion capture must describe the actual dispatch")
				if gjson.GetBytes(state.wire, "input.1.type").String() == "message" {
					require.True(t, state.fcoRewritten)
					require.Equal(t, state.wire, state.responsesInput, "subsequent repairs start from the rewritten wire")
				}
				rec := httptest.NewRecorder()
				up.relay.ServeHTTP(rec, request)
				return fcoRelayResponse(rec), nil
			}
			_, err := callFCOWire("chat_input", svc, req, account, []byte(fcoWireInput), 1)
			require.NoError(t, err)
			state := openAIConversionRetryFrom(req.ctx)
			require.Equal(t, "function_call_output", gjson.GetBytes(state.first, "input.1.type").String(), "candidate validation retains the first builder result")
			require.Equal(t, "message", gjson.GetBytes(state.wire, "input.1.type").String())
		})
	}
}
