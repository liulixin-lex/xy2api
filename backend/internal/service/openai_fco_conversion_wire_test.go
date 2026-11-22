//go:build unit

package service

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFCOConversionRetryCapturesFinalWire(t *testing.T) {
	for _, mode := range []string{resilience.FCOCompatModeProactive, resilience.FCOCompatModeReactive} {
		t.Run(mode, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, "chat_input", mode)
			resilience.StateFrom(req.ctx).SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "gpt-5.1"})
			svc.SetSchedAttemptRecorder(&captureServiceRecorder{})
			req.ctx = WithOpenAIConversionRetry(req.ctx)
			req.ctx = WithGroupSystemPrompt(req.ctx, GroupSystemPromptConfig{Prompt: "group fixture"}, "gpt-5.1")
			req.c.Request = req.c.Request.WithContext(req.ctx)
			up.reply = func(n int, request *http.Request) (*http.Response, error) {
				state := openAIConversionRetryFrom(request.Context())
				require.NotNil(t, state)
				capture := currentSchedAttempt(request.Context())
				require.NotNil(t, capture)
				require.Equal(t, sha256.Sum256(up.bodies[n-1]), capture.bodyHash, "scheduler capture must hash the transport body")
				require.False(t, state.used, "FCO replay must leave the conversion retry available")
				require.Equal(t, up.bodies[n-1], state.wire, "conversion capture must describe the actual dispatch")
				if gjson.GetBytes(state.wire, "input.1.type").String() == "message" {
					require.True(t, state.fcoRewritten)
					require.Equal(t, up.bodies[n-1], openAIRequestRepairFrom(req.c).body, "request repair must start from the dispatched mapped bytes")
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

// Exercise dispatch directly: admission approved the candidate, but its
// effective policy changed before the saved body could be installed.
func TestFCOConversionReplayRechecksAccountBeforeSend(t *testing.T) {
	for _, kind := range []string{"off", "official"} {
		t.Run(kind, func(t *testing.T) {
			svc, account, req, _ := newFCOWireFixture(t, "chat_input", resilience.FCOCompatModeProactive)
			req.ctx = WithOpenAIConversionRetry(req.ctx)
			req.c.Request = req.c.Request.WithContext(req.ctx)
			state := openAIConversionRetryFrom(req.ctx)
			original := []byte(fcoWireInput)
			r, err := http.NewRequest(http.MethodPost, "https://relay.example/v1/responses", strings.NewReader(fcoWireInput))
			require.NoError(t, err)
			state.first = original
			state.wire, _, err = normalizeOpenAIFCOBody(original, DefaultOpenAIFCORules(nil))
			require.NoError(t, err)
			require.NotEqual(t, original, state.wire)
			state.used, state.fcoRewritten = true, true
			state.startReplay()
			state.failure = &UpstreamFailoverError{StatusCode: 200, conversionRetry: state, cause: errors.New("conversion conflict")}
			if kind == "off" {
				account.Extra[OpenAIFCOCompatExtraKey] = resilience.FCOCompatModeOff
			} else {
				account.Credentials["base_url"] = "https://api.openai.com"
			}
			_, err = svc.prepareOpenAIConversionWire(req.ctx, req.c, r, account, APIProtocolResponses, conversionSendBody{wire: original})
			require.ErrorIs(t, err, state.failure.cause)
			require.True(t, state.pendingReplay)
			require.Equal(t, http.StatusBadGateway, req.c.Writer.Status())
		})
	}
}
