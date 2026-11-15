//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM7FixedChatConvertsResponsesShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{PlatformOpenAI, PlatformGrok} {
		for _, tc := range []struct{ name, fields, param string }{
			{"string", `"input":"hello"`, ""},
			{"array", `"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]`, ""},
			{"image", `"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/image.png"}]}]`, ""},
			{"history", `"input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]`, ""},
			{"messages win", `"messages":[{"role":"user","content":"original"}],"input":"ignored"`, ""},
			{"unsupported", `"input":[{"role":"user","content":[{"type":"unsupported_media"}]}]`, "content.type"},
		} {
			t.Run(platform+"/"+tc.name, func(t *testing.T) {
				body := []byte(`{"model":"grok-4.5","stream":false,` + tc.fields + `}`)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))}}
				account := rawChatCompletionsTestAccount()
				account.Platform = platform
				account.Extra = map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions)}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				if tc.param != "" {
					require.Error(t, err)
					require.Empty(t, upstream.requests)
					require.Equal(t, 400, rec.Code)
					require.Equal(t, tc.param, gjson.Get(rec.Body.String(), "error.param").String())
					return
				}
				require.NoError(t, err)
				require.Len(t, upstream.requests, 1)
				require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, "/chat/completions"))
				require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray())
				if tc.name != "messages win" {
					require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
				}
			})
		}
	}
}
