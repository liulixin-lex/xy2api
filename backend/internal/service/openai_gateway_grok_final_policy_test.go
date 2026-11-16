//go:build unit

package service

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFinalF4GrokResponsesGroupPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, model, effort, maxEffort, overLimit, want string
		mappings                                        []ReasoningEffortMapping
		deny                                            bool
	}{
		{name: "mapping", model: "grok-4.7", effort: "low", mappings: []ReasoningEffortMapping{{From: "low", To: "medium"}}, want: "medium"},
		{name: "cap", model: "grok-4.7", effort: "high", maxEffort: "medium", want: "medium"},
		{name: "mapping_deny", model: "grok-4.7", effort: "low", mappings: []ReasoningEffortMapping{{From: "low", To: "deny"}}, deny: true},
		{name: "cap_deny", model: "grok-4.7", effort: "high", maxEffort: "medium", overLimit: ReasoningEffortOverLimitDeny, deny: true},
		{name: "final_model", model: "alias", effort: "low", mappings: []ReasoningEffortMapping{{From: "low", To: "medium", Model: "grok-4.7", MatchType: "exact"}, {From: "medium", To: "high"}}, want: "medium"},
		{name: "final_wire", model: "grok-composer", effort: "low", mappings: []ReasoningEffortMapping{{From: "low", To: "high"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"stream":true,"reasoning":{"effort":%q},"input":"hi"}`, tc.model, tc.effort))
			up := finalF4GrokUpstream()
			svc := newOpenAIImageGenerationControlTestService(up)
			c, rec := newOpenAIImageGenerationControlTestContext(true, "curl/8.0")
			ctx := WithOpenAIReasoningEffortPolicy(m1EffortContext(body), tc.maxEffort, tc.mappings, tc.overLimit)
			c.Request = c.Request.WithContext(ctx)
			account := newOpenAIImageGenerationControlTestAccount()
			account.Platform = PlatformGrok
			account.Credentials["model_mapping"] = map[string]any{"alias": "grok-4.7"}
			result, err := svc.Forward(ctx, c, account, body)
			t.Logf("status=%d sends=%d wire_effort=%q error=%v", rec.Code, len(up.bodies), gjson.GetBytes(up.lastBody, "reasoning.effort").String(), err)
			if tc.deny {
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, http.StatusForbidden, rec.Code)
				require.Empty(t, up.bodies)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.want, gjson.GetBytes(up.lastBody, "reasoning.effort").String())
			if tc.want == "" {
				require.Nil(t, result.ReasoningEffort)
			} else {
				require.NotNil(t, result.ReasoningEffort)
				require.Equal(t, tc.want, *result.ReasoningEffort)
			}
		})
	}
}

func TestFinalF4GrokResponsesWithoutGroupPolicy(t *testing.T) {
	for _, effortField := range []string{`"reasoning":{"effort":"low"}`, `"reasoning_effort":"max"`, `"reasoning":{"summary":"auto"}`} {
		t.Run(effortField, func(t *testing.T) {
			body := []byte(`{"model":"grok-4.7","stream":true,"input":"hi","prompt_cache_key":"fixture",` + effortField + `}`)
			up := finalF4GrokUpstream()
			svc := newOpenAIImageGenerationControlTestService(up)
			c, _ := newOpenAIImageGenerationControlTestContext(true, "curl/8.0")
			ctx := m1EffortContext(body)
			c.Request = c.Request.WithContext(ctx)
			account := newOpenAIImageGenerationControlTestAccount()
			account.Platform = PlatformGrok
			result, err := svc.Forward(ctx, c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			t.Logf("no_policy_wire=%s", up.lastBody)
			wantField := strings.ReplaceAll(effortField, `"max"`, `"high"`)
			require.Equal(t, `{"model":"grok-4.7","stream":true,"input":"hi","prompt_cache_key":"cdb3f8cc-10a9-49dc-a4e3-07ceb1b4369c",`+wantField+`}`, string(up.lastBody))
		})
	}
}

func finalF4GrokUpstream() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_f4\",\"object\":\"response\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")),
	}}
}
