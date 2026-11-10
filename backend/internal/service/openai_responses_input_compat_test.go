package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSanitizeOpenAIResponsesOrphanToolOutputs(t *testing.T) {
	t.Run("named standalone inputs do not legitimize orphan results", func(t *testing.T) {
		named := map[string]any{"type": "function_call_output", "name": "send_message_to_thread", "namespace": "codex_app", "output": "delegation"}
		input := []any{
			named,
			map[string]any{"type": "function_call_output", "output": "missing name and call id"},
			map[string]any{"type": "function_call_output", "name": " ", "output": "blank name"},
			map[string]any{"type": "function_call_output", "name": "send_message_to_thread", "call_id": "missing", "output": "orphan result"},
			map[string]any{"type": "custom_tool_call_output", "name": "apply_patch", "output": "missing call id"},
		}
		reqBody := map[string]any{"input": input}

		require.True(t, sanitizeOpenAIResponsesOrphanToolOutputs(reqBody, input, false))
		require.Equal(t, []any{named}, reqBody["input"])
	})

	t.Run("preserves matches regardless of item order", func(t *testing.T) {
		input := []any{
			map[string]any{"type": "tool_search_output", "call_id": "search_1", "output": "first"},
			map[string]any{"type": "tool_search_call", "id": "search_1", "query": "docs"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "custom_1", "output": "second"},
			map[string]any{"type": "item_reference", "id": "custom_1"},
		}
		reqBody := map[string]any{"input": input}

		require.False(t, sanitizeOpenAIResponsesOrphanToolOutputs(reqBody, input, false))
		require.Equal(t, input, reqBody["input"])
	})

	t.Run("outputs do not legitimize each other", func(t *testing.T) {
		input := []any{
			map[string]any{"type": "function_call_output", "call_id": "missing", "output": "one"},
			map[string]any{"type": "tool_search_output", "call_id": "missing", "output": "two"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "missing", "output": "three"},
			map[string]any{"type": "mcp_tool_call_output", "call_id": "missing", "output": "four"},
		}
		reqBody := map[string]any{"input": input}

		require.True(t, sanitizeOpenAIResponsesOrphanToolOutputs(reqBody, input, false))
		got, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Empty(t, got)
	})

	t.Run("preserves all output variants with matching calls", func(t *testing.T) {
		pairs := []struct {
			callType   string
			outputType string
		}{
			{callType: "function_call", outputType: "function_call_output"},
			{callType: "tool_search_call", outputType: "tool_search_output"},
			{callType: "custom_tool_call", outputType: "custom_tool_call_output"},
			{callType: "mcp_tool_call", outputType: "mcp_tool_call_output"},
		}
		input := make([]any, 0, len(pairs)*2)
		for index, pair := range pairs {
			callID := string(rune('a' + index))
			input = append(input,
				map[string]any{"type": pair.callType, "call_id": callID},
				map[string]any{"type": pair.outputType, "call_id": callID, "output": "ok"},
			)
		}
		reqBody := map[string]any{"input": input}

		require.False(t, sanitizeOpenAIResponsesOrphanToolOutputs(reqBody, input, false))
	})

	t.Run("previous response may contain the missing call", func(t *testing.T) {
		input := []any{map[string]any{"type": "function_call_output", "call_id": "remote", "output": "ok"}}
		reqBody := map[string]any{"input": input, "previous_response_id": "resp_1"}

		require.False(t, sanitizeOpenAIResponsesOrphanToolOutputs(reqBody, input, true))
		require.Equal(t, input, reqBody["input"])
	})
}

