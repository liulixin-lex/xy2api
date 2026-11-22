//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func gpt62Forward(t *testing.T, ctx context.Context, s *OpenAIGatewayService, a *Account, path string, body []byte) error {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	switch path {
	case "/v1/chat/completions":
		_, err := s.ForwardAsChatCompletions(ctx, c, a, body, "", "")
		return err
	case "/v1/messages":
		_, err := s.ForwardAsAnthropic(ctx, c, a, body, "", "")
		return err
	default:
		_, err := s.Forward(ctx, c, a, body)
		return err
	}
}

func TestGPT62ReasoningWire(t *testing.T) {
	for _, route := range []string{"api", "oauth", "raw-chat", "passthrough"} {
		for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
			if route == "passthrough" && path != "/v1/responses" {
				continue
			}
			for _, selection := range []string{"none", "minimal", "max", "suffix-none", "suffix-minimal", "suffix-max", "disabled"} {
				if selection == "disabled" && path != "/v1/messages" {
					continue
				}
				t.Run(route+path+"/"+selection, func(t *testing.T) {
					model := "gpt-6.2-astra"
					effort := strings.TrimPrefix(selection, "suffix-")
					payload := map[string]any{"temperature": 0.3, "top_p": 0.7, "model": model}
					if path != "/v1/messages" {
						payload["logprobs"] = true
						payload["top_logprobs"] = 2
					}
					if path == "/v1/responses" {
						payload["input"] = "hello"
						payload["include"] = []string{"reasoning.encrypted_content", "message.output_text.logprobs"}
					} else {
						payload["messages"] = []map[string]string{{"role": "user", "content": "hello"}}
					}
					if path == "/v1/messages" {
						payload["max_tokens"] = 64
					}
					switch {
					case selection == "disabled":
						payload["thinking"] = map[string]string{"type": "disabled"}
					case effort != selection:
						payload["model"] = model + "-" + effort
					case path == "/v1/responses":
						payload["reasoning"] = map[string]string{"effort": effort}
					case path == "/v1/messages":
						payload["output_config"] = map[string]string{"effort": effort}
					default:
						payload["reasoning_effort"] = effort
					}
					body, err := json.Marshal(payload)
					require.NoError(t, err)
					a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture", "access_token": "fixture"}, Extra: map[string]any{"codex_fingerprint_mode": "off"}}
					switch route {
					case "oauth":
						a.Type = AccountTypeOAuth
					case "raw-chat":
						a.Extra["openai_responses_supported"] = false
					case "passthrough":
						a.Extra["openai_passthrough"] = true
					}
					u := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_fixture", model)}
					s := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: u}
					err = gpt62Forward(t, context.Background(), s, a, path, body)
					if route == "oauth" && effort != "max" {
						require.ErrorContains(t, err, "does not support reasoning effort")
						require.Nil(t, u.lastReq)
						return
					}
					require.NotNil(t, u.lastReq, "%v", err)
					wire := gjson.ParseBytes(u.lastBody)
					field := "reasoning.effort"
					if route == "raw-chat" {
						field = "reasoning_effort"
					}
					expected := "low"
					if effort == "max" {
						expected = effort
					}
					if selection == "suffix-max" && path != "/v1/messages" {
						expected = "" // An unchanged suffix is metadata on Chat and Responses.
					}
					require.Equal(t, expected, wire.Get(field).String())
					wireModel := model
					if route != "oauth" && path != "/v1/messages" {
						wireModel = payload["model"].(string)
					}
					require.Equal(t, wireModel, wire.Get("model").String())
					if route != "oauth" {
						require.False(t, wire.Get("instructions").Exists())
					}
					keepsSampling := (route == "api" || route == "passthrough") && path == "/v1/responses" || route == "raw-chat" && path == "/v1/chat/completions"
					for _, field := range []string{"temperature", "top_p"} {
						require.Equal(t, keepsSampling, wire.Get(field).Exists(), field)
					}
					if !keepsSampling {
						require.False(t, wire.Get("logprobs").Exists())
						require.False(t, wire.Get("top_logprobs").Exists())
						require.NotContains(t, wire.Get("include").String(), "message.output_text.logprobs")
					}
				})
			}
		}
	}
}

