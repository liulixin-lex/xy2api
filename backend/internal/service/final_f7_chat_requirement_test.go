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
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFinalF7ChatSelectionMatchesForwardedEffort(t *testing.T) {
	for _, tc := range []struct {
		name, model, bodyModel, fields string
		mappings                       []ReasoningEffortMapping
		wantAccount                    int64
		wantEffort                     string
	}{
		{name: "thinking_disabled", model: "gpt-6-sol", bodyModel: "public-alias", fields: `"thinking":{"type":"disabled"}`, wantAccount: 2},
		{name: "luna_thinking_disabled", model: "gpt-6-luna", bodyModel: "public-alias", fields: `"thinking":{"type":"disabled"}`, wantAccount: 2},
		{name: "model_suffix_none", model: "gpt-6-sol-none", bodyModel: "gpt-6-sol-none", wantAccount: 2},
		{name: "explicit_none", model: "gpt-6-sol", bodyModel: "public-alias", fields: `"reasoning_effort":"none"`, wantAccount: 1, wantEffort: "none"},
		{name: "reasoning_object_none", model: "gpt-6-sol", bodyModel: "public-alias", fields: `"reasoning":{"effort":"none"}`, wantAccount: 1, wantEffort: "none"},
		{name: "reasoning_string_none", model: "gpt-6-sol", bodyModel: "public-alias", fields: `"reasoning":"none"`, wantAccount: 1, wantEffort: "none"},
		{name: "thinking_mapped_high", model: "gpt-6-sol", bodyModel: "public-alias", fields: `"thinking":{"type":"disabled"}`, mappings: []ReasoningEffortMapping{{From: "none", To: "high"}}, wantAccount: 2, wantEffort: "high"},
		{name: "explicit_high_mapped_none", model: "gpt-6-sol", bodyModel: "public-alias", fields: `"reasoning_effort":"high"`, mappings: []ReasoningEffortMapping{{From: "high", To: "none"}}, wantAccount: 1, wantEffort: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			request := map[string]any{}
			require.NoError(t, json.Unmarshal([]byte("{"+tc.fields+"}"), &request))
			request["model"] = tc.bodyModel
			request["messages"] = []any{map[string]string{"role": "user", "content": "hello"}}
			request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}}
			body, err := json.Marshal(request)
			require.NoError(t, err)

			chatOnly := chatRequirementAccount(1, PlatformOpenAI, tc.model, APIProtocolChatCompletions)
			responses := chatRequirementAccount(2, PlatformOpenAI, tc.model, APIProtocolResponses)
			for _, account := range []*Account{&chatOnly, &responses} {
				account.Credentials["base_url"] = "http://upstream.example"
				account.Credentials["model_mapping"].(map[string]any)[tc.bodyModel] = tc.model
			}
			f := newSelectionRuntimeFixture(t, chatOnly, responses)
			ctx := WithOpenAIReasoningEffortPolicy(m1EffortContext(body), "", tc.mappings, "")
			ctx = WithChatRequestRequirementFromBody(ctx, body, "dispatch")
			selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "", "dispatch", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, true)
			require.NoError(t, err)
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			require.Empty(t, resilience.SnapshotConversionNotices(ctx))
			require.False(t, ctx.Value(finalReasoningRequestKey{}).(*finalReasoningRequest).appliedSet)

			reply := `{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
			contentType := "application/json"
			if selected.Account.ID == 2 {
				reply = `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
				reply = "data: {\"type\":\"response.completed\",\"response\":" + reply + "}\n\n"
				contentType = "text/event-stream"
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(reply))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
			result, err := svc.ForwardAsChatCompletions(ctx, c, selected.Account, body, "", "")
			t.Logf("input=%s account=%d status=%d sends=%d error=%v", body, selected.Account.ID, recorder.Code, len(upstream.requests), err)
			require.NoError(t, err)
			require.Equal(t, tc.wantAccount, selected.Account.ID)
			require.NotNil(t, result)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "ok", gjson.GetBytes(recorder.Body.Bytes(), "choices.0.message.content").String())
			require.Len(t, upstream.requests, 1)
			effortField, endpoint := "reasoning_effort", "/chat/completions"
			if tc.wantAccount == 2 {
				effortField, endpoint = "reasoning.effort", "/responses"
			}
			require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, endpoint))
			require.Equal(t, tc.wantEffort, gjson.GetBytes(upstream.lastBody, effortField).String())
		})
	}
}