func TestWebSocketCompatibilityPreservesNamedDelegation(t *testing.T) {
	for _, toolName := range []string{"create_thread", "send_message_to_thread"} {
		for _, withHistory := range []bool{false, true} {
			for _, previousResponseID := range []string{"", "resp_previous"} {
				name := fmt.Sprintf("%s/history=%t/previous=%t", toolName, withHistory, previousResponseID != "")
				t.Run(name, func(t *testing.T) {
					input := []any{}
					if withHistory {
						input = append(input,
							map[string]any{"type": "function_call", "call_id": "fc_history", "name": "lookup", "arguments": "{}"},
							map[string]any{"type": "function_call_output", "call_id": "fc_history", "output": "historical result"},
						)
					}
					input = append(input,
						map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Updated environment context."}}},
						map[string]any{"type": "function_call_output", "name": toolName, "namespace": "codex_app", "output": "<codex_delegation>\n  <source_thread_id>source-task</source_thread_id>\n  <input>Reply with DELEGATION_OK.</input>\n</codex_delegation>"},
					)
					reqBody := map[string]any{"type": "response.create", "model": "gpt-5.5", "input": input}
					if previousResponseID != "" {
						reqBody["previous_response_id"] = previousResponseID
					}
					body, err := json.Marshal(reqBody)
					require.NoError(t, err)
					account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

					normalized, _, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(body, account, false)
					require.NoError(t, err)
					var got map[string]any
					require.NoError(t, json.Unmarshal(normalized, &got))
					require.Equal(t, input, got["input"], "preserve the delegation envelope, native type and history in order")
					require.Equal(t, reqBody["previous_response_id"], got["previous_response_id"])

					again, changed, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(normalized, account, false)
					require.NoError(t, err)
					require.False(t, changed, "normalization must be idempotent")
					require.JSONEq(t, string(normalized), string(again))
				})
			}
		}
	}
}

func TestOpenAIResponsesInputTextIsNeverSilentlyTruncated(t *testing.T) {
	atLimit := strings.Repeat("z", openAIResponsesInputTextMaxChars)
	oversized := strings.Repeat("a", openAIResponsesInputTextMaxChars) + "中"
	input := []any{
		map[string]any{"type": "function_call_output", "call_id": "limit", "output": atLimit},
		map[string]any{"type": "function_call_output", "call_id": "a", "output": oversized},
		map[string]any{"type": "tool_search_output", "call_id": "b", "output": oversized},
		map[string]any{"type": "custom_tool_call_output", "call_id": "c", "output": oversized},
		map[string]any{"type": "mcp_tool_call_output", "call_id": "d", "output": oversized},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "short"},
				map[string]any{"type": "input_text", "text": oversized},
			},
		},
	}
	reqBody := map[string]any{"input": input}

	require.False(t, truncateOpenAIResponsesInputText(reqBody))
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, atLimit, first["output"])
	for _, rawItem := range input[1:5] {
		item, ok := rawItem.(map[string]any)
		require.True(t, ok)
		require.Equal(t, oversized, item["output"])
	}
	last, ok := input[5].(map[string]any)
	require.True(t, ok)
	content, ok := last["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 2)
	shortPart, ok := content[0].(map[string]any)
	require.True(t, ok)
	oversizedPart, ok := content[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "short", shortPart["text"])
	require.Equal(t, oversized, oversizedPart["text"])
}

func TestOpenAIResponsesInputNeverRequestsPreemptiveTruncation(t *testing.T) {
	short := []byte(`{"input":[{"type":"function_call_output","output":"ok"}]}`)
	largeUnrelated := []byte(`{"input":"` + strings.Repeat("x", openAIResponsesInputTextMaxChars+1) + `"}`)
	largeOutput := []byte(`{"input":[{"type":"function_call_output","output":"` + strings.Repeat("x", openAIResponsesInputTextMaxChars+1) + `"}]}`)

	require.False(t, openAIResponsesInputMayNeedTruncation(short))
	require.False(t, openAIResponsesInputMayNeedTruncation(largeUnrelated))
	require.False(t, openAIResponsesInputMayNeedTruncation(largeOutput))
}

