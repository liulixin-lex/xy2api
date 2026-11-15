//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM4MetadataUserIDForwarding(t *testing.T) {
	for _, target := range []string{"native", "gateway"} {
		t.Run(target, func(t *testing.T) {
			body := []byte(`{"model":"k3","messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"fixture-user","project":"fixture"}}`)
			c, _ := compatStreamContext("/v1/chat/completions", string(body))
			upstream := &httpUpstreamRecorder{resp: nativeAnthropicStreamResponse()}
			if target == "native" {
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				_, err := svc.forwardChatCompletionsViaNativeAnthropic(context.Background(), c, nativeAnthropicTestAccount(), body, "")
				require.NoError(t, err)
			} else {
				svc := &GatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}}
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
				require.NoError(t, err)
			}
			require.Equal(t, "fixture-user", gjson.GetBytes(upstream.lastBody, "metadata.user_id").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "metadata.project").Exists())
		})
	}
}

func TestM4ChatFallbackCompletionGate(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, arguments := range []string{`{"x":1}`, "", `{"x":`} {
			for _, reason := range []string{"length", "tool_calls"} {
				t.Run(fmt.Sprintf("%t/%s/%s", stream, reason, arguments), func(t *testing.T) {
					c, rec := compatStreamContext("/v1/responses", "")
					account := rawChatCompletionsTestAccount()
					opts := m7BridgeConversionOptions(c.Request.Context(), account)
					call := map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "f", "arguments": arguments}}
					wire := map[string]any{"id": "chat_1", "choices": []any{map[string]any{"index": 0, "finish_reason": reason, "message": map[string]any{"role": "assistant", "tool_calls": []any{call}}}}, "usage": map[string]int{"prompt_tokens": 11, "completion_tokens": 6, "total_tokens": 17}}
					contentType := "application/json"
					raw, _ := json.Marshal(wire)
					if stream {
						contentType = "text/event-stream"
						rawCall, _ := json.Marshal(call)
						raw = []byte("data: " + `{"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[` + string(rawCall) + `]},"finish_reason":"` + reason + `"}],"usage":{"prompt_tokens":11,"completion_tokens":6,"total_tokens":17}}` + "\n\ndata: [DONE]\n\n")
					}
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(string(raw)))}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
					var err error
					if stream {
						_, err = svc.streamChatCompletionsAsResponses(c, resp, account, "fixture", nil, nil, false, nil, "fixture", "fixture", nil, nil, time.Now(), opts)
					} else {
						_, err = svc.bufferChatCompletionsAsResponses(c, resp, account, "fixture", nil, nil, false, nil, "fixture", "fixture", nil, nil, time.Now(), opts)
					}
					if reason == "tool_calls" && arguments == `{"x":` {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					if reason == "length" {
						require.Contains(t, rec.Body.String(), `"status":"incomplete"`)
						require.NotContains(t, rec.Body.String(), "function_call_arguments.done")
						if stream {
							for _, line := range strings.Split(rec.Body.String(), "\n") {
								if strings.HasPrefix(line, "data: ") && gjson.Get(strings.TrimPrefix(line, "data: "), "type").String() == "response.incomplete" {
									for _, item := range gjson.Get(strings.TrimPrefix(line, "data: "), "response.output").Array() {
										require.Equal(t, "incomplete", item.Get("status").String())
									}
								}
							}
						} else {
							for _, item := range gjson.Get(rec.Body.String(), "output").Array() {
								require.Equal(t, "incomplete", item.Get("status").String())
							}
						}
					} else {
						require.Contains(t, rec.Body.String(), `"type":"function_call"`)
					}
				})
			}
		}
	}
}

func TestM7ChatArrayStreamReachesBridge(t *testing.T) {
	for _, target := range []string{"responses", "messages"} {
		t.Run(target, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/"+target, "")
			account := rawChatCompletionsTestAccount()
			opts := m7BridgeConversionOptions(c.Request.Context(), account)
			wire := "data: " + `{"id":"chat_1","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":"think"},{"type":"text","text":"answer"}]},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			var err error
			if target == "responses" {
				_, err = svc.streamChatCompletionsAsResponses(c, resp, account, "fixture", nil, nil, false, nil, "fixture", "fixture", nil, nil, time.Now(), opts)
			} else {
				_, err = svc.streamChatCompletionsAsAnthropic(c, resp, account, "fixture", "fixture", "fixture", nil, nil, time.Now(), opts)
			}
			require.NoError(t, err)
			require.Contains(t, rec.Body.String(), "think")
			require.Contains(t, rec.Body.String(), "answer")
		})
	}
}

func TestM7BridgeOptionsExcludeLegacyTargets(t *testing.T) {
	for _, account := range []*Account{{Type: AccountTypeOAuth, Platform: PlatformOpenAI}, {Type: AccountTypeAPIKey, Platform: PlatformGrok}} {
		require.Nil(t, m7BridgeConversionOptions(context.Background(), account).Bridge)
	}
	body := []byte(`{"model":"fixture","input":"hi","tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"f","parameters":{"type":"object"}}]}]}`)
	legacy, _, err := adaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)
	explicit, _, err := adaptResponsesClientToolsForAnthropic(body, apicompat.ConversionOptions{})
	require.NoError(t, err)
	require.Equal(t, string(legacy), string(explicit))
}

func TestM7BridgeToolIdentityRoundTrip(t *testing.T) {
	name := strings.Repeat("long_tool_", 10)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"k3","input":"hi","stream":%t,"tools":[{"type":"function","name":%q,"parameters":{"type":"object"}}]}`, stream, name))
			c, rec := compatStreamContext("/v1/responses", string(body))
			opts := m7BridgeConversionOptions(c.Request.Context(), nativeAnthropicTestAccount())
			adapted, _, err := adaptResponsesClientToolsForAnthropic(body, opts)
			require.NoError(t, err)
			alias := gjson.GetBytes(adapted, "tools.0.name").String()
			require.NotEqual(t, name, alias)
			wire := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","model":"k3","role":"assistant","content":[],"usage":{"input_tokens":1}}}` + "\n\nevent: content_block_start\ndata: " + fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":%q,"input":{"x":1}}}`, alias) + "\n\nevent: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}` + "\n\nevent: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			_, err = svc.forwardResponsesViaNativeAnthropic(c.Request.Context(), c, nativeAnthropicTestAccount(), body, "")
			require.NoError(t, err)
			require.Equal(t, alias, gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
			require.Contains(t, rec.Body.String(), `"name":"`+name+`"`)
			require.NotContains(t, rec.Body.String(), `"name":"`+alias+`"`)
		})
	}
}

func TestM7AnthropicBridgeKeepsIntermediateStop(t *testing.T) {
	req, err := apicompat.ChatCompletionsToResponsesWithOptions(&apicompat.ChatCompletionsRequest{Model: "k3", Messages: []apicompat.ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}, Stop: json.RawMessage(`["END"]`)}, apicompat.ConversionOptions{})
	require.NoError(t, err)
	opts := m7BridgeConversionOptions(context.Background(), nativeAnthropicTestAccount())
	out, err := m7ResponsesToAnthropicRequest(req, opts)
	require.NoError(t, err)
	require.Equal(t, []string{"END"}, out.StopSeqs)
}
