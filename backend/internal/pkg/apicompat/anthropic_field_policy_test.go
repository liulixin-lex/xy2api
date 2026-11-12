package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func anthropicPolicyRequest(t *testing.T, entry, fields string) (*AnthropicRequest, error) {
	t.Helper()
	if entry == "chat" {
		var req ChatCompletionsRequest
		require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],`+fields+`}`), &req))
		responses, err := ChatCompletionsToResponses(&req)
		if err != nil {
			return nil, err
		}
		return ResponsesToAnthropicRequest(responses)
	}
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-sonnet-4-5","input":"hi",`+fields+`}`), &req))
	return ResponsesToAnthropicRequest(&req)
}

func TestAnthropicPolicyIgnoredFields(t *testing.T) {
	fields := map[string]string{"logprobs": "true", "top_logprobs": "4", "seed": "123", "presence_penalty": "0.5", "frequency_penalty": "0.5", "logit_bias": `{"1":5}`, "user": `"client"`, "metadata": `{"a":"b"}`, "prediction": `{"type":"content","content":"predicted"}`, "service_tier": `"priority"`, "audio": `{"voice":"alloy"}`, "store": "true", "modalities": `["text","audio"]`}
	for _, entry := range []string{"chat", "responses"} {
		baseline, err := anthropicPolicyRequest(t, entry, `"stream":true`)
		require.NoError(t, err)
		for field, value := range fields {
			t.Run(entry+"/"+field, func(t *testing.T) {
				out, err := anthropicPolicyRequest(t, entry, `"stream":true,"`+field+`":`+value)
				require.NoError(t, err)
				require.Equal(t, baseline, out)
			})
		}
	}
}

func TestAnthropicPolicyMappedFields(t *testing.T) {
	for _, entry := range []string{"chat", "responses"} {
		for _, tc := range []struct {
			fields string
			check  func(*AnthropicRequest)
		}{
			{`"temperature":1.8`, func(r *AnthropicRequest) { require.Equal(t, 1.0, *r.Temperature) }},
			{`"temperature":0.25`, func(r *AnthropicRequest) { require.Equal(t, 0.25, *r.Temperature) }},
			{`"top_p":0.8`, func(r *AnthropicRequest) { require.Equal(t, 0.8, *r.TopP) }},
			{`"n":1`, func(r *AnthropicRequest) { require.Len(t, r.Messages, 1) }},
			{`"tool_choice":"required"`, func(r *AnthropicRequest) { require.JSONEq(t, `{"type":"any"}`, string(r.ToolChoice)) }},
			{`"stream":true,"stream_options":{"include_usage":true}`, func(r *AnthropicRequest) { require.True(t, r.Stream) }},
		} {
			t.Run(entry+tc.fields, func(t *testing.T) {
				r, err := anthropicPolicyRequest(t, entry, tc.fields)
				require.NoError(t, err)
				tc.check(r)
			})
		}
		limit := `"max_output_tokens":17`
		if entry == "chat" {
			limit = `"max_tokens":8,"max_completion_tokens":17`
		}
		r, err := anthropicPolicyRequest(t, entry, limit)
		require.NoError(t, err)
		require.Equal(t, 17, r.MaxTokens)
		for _, strict := range []string{"true", "false"} {
			tool := `{"type":"function","name":"lookup","parameters":{"type":"object"},"strict":` + strict + `}`
			if entry == "chat" {
				tool = `{"type":"function","function":{"name":"lookup","parameters":{"type":"object"},"strict":` + strict + `}}`
			}
			for _, parallel := range []bool{true, false} {
				flag := "false"
				if parallel {
					flag = "true"
				}
				r, err := anthropicPolicyRequest(t, entry, `"tools":[`+tool+`],"parallel_tool_calls":`+flag+`,"tool_choice":{"type":"function","name":"lookup","function":{"name":"lookup"}}`)
				require.NoError(t, err)
				require.Len(t, r.Tools, 1)
				wire, err := json.Marshal(r.Tools[0])
				require.NoError(t, err)
				require.NotContains(t, string(wire), "strict")
				var choice map[string]any
				require.NoError(t, json.Unmarshal(r.ToolChoice, &choice))
				require.Equal(t, "tool", choice["type"])
				require.Equal(t, "lookup", choice["name"])
				require.Equal(t, !parallel, choice["disable_parallel_tool_use"])
			}
		}
		format := `"text":{"format":{"type":"json_schema","strict":true,"schema":{"type":"object"}}}`
		if entry == "chat" {
			format = `"response_format":{"type":"json_schema","json_schema":{"name":"response","strict":true,"schema":{"type":"object"}}}`
		}
		r, err = anthropicPolicyRequest(t, entry, format)
		require.NoError(t, err)
		require.JSONEq(t, `{"type":"json_schema","schema":{"type":"object"}}`, string(r.OutputConfig.Format))
	}
	for _, stop := range []string{`"END"`, `["END"," DONE "]`} {
		r, err := anthropicPolicyRequest(t, "chat", `"max_tokens":9,"stop":`+stop)
		require.NoError(t, err)
		require.Equal(t, 9, r.MaxTokens)
		require.Equal(t, "END", r.StopSeqs[0])
	}
}