func TestOpenAIGatewayService_OAuthDropsOrphanAfterDroppingPreviousResponse(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"previous_response_id":"resp_missing","input":[{"type":"function_call_output","call_id":"call_missing","output":"keep this result"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"id":"resp_ok","output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		newOpenAIOAuthNamespaceTestAccount(),
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "previous_response_id").Exists())
	require.Empty(t, gjson.GetBytes(upstream.bodies[0], "input").Array())
}

func TestOpenAIGatewayService_PreservesOversizedToolOutputForUpstream(t *testing.T) {
	oversized := strings.Repeat("x", openAIResponsesInputTextMaxChars) + "中"
	body := []byte(`{"model":"gpt-5.5","stream":false,"input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"` + oversized + `"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"id":"resp_ok","output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		newOpenAIRejectedFieldTestAccount(),
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.Equal(t, oversized, gjson.GetBytes(upstream.bodies[0], "input.1.output").String())
}

func TestFCONamedOutputOutbound(t *testing.T) {
	named := map[string]any{"type": "function_call_output", "name": "untrusted_context", "namespace": "agent", "output": "context", "id": "item_fixture"}
	t.Run("T12_namespace_all_modes", func(t *testing.T) {
		for _, mode := range []string{"oauth", "apikey", "apikey_no_declaration", "flatten", "compact", "passthrough"} {
			t.Run(mode, func(t *testing.T) {
				body := []byte(`{"model":"gpt-5.6-terra","stream":false,"instructions":"test","tools":[{"type":"namespace","name":"agent","tools":[{"type":"function","name":"untrusted_context","parameters":{"type":"object"}}]}],"input":[{"type":"function_call_output","name":"untrusted_context","namespace":"agent","output":"context"}]}`)
				account := newOpenAIOAuthNamespaceTestAccount()
				if strings.HasPrefix(mode, "apikey") {
					account = newOpenAIRejectedFieldTestAccount()
				}
				if mode == "apikey_no_declaration" {
					var request map[string]any
					require.NoError(t, json.Unmarshal(body, &request))
					delete(request, "tools")
					var err error
					body, err = json.Marshal(request)
					require.NoError(t, err)
				}
				if mode == "flatten" {
					account.Extra = map[string]any{"openai_responses_flatten_namespaces": true}
				}
				if mode == "passthrough" {
					account.Extra = map[string]any{"openai_oauth_passthrough": true}
				}
				response := newOpenAIRejectedFieldTestResponse(http.StatusOK, namespaceForwardOKResponse)
				if mode == "passthrough" {
					response.Header.Set("Content-Type", "text/event-stream")
					response.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
				}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{response}}
				c := newOpenAIRejectedFieldTestContext(body)
				if mode == "compact" {
					c.Request.URL.Path = "/v1/responses/compact"
				}
				_, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.Len(t, upstream.bodies, 1)
				item := gjson.GetBytes(upstream.bodies[0], `input.#(type=="function_call_output")`)
				require.Equal(t, "agent", item.Get("namespace").String())
				require.Equal(t, "untrusted_context", item.Get("name").String())
				require.False(t, item.Get("call_id").Exists())
			})
		}
	})
	t.Run("T13_rejected_namespace", func(t *testing.T) {
		raw, err := json.Marshal(map[string]any{"input": []any{named}})
		require.NoError(t, err)
		out, _, changed, err := removeOpenAIResponsesRejectedNamespaceAtIndex(raw, 0)
		require.NoError(t, err)
		require.True(t, changed)
		require.False(t, gjson.GetBytes(out, "input.0.namespace").Exists())
		raw = []byte(`{"input":[{"type":"function_call_output","call_id":"paired","namespace":"agent"}]}`)
		_, _, changed, err = removeOpenAIResponsesRejectedNamespaceAtIndex(raw, 0)
		require.NoError(t, err)
		require.False(t, changed)
	})
	t.Run("T14_no_output_id_fallback", func(t *testing.T) {
		for _, typ := range []string{"function_call_output", "custom_tool_call_output", "mcp_tool_call_output", "tool_search_output"} {
			item := map[string]any{"type": typ, "id": "call_fixture", "name": "context", "output": "context"}
			out := filterCodexInputWithOptions([]any{item}, codexInputFilterOptions{})
			require.Len(t, out, 1)
			got, ok := out[0].(map[string]any)
			require.True(t, ok)
			require.NotContains(t, got, "call_id")
		}
		out := filterCodexInputWithOptions([]any{map[string]any{"type": "function_call", "id": "call_fixture", "name": "f", "arguments": "{}"}}, codexInputFilterOptions{})
		got, ok := out[0].(map[string]any)
		require.True(t, ok)
		require.NotEmpty(t, got["call_id"])
	})
	t.Run("T15_no_grok_guess", func(t *testing.T) {
		for _, items := range [][]any{{named, map[string]any{"type": "function_call", "call_id": "call_fixture", "name": "f", "arguments": "{}"}, map[string]any{"type": "function_call_output", "output": "paired"}}, {map[string]any{"type": "message", "role": "user", "content": "hi"}, named}} {
			raw, err := json.Marshal(map[string]any{"input": items})
			require.NoError(t, err)
			out, err := sanitizeGrokResponsesModelInput(raw)
			require.NoError(t, err)
			item := gjson.GetBytes(out, `input.#(name=="untrusted_context")`)
			require.True(t, item.Exists())
			require.Equal(t, "agent", item.Get("namespace").String())
			require.False(t, item.Get("call_id").Exists())
			require.Equal(t, "item_fixture", item.Get("id").String())
			if len(items) == 3 {
				require.Equal(t, "call_fixture", gjson.GetBytes(out, "input.2.call_id").String())
			}
		}
	})
	t.Run("baseline_sanitize_shape", func(t *testing.T) {
		for _, callID := range []any{nil, "", " ", 123, true, []any{}, map[string]any{}} {
			item := map[string]any{"type": "function_call_output", "name": "context", "call_id": callID}
			input := []any{item}
			req := map[string]any{"input": input}
			changed := sanitizeOpenAIResponsesOrphanToolOutputs(req, input, false)
			require.False(t, changed)
			require.Equal(t, input, req["input"])
		}
	})
}

