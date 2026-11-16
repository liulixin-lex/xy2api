//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM9RawChatUsesFinalResponsesRequirement(t *testing.T) {
	for _, tc := range []struct {
		name, model, effort, field string
		mappings                   []ReasoningEffortMapping
		reject                     bool
	}{
		{name: "sol61_tools", model: "gpt-6.1-sol", effort: "none", field: "tools", reject: true},
		{name: "terra_functions", model: "gpt-6.1-terra", effort: "none", field: "functions", reject: true},
		{name: "sol_default", model: "gpt-6-sol", field: "tools", reject: true},
		{name: "luna_default", model: "gpt-6-luna", field: "functions", reject: true},
		{name: "sol_none", model: "gpt-6-sol", effort: "none", field: "tools"},
		{name: "luna_none", model: "gpt-6-luna", effort: "none", field: "functions"},
		{name: "mapped_none", model: "gpt-6-sol", effort: "high", field: "tools", mappings: []ReasoningEffortMapping{{From: "high", To: "none"}}},
		{name: "mapped_high", model: "gpt-6-sol", effort: "none", field: "tools", mappings: []ReasoningEffortMapping{{From: "none", To: "high"}}, reject: true},
		{name: "plain_text", model: "gpt-6.1-sol"},
		{name: "ordinary_tools", model: "custom-model", field: "tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{"model": "public", "messages": []any{map[string]string{"role": "user", "content": "hello"}}}
			if tc.effort != "" {
				request["reasoning_effort"] = tc.effort
			}
			function := map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}
			if tc.field == "tools" {
				request[tc.field] = []any{map[string]any{"type": "function", "function": function}}
			} else if tc.field == "functions" {
				request[tc.field] = []any{function}
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			ctx := WithOpenAIReasoningEffortPolicy(m1EffortContext(body), "", tc.mappings, "")
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			account := rawChatCompletionsTestAccount()
			account.Credentials["model_mapping"] = map[string]any{"public": tc.model}
			result, err := svc.forwardAsRawChatCompletions(ctx, c, account, body, "")
			if tc.reject {
				require.ErrorContains(t, err, "requires Responses")
				require.Nil(t, result)
				require.Empty(t, upstream.requests)
				require.Equal(t, 400, recorder.Code)
				require.Equal(t, "invalid_request_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, tc.model, gjson.GetBytes(upstream.lastBody, "model").String())
			if tc.field != "" {
				require.Len(t, gjson.GetBytes(upstream.lastBody, tc.field).Array(), 1)
			}
		})
	}
}