func TestGPT62ModelSpellings(t *testing.T) {
	for _, model := range []string{"openai/GPT_6.2_ASTRA_MAX", "gpt-6.2-astra-openai-compact", "gpt-6.2-Astra"} {
		req := apicompat.AnthropicRequest{Model: model}
		applyOpenAICompatModelNormalization(&req)
		want := model
		if strings.HasSuffix(model, "_MAX") {
			want = "gpt-6.2-astra"
		}
		require.Equal(t, want, req.Model)
		require.Equal(t, "gpt-6.2-astra", NormalizeOpenAICompatRequestedModel(model))
	}
	for _, model := range []string{"gpt-6.2-terra", "gpt-6.2-sol-2026-11-17", "gpt-6.2-astra-pro"} {
		require.Equal(t, model, NormalizeOpenAICompatRequestedModel(model))
		require.Equal(t, model, normalizeCodexModel(model))
		require.False(t, isOpenAIGPT6Model(model))
	}
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6.1-sol": "gpt-6.1-sol", "gpt-6.2-astra": "gpt-6.2-astra"}}}
	require.False(t, a.IsModelSupported("gpt-6.1-Sol"))
	require.False(t, a.IsModelSupported("gpt-6.2-Astra"))
}

func TestGPT62WebSocketReasoning(t *testing.T) {
	for _, invalidFirst := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		upstream := newStagedPassthroughConn()
		s := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
		server, serverErr := startPassthroughLifecycleServer(t, ctx, s, passthroughLifecycleAccount())
		payload := `{"type":"response.create","model":"gpt-6.2-sol-max","reasoning":{"effort":"max"},"temperature":0.7}`
		if invalidFirst {
			payload = `{"type":"response.create","model":"gpt-6.2-sol-none"}`
		}
		conn := dialPassthroughLifecycleClientWithPayload(t, server, payload)
		if !invalidFirst {
			wire := requirePassthroughUpstreamWrite(t, upstream, time.Second)
			require.Equal(t, "gpt-6.2-sol-max", gjson.GetBytes(wire, "model").String())
			require.Equal(t, "max", gjson.GetBytes(wire, "reasoning.effort").String())
			require.Equal(t, 0.7, gjson.GetBytes(wire, "temperature").Float())
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_fixture","usage":{"input_tokens":1,"output_tokens":1}}}`)
			_, err := readPassthroughLifecycleFrame(t, conn, time.Second)
			require.NoError(t, err)
			require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","reasoning":{"effort":"minimal"}}`)))
			_, err = readPassthroughLifecycleFrame(t, conn, 3*time.Second)
			require.Error(t, err)
		}
		select {
		case err := <-serverErr:
			var closeErr *OpenAIWSClientCloseError
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
			require.Contains(t, closeErr.Reason(), "does not support reasoning effort")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		require.Empty(t, upstream.writes)
		_ = conn.CloseNow()
		server.Close()
		cancel()
	}
}

func TestGPT62ToolsSelectionAndWire(t *testing.T) {
	for _, probe := range []string{"true", "unset", "false"} {
		t.Run(probe, func(t *testing.T) {
			withResilienceSettings(t, nil)
			a := chatRequirementAccount(1, PlatformOpenAI, "gpt-6.2-sol", APIProtocolResponses)
			delete(a.Credentials, "api_protocol")
			if probe == "unset" {
				delete(a.Extra, "openai_responses_supported")
			} else {
				a.Extra["openai_responses_supported"] = probe == "true"
			}
			f := newSelectionRuntimeFixture(t, a)
			body := []byte(`{"model":"dispatch","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`)
			ctx := WithChatRequestRequirementFromBody(context.Background(), body, "dispatch")
			selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "", "dispatch", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, true)
			if probe == "false" {
				require.Error(t, err)
				require.Nil(t, selected)
				return
			}
			require.NoError(t, err)
			defer selected.ReleaseFunc()
			u := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_fixture", "gpt-6.2-sol")}
			s := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: u}
			require.NoError(t, gpt62Forward(t, ctx, s, selected.Account, "/v1/chat/completions", body))
			require.Equal(t, "/v1/responses", u.lastReq.URL.Path)
			require.Equal(t, "lookup", gjson.GetBytes(u.lastBody, "tools.0.name").String())
		})
	}
}