func TestFCONamedOutputStrictIngressKeepsLegacySanitize(t *testing.T) {
	for _, typ := range []string{" function_call_output", "function_call_output "} {
		item := map[string]any{"type": typ, "name": "context"}
		input := []any{item}
		req := map[string]any{"input": input}
		body, err := json.Marshal(req)
		require.NoError(t, err)
		require.True(t, ValidateFunctionCallOutputContextBytes(body).HasFunctionCallOutputMissingCallID)
		require.False(t, sanitizeOpenAIResponsesOrphanToolOutputs(req, input, false))
		require.Equal(t, input, req["input"])
	}
}

func TestFCONamedOutputWSFrame(t *testing.T) {
	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	capture := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_named","model":"gpt-5.6-sol","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`)}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: capture})
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSPool: pool}
	account := newOpenAIOAuthNamespaceTestAccount()
	account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
	body := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":[{"type":"function_call_output","name":"untrusted_context","namespace":"agent","output":"context"},{"type":"function_call_output","call_id":null,"name":"notification","namespace":"agent","output":"second"}]}`)
	c := newOpenAIRejectedFieldTestContext(body)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.OpenAIWSMode)
	capture.mu.Lock()
	frame := requestToJSONString(cloneMapStringAny(capture.lastWrite))
	capture.mu.Unlock()
	require.Equal(t, "response.create", gjson.Get(frame, "type").String())
	require.JSONEq(t, gjson.GetBytes(body, "input").Raw, gjson.Get(frame, "input").Raw)
	t.Logf("WS_FRAME input=%s", gjson.Get(frame, "input").Raw)
}

