//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGPT62CacheWire(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		for _, auth := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
			if auth == AccountTypeOAuth && path == "/v1/responses" {
				continue // native Responses fill is API-key HTTP only
			}
			t.Run(auth+path, func(t *testing.T) {
				upstream := &httpUpstreamRecorder{}
				s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "fixture"}}, httpUpstream: upstream}
				forward := func(keyID int64, model string, count int, explicit string) (string, string) {
					t.Helper()
					a := &Account{ID: 1, Type: auth, Platform: PlatformOpenAI, Credentials: map[string]any{
						"api_key": "fixture", "access_token": "fixture", "model_mapping": map[string]any{"model-alias": model},
					}, Extra: map[string]any{"codex_fingerprint_mode": "off"}}
					var messages []map[string]string
					for i := range count {
						messages = append(messages, map[string]string{"role": "user", "content": fmt.Sprintf("turn %d", i)})
					}
					payload := map[string]any{"model": "model-alias", "stream": false}
					switch path {
					case "/v1/responses":
						payload["input"] = messages
					case "/v1/messages":
						payload["max_tokens"] = 32
						fallthrough
					default:
						payload["messages"] = messages
					}
					if explicit != "" {
						payload["prompt_cache_key"] = explicit
					}
					body, err := json.Marshal(payload)
					require.NoError(t, err)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, path, nil)
					c.Set("api_key", &APIKey{ID: keyID})
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					s.GenerateSessionHash(c, body)
					clientKey := s.ExtractSessionID(c, body)
					upstream.resp = openAICompatSSECompletedResponse("resp_fixture", model)
					upstream.resp.Header.Set("x-codex-turn-state", "fixture-state")
					ctx := c.Request.Context()
					switch path {
					case "/v1/messages":
						_, err = s.ForwardAsAnthropic(ctx, c, a, body, clientKey, "")
					case "/v1/chat/completions":
						_, err = s.ForwardAsChatCompletions(ctx, c, a, body, clientKey, "")
					default:
						_, err = s.Forward(ctx, c, a, body)
					}
					require.NoError(t, err)
					wire := upstream.lastBody
					require.Equal(t, model, gjson.GetBytes(wire, "model").String())
					key := gjson.GetBytes(wire, "prompt_cache_key").String()
					session := upstream.lastReq.Header.Get("session_id")
					if path == "/v1/messages" {
						require.False(t, gjson.GetBytes(wire, "previous_response_id").Exists())
						require.NotContains(t, string(wire), openAICompatClaudeCodeTodoGuardMarker)
						require.Len(t, gjson.GetBytes(wire, "input").Array(), count)
						for i := range count {
							require.Equal(t, fmt.Sprintf("turn %d", i), gjson.GetBytes(wire, fmt.Sprintf("input.%d.content.0.text", i)).String())
						}
						if auth == AccountTypeOAuth {
							require.Empty(t, key)
							if explicit != "" {
								require.Equal(t, openAICompatSessionUUID(c, a, explicit, false), session)
							}
							return session, upstream.lastReq.Header.Get("x-codex-turn-state")
						}
					}
					if explicit != "" {
						require.Equal(t, explicit, key)
					} else if path != "/v1/responses" {
						require.NotEmpty(t, session)
					}
					return key, ""
				}
				first, _ := forward(1, "gpt-6.2-sol", 1, "")
				require.NotEmpty(t, first)
				next, turnState := forward(1, "gpt-6.2-sol", 30, "")
				require.Equal(t, first, next)
				if path == "/v1/messages" && auth == AccountTypeOAuth {
					require.Equal(t, "fixture-state", turnState)
				}
				otherModel, _ := forward(1, "gpt-6.2-astra", 1, "")
				require.NotEqual(t, first, otherModel)
				otherTenant, _ := forward(2, "gpt-6.2-sol", 1, "")
				require.NotEqual(t, first, otherTenant)
				forward(1, "gpt-6.2-sol", 1, "client-key")
			})
		}
	}
}

func TestGPT62ReplayPolicy(t *testing.T) {
	for _, model := range []string{"gpt-5.4", "gpt-5.3-codex", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-luna", "gpt-6-astra"} {
		require.True(t, shouldApplyCompatMessagesReplayPolicy(model), model)
	}
	for _, model := range []string{"gpt-6.2-sol", "openai/gpt-6.2-astra-high", "gpt-6.2-terra", "gpt-4o"} {
		require.False(t, shouldApplyCompatMessagesReplayPolicy(model), model)
	}
}

func TestGPT62MessagesCacheMarkers(t *testing.T) {
	key := func(turn int, metadata bool, system, user, schema string) string {
		t.Helper()
		req := apicompat.AnthropicRequest{}
		req.System = json.RawMessage(fmt.Sprintf(`[{"type":"text","text":"x-anthropic-billing-header: cch=%d"},{"type":"text","text":%q}]`, turn, system))
		tool := apicompat.AnthropicTool{Name: "lookup", InputSchema: json.RawMessage(fmt.Sprintf(`{"properties":{"cache_control":{"type":%q}}}`, schema))}
		if turn == 1 {
			tool.CacheControl = &apicompat.AnthropicCacheControl{Type: "ephemeral"}
		}
		req.Tools = []apicompat.AnthropicTool{tool}
		for i := 1; i <= turn; i++ {
			text := user
			if i > 1 {
				text = "follow-up"
			}
			content := fmt.Sprintf("%q", text)
			if i == turn {
				content = fmt.Sprintf(`[{"type":"text","text":%q,"cache_control":{"type":"ephemeral"}}]`, text)
			}
			req.Messages = append(req.Messages, apicompat.AnthropicMessage{Role: "user", Content: json.RawMessage(content)})
		}
		if metadata {
			req.Metadata = json.RawMessage(`{"user_id":"{\"device_id\":\"mock\",\"session_id\":\"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\"}"}`)
		}
		return gpt62MessagesCacheKey(req, "gpt-6.2-sol", 1)
	}
	for _, metadata := range []bool{false, true} {
		first := key(1, metadata, "policy", "hello", "string")
		for turn := 2; turn <= 3; turn++ {
			require.Equal(t, first, key(turn, metadata, "policy", "hello", "string"))
		}
	}
	first := key(1, false, "policy", "hello", "string")
	require.NotEqual(t, first, key(1, false, "policy", "changed", "string"))
	require.NotEqual(t, first, key(1, false, "changed", "hello", "string"))
	require.NotEqual(t, first, key(1, false, "policy", "hello", "boolean"))
	require.NotEqual(t, first, key(1, true, "policy", "hello", "string"))
}
