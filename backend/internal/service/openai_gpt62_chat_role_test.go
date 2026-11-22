//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestGPT62ChatSystemAsDeveloperWire(t *testing.T) {
	const initial = `{"model":"gpt-6.2-sol","prompt_cache_key":"explicit","response_format":{"type":"json_object"},"messages":[{"role":"system","content":"Return JSON."},{"role":"user","content":"hello"},{"role":"system","cache_control":{"type":"ephemeral"},"content":[{"type":"text","text":"inspect","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},{"role":"developer","content":"keep"}]}`
	for _, auth := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, instructions := range []string{"absent", "", "  client value  "} {
			t.Run(fmt.Sprintf("%s/instructions=%q", auth, instructions), func(t *testing.T) {
				body := []byte(initial)
				if instructions != "absent" {
					body, _ = sjson.SetBytes(body, "instructions", instructions)
				}
				original := bytes.Clone(body)
				u := &httpUpstreamRecorder{}
				s := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: u}
				ctx := WithGroupSystemPrompt(context.Background(), GroupSystemPromptConfig{Prompt: "group policy"}, "gpt-6.2-sol")
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
				account := func(id int64, model string) *Account {
					return &Account{ID: id, Type: auth, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "fixture", "access_token": "fixture", "model_mapping": map[string]any{"gpt-6.2-sol": model}}, Extra: map[string]any{"codex_fingerprint_mode": "off"}}
				}
				a, other := account(1, "gpt-6.2-astra"), account(2, "gpt-5.4")
				forward := func(a *Account, enabled bool) []byte {
					t.Helper()
					s.cfg.Gateway.GPT62ChatSystemAsDeveloper = enabled
					u.resp = openAICompatSSECompletedResponse("resp_fixture", "fixture")
					_, err := s.ForwardAsChatCompletions(ctx, c, a, body, "explicit", "")
					require.NoError(t, err)
					require.Equal(t, original, body)
					return bytes.Clone(u.lastBody)
				}
				baseline := forward(a, false)
				otherBaseline := forward(other, false)
				on := forward(a, true)
				require.Equal(t, on, forward(a, true), "same account retry")
				require.Equal(t, otherBaseline, forward(other, true), "account change")
				require.Equal(t, baseline, forward(a, false))
				input := gjson.GetBytes(on, "input")
				require.Len(t, input.Array(), 5)
				for i, role := range []string{"developer", "developer", "user", "developer", "developer"} {
					require.Equal(t, role, input.Get(fmt.Sprintf("%d.role", i)).String())
				}
				require.Contains(t, input.Get("0.content").String(), "group policy")
				require.Contains(t, input.Get("1.content").String(), "Return JSON.")
				require.Equal(t, "json_object", gjson.GetBytes(on, "text.format.type").String())
				// The baseline JSON-mode OAuth transform also retains system content in input.
				require.Equal(t, gjson.GetBytes(baseline, "input.2.content").Raw, input.Get("3.content").Raw)
				require.Equal(t, "input_image", input.Get("3.content.1.type").String())
				require.Equal(t, "ephemeral", input.Get("3.cache_control.type").String())
				require.Equal(t, "ephemeral", input.Get("3.content.0.cache_control.type").String())
				value := gjson.GetBytes(on, "instructions")
				if instructions == "absent" || instructions == "" {
					require.Equal(t, auth == AccountTypeOAuth, value.Exists(), "Codex requires an empty instructions field")
					require.Empty(t, value.String())
				} else {
					require.Equal(t, instructions, value.String())
				}
			})
		}
	}
}

func TestGPT62ChatRoleProtocolScope(t *testing.T) {
	for _, tc := range []struct {
		name, path, platform, body string
		wantRole                   string
	}{
		{"mapped alias", "/v1/chat/completions", PlatformOpenAI, `{"model":"alias","messages":[{"role":"system","content":"policy"}]}`, "developer"},
		{"native Responses", "/v1/responses", PlatformOpenAI, `{"model":"alias","input":[{"role":"system","content":"policy"}]}`, "system"},
		{"Responses shape on Chat", "/v1/chat/completions", PlatformOpenAI, `{"model":"alias","input":[{"role":"system","content":"policy"}]}`, "system"},
		{"raw Chat", "/v1/chat/completions", PlatformOpenAI, `{"model":"alias","messages":[{"role":"system","content":"policy"}]}`, "system"},
		{"another platform", "/v1/chat/completions", PlatformKimi, `{"model":"alias","messages":[{"role":"system","content":"policy"}]}`, "system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture", "api_protocol": APIProtocolResponses, "model_mapping": map[string]any{"alias": "gpt-6.2-sol"}}, Extra: map[string]any{"openai_responses_supported": tc.name != "raw Chat"}}
			u := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_fixture", "gpt-6.2-sol")}
			s := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: u}
			s.cfg.Gateway.GPT62ChatSystemAsDeveloper = true
			err := gpt62Forward(t, context.Background(), s, a, tc.path, []byte(tc.body))
			require.NotNil(t, u.lastReq, "%v", err)
			field := "input.0.role"
			if tc.name == "raw Chat" {
				field = "messages.0.role"
			}
			require.Equal(t, tc.wantRole, gjson.GetBytes(u.lastBody, field).String())
		})
	}
}