func TestFCONamedOutputRejectedNamespaceRetry(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":[{"type":"function_call_output","name":"untrusted_context","namespace":"agent","output":"context"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"message":"Unknown parameter: 'input[0].namespace'.","type":"invalid_request_error","param":"input[0].namespace"}}`),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, namespaceForwardOKResponse),
	}}
	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), newOpenAIRejectedFieldTestContext(body), newOpenAIOAuthNamespaceTestAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, "agent", gjson.GetBytes(upstream.bodies[0], "input.0.namespace").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.namespace").Exists())
	require.Equal(t, "untrusted_context", gjson.GetBytes(upstream.bodies[1], "input.0.name").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.call_id").Exists())
}

func TestFCOL63MalformedNamedPreservation(t *testing.T) {
	for _, tc := range []struct{ name, item string }{
		{"empty", `{"type":"function_call_output","call_id":"","name":"notification","namespace":"agent","output":"context"}`},
		{"whitespace", `{"type":"function_call_output","call_id":" \t","name":"notification","namespace":"agent","output":"context"}`},
		{"number", `{"type":"function_call_output","call_id":7,"name":"notification","namespace":"agent","output":"context"}`},
		{"boolean", `{"type":"function_call_output","call_id":true,"name":"notification","namespace":"agent","output":"context"}`},
		{"array", `{"type":"function_call_output","call_id":[],"name":"notification","namespace":"agent","output":"context"}`},
		{"object", `{"type":"function_call_output","call_id":{},"name":"notification","namespace":"agent","output":"context"}`},
		{"padded_type", `{"type":" function_call_output ","name":"notification","namespace":"agent","output":"context"}`},
	} {
		for _, route := range []string{"http_sanitize_with_context", "http_forward_with_context", "ws_with_context", "ws_without_context"} {
			t.Run(tc.name+"/"+route, func(t *testing.T) {
				input := `[{"type":"function_call","call_id":"fc_fixture","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"fc_fixture","output":"paired"},` + tc.item + `]`
				if route == "ws_without_context" {
					input = `[` + tc.item + `]`
				}
				body := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":` + input + `}`)
				var forwarded string
				switch route {
				case "http_sanitize_with_context":
					var request map[string]any
					require.NoError(t, decodeOpenAIJSONUseNumber(body, &request))
					items, ok := request["input"].([]any)
					require.True(t, ok)
					sanitizeOpenAIResponsesOrphanToolOutputs(request, items, false)
					data, err := json.Marshal(request)
					require.NoError(t, err)
					forwarded = string(data)
				case "http_forward_with_context":
					upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(http.StatusOK, namespaceForwardOKResponse)}}
					result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), newOpenAIRejectedFieldTestContext(body), newOpenAIOAuthNamespaceTestAccount(), body)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Len(t, upstream.bodies, 1)
					forwarded = string(upstream.bodies[0])
				default:
					body = []byte(strings.Replace(string(body), `{"model":`, `{"type":"response.create","model":`, 1))
					data, _, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(body, newOpenAIOAuthNamespaceTestAccount(), false)
					require.NoError(t, err)
					forwarded = string(data)
					require.Equal(t, "response.create", gjson.Get(forwarded, "type").String())
				}
				before, after := gjson.GetBytes(body, "input").Array(), gjson.Get(forwarded, "input").Array()
				t.Logf("items %d -> %d; input=%s", len(before), len(after), gjson.Get(forwarded, "input").Raw)
				if route == "http_forward_with_context" && tc.name == "whitespace" {
					// Both baseline and current native HTTP normalize this ID before sanitize.
					require.Len(t, after, 2)
					return
				}
				require.Len(t, after, len(before))
				last := after[len(after)-1]
				require.Equal(t, "notification", last.Get("name").String())
				require.Equal(t, "context", last.Get("output").String())
				if route != "http_forward_with_context" {
					require.JSONEq(t, input, gjson.Get(forwarded, "input").Raw)
				}
			})
		}
	}
}