func TestAnthropicPolicyRejectedFields(t *testing.T) {
	for _, entry := range []string{"chat", "responses"} {
		for _, tc := range []struct{ field, value string }{{"n", "2"}, {"n", "0"}, {"n", "-1"}, {"n", "1.5"}, {"temperature", "-0.1"}, {"tool_choice", `"future"`}, {"tool_choice", `{"type":"function"}`}, {"tools", `[{"type":"future"}]`}, {"tools", `[{"type":"custom","name":"patch"}]`}} {
			t.Run(entry+tc.field+tc.value, func(t *testing.T) {
				_, err := anthropicPolicyRequest(t, entry, `"`+tc.field+`":`+tc.value)
				requireUnsupportedConversion(t, err, tc.field)
			})
		}
		_, err := anthropicPolicyRequest(t, entry, `"temperature":1,"top_p":0.99`)
		requireUnsupportedConversion(t, err, "top_p")
		for _, kind := range []string{"json_object"} {
			format := `"text":{"format":{"type":"` + kind + `"}}`
			field := "text.format"
			if entry == "chat" {
				format = `"response_format":{"type":"` + kind + `"}`
				field = "response_format"
			}
			_, err := anthropicPolicyRequest(t, entry, format)
			requireUnsupportedConversion(t, err, field)
		}
	}
	_, err := anthropicPolicyRequest(t, "chat", `"stop":"  "`)
	requireUnsupportedConversion(t, err, "stop")
}

func TestAnthropicReasoningExtensionEfforts(t *testing.T) {
	for _, entry := range []string{"chat", "responses"} {
		for _, model := range []string{"claude-sonnet-4-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
			for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "invalid"} {
				t.Run(entry+model+effort, func(t *testing.T) {
					field := `"reasoning":{"effort":"` + effort + `"}`
					if entry == "chat" {
						field = `"reasoning_effort":"` + effort + `"`
					}
					var req ResponsesRequest
					if entry == "chat" {
						var chat ChatCompletionsRequest
						require.NoError(t, json.Unmarshal([]byte(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],`+field+`}`), &chat))
						intermediate, err := ChatCompletionsToResponses(&chat)
						require.NoError(t, err)
						req = *intermediate
					} else {
						require.NoError(t, json.Unmarshal([]byte(`{"model":"`+model+`","input":"hi",`+field+`}`), &req))
					}
					out, err := ResponsesToAnthropicRequest(&req)
					if effort == "invalid" {
						requireUnsupportedConversion(t, err, "reasoning")
						return
					}
					require.NoError(t, err)
					if effort == "none" && model == "claude-sonnet-4-5" {
						require.Equal(t, "disabled", out.Thinking.Type)
						return
					}
					want := effort
					if want == "none" || want == "minimal" {
						want = "low"
					}
					if model == "claude-sonnet-4-5" && want == "xhigh" {
						want = "max"
					}
					require.Equal(t, want, out.OutputConfig.Effort)
					if model == "claude-sonnet-5-5" && effort == "none" {
						require.Equal(t, "between_tools", out.Thinking.Type)
					}
				})
			}
		}
	}
}

func TestAnthropicResponsesFieldAliasesDoNotChangeNativeWire(t *testing.T) {
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-sonnet-4-5","input":"hi","stop":["END"],"max_completion_tokens":21,"response_format":{"type":"json_schema","json_schema":{"name":"out","strict":true,"schema":{"type":"object"}}},"reasoning_effort":"low"}`), &req))
	wire, err := json.Marshal(req)
	require.NoError(t, err)
	require.NotContains(t, string(wire), "END")
	require.NotContains(t, string(wire), "json_schema")
	require.Nil(t, req.MaxOutputTokens)
	out, err := ResponsesToAnthropicRequest(&req)
	require.NoError(t, err)
	require.Equal(t, []string{"END"}, out.StopSeqs)
	require.Equal(t, 21, out.MaxTokens)
	require.Equal(t, "low", out.OutputConfig.Effort)
	require.JSONEq(t, `{"type":"json_schema","schema":{"type":"object"}}`, string(out.OutputConfig.Format))
}

func TestAnthropicLegacyFunctionPolicy(t *testing.T) {
	out, err := anthropicPolicyRequest(t, "chat", `"functions":[{"name":"lookup","parameters":{"type":"object"},"strict":true}],"function_call":{"name":"lookup"}`)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)
	require.JSONEq(t, `{"type":"tool","name":"lookup"}`, string(out.ToolChoice))
}

func TestAnthropicResponseFinishAndUsagePolicy(t *testing.T) {
	for _, tc := range []struct{ stop, finish, status string }{{"end_turn", "stop", "completed"}, {"stop_sequence", "stop", "completed"}, {"tool_use", "tool_calls", "completed"}, {"max_tokens", "length", "incomplete"}} {
		t.Run(tc.stop, func(t *testing.T) {
			content := []AnthropicContentBlock{{Type: "text", Text: "done"}}
			if tc.stop == "tool_use" {
				content = []AnthropicContentBlock{{Type: "tool_use", ID: "toolu_1", Name: "lookup", Input: json.RawMessage(`{}`)}}
			}
			response := AnthropicToResponsesResponse(&AnthropicResponse{ID: "msg_1", Model: "claude-sonnet-4-5", Content: content, StopReason: AnthropicStopReasonPtr(tc.stop), Usage: AnthropicUsage{InputTokens: 11, CacheReadInputTokens: 5, CacheCreationInputTokens: 3, OutputTokens: 7}})
			require.Equal(t, tc.status, response.Status)
			chat := ResponsesToChatCompletions(response, response.Model)
			require.Equal(t, tc.finish, chat.Choices[0].FinishReason)
			require.Equal(t, 19, chat.Usage.PromptTokens)
			require.Equal(t, 7, chat.Usage.CompletionTokens)
			require.Equal(t, 26, chat.Usage.TotalTokens)
		})
	}
}
