//go:build unit

package handler

import (
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM5ToolChoiceErrors(t *testing.T) {
	withHandlerResilience(t, nil)
	f := newHandlerCompatFixture(t)
	f.group.Platform = service.PlatformGemini
	f.account.Platform = service.PlatformGemini
	f.account.Extra = nil
	f.handler.geminiCompatService = service.NewGeminiMessagesCompatService(nil, nil, nil, nil, nil, nil, nil, nil, &config.Config{})
	recorder := httptest.NewRecorder()
	f.call("messages", `{"model":"gemini-2.5-flash","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"missing"}}`, recorder)
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Equal(t, "tool_choice.name", gjson.Get(recorder.Body.String(), "error.param").String())
	require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Empty(t, f.wire)
	require.Zero(t, f.accounts.penalties)
}

type m5NativeUpstream struct {
	service.HTTPUpstream
	body  []byte
	calls int
}

func (u *m5NativeUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	var err error
	u.body, err = io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`))}, nil
}

func TestM5GeminiOutputLimitReservation(t *testing.T) {
	for _, tt := range []struct {
		name, config string
		limit        int
		invalid      bool
	}{
		{"camel", `,"generationConfig":{"maxOutputTokens":512}`, 512, false},
		{"snake", `,"generation_config":{"max_output_tokens":8192}`, 8192, false},
		{"conflict", `,"generationConfig":{"maxOutputTokens":512},"generation_config":{"max_output_tokens":8192}`, 512, false},
		{"null", `,"generationConfig":{"maxOutputTokens":null},"generation_config":{"max_output_tokens":8192}`, 8192, false},
		{"missing", ``, 0, false},
		{"invalid_selected", `,"generationConfig":{"maxOutputTokens":"512"},"generation_config":{"max_output_tokens":8192}`, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]` + tt.config + `}`
			estimate, err := tokenInflightEstimateForProtocol("gemini-2.5-flash", []byte(body), "gemini")
			f := newGeminiClientCancelFixture(t)
			upstream := &m5NativeUpstream{}
			f.handler.geminiCompatService = service.NewGeminiMessagesCompatService(nil, nil, nil, nil, nil, nil, upstream, nil, &config.Config{})
			rec, _ := f.serve(t, "/v1beta/models/*modelAction", "/v1beta/models/gemini-2.5-flash:generateContent", body, f.handler.GeminiV1BetaModels)
			if tt.invalid {
				// E22: an unreadable limit uses the old estimate and the native body still goes out.
				require.Error(t, err)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, 1, upstream.calls)
				require.JSONEq(t, body, string(upstream.body))
				return
			}
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, 1, upstream.calls)
			require.Equal(t, tt.limit, estimate.MaxTokens)
			limit, _, err := apicompat.EffectiveRequestMaxOutputTokens("gemini", upstream.body)
			require.NoError(t, err)
			if tt.limit == 0 {
				require.Nil(t, limit)
			} else {
				require.NotNil(t, limit)
				require.Equal(t, estimate.MaxTokens, *limit)
			}
		})
	}
}
