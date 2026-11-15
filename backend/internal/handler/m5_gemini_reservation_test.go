//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
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
