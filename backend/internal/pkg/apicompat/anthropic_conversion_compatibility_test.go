package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicObjectCompatibleSchemas(t *testing.T) {
	for _, entry := range []string{"chat", "responses"} {
		for _, tc := range []struct{ schema, required string }{
			{`{"type":"object","properties":{"a":{},"b":{}},"anyOf":[{"required":["a"]},{"required":["b"]}]}`, ""},
			{`{"type":"object","properties":{"a":{},"b":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`, ""},
			{`{"allOf":[{"required":["a"]},{"required":["b"]}]}`, `["a","b"]`},
			{`{"type":["object","null"],"properties":{"a":{"type":"string"}},"required":["a"]}`, `["a"]`},
			{`{"type":["object"],"properties":{"a":{}},"required":["a"]}`, `["a"]`},
			{`{"type":null,"properties":null,"required":null,"oneOf":null}`, ""},
		} {
			t.Run(entry+tc.schema, func(t *testing.T) {
				tools := `"tools":[{"type":"function","name":"lookup","parameters":` + tc.schema + `}]`
				if entry == "chat" {
					tools = `"tools":[{"type":"function","function":{"name":"lookup","parameters":` + tc.schema + `}}]`
				}
				out, err := anthropicPolicyRequest(t, entry, tools)
				require.NoError(t, err)
				var schema map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(out.Tools[0].InputSchema, &schema))
				require.JSONEq(t, `"object"`, string(schema["type"]))
				if tc.required == "" {
					require.Empty(t, schema["required"])
				} else {
					require.JSONEq(t, tc.required, string(schema["required"]))
				}
				require.NotEmpty(t, schema["properties"])
				if tc.required == `["a"]` {
					require.Contains(t, string(schema["properties"]), `"a"`)
				}
			})
		}
	}
}

func TestAnthropicSamplingAndThinkingByVersion(t *testing.T) {
	for _, model := range []string{"claude-sonnet-4-5", "claude-sonnet-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2"} {
		for _, entry := range []string{"chat", "responses"} {
			t.Run(entry+model, func(t *testing.T) {
				fields := `"model":"` + model + `","temperature":0.7,"top_p":0.8,"reasoning_effort":"high","reasoning":{"effort":"high"}`
				out, err := anthropicPolicyRequest(t, entry, fields)
				require.NoError(t, err)
				switch model {
				case "claude-sonnet-4-5":
					require.Equal(t, "enabled", out.Thinking.Type)
					require.Positive(t, out.Thinking.BudgetTokens)
					require.NotNil(t, out.Temperature)
				case "claude-sonnet-4-6":
					require.Equal(t, "adaptive", out.Thinking.Type)
					require.Zero(t, out.Thinking.BudgetTokens)
					require.NotNil(t, out.Temperature)
				default:
					require.Equal(t, "adaptive", out.Thinking.Type)
					require.Zero(t, out.Thinking.BudgetTokens)
					require.Nil(t, out.Temperature)
					require.Nil(t, out.TopP)
				}
				if model == "claude-sonnet-4-5" {
					require.Nil(t, out.OutputConfig)
				} else {
					require.Equal(t, "high", out.OutputConfig.Effort)
				}
				require.Nil(t, out.TopP)
			})
		}
	}
}

func TestAnthropicToolAndStopCompatibility(t *testing.T) {
	for _, typ := range []string{"bash_20250124", "text_editor_20250728", "web_fetch_20250910"} {
		out, err := anthropicPolicyRequest(t, "responses", `"tools":[{"type":"`+typ+`","name":"native"}]`)
		require.NoError(t, err)
		require.Len(t, out.Tools, 1)
		require.Equal(t, typ, out.Tools[0].Type)
	}
	out, err := anthropicPolicyRequest(t, "responses", `"tools":[{"type":"custom","name":"patch"}]`)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"object","properties":{}}`, string(out.Tools[0].InputSchema))
	for _, entry := range []string{"chat", "responses"} {
		out, err := anthropicPolicyRequest(t, entry, `"stop":["\n"," END ","\t"]`)
		require.NoError(t, err)
		require.Equal(t, []string{" END "}, out.StopSeqs)
		out, err = anthropicPolicyRequest(t, entry, `"function_call":null,"tool_choice":null,"text":{"format":null}`)
		require.NoError(t, err)
		require.Empty(t, out.ToolChoice)
		for _, mode := range []string{"auto", "required"} {
			out, err := anthropicPolicyRequest(t, entry, `"tool_choice":{"type":"allowed_tools","mode":"`+mode+`","tools":[{"type":"function","name":"lookup"}]}`)
			require.NoError(t, err)
			want := "auto"
			if mode == "required" {
				want = "any"
			}
			require.JSONEq(t, `{"type":"`+want+`"}`, string(out.ToolChoice))
		}
	}
}

func TestAnthropicDroppedToolSelectionAndOriginalFieldPath(t *testing.T) {
	for _, entry := range []string{"chat", "responses"} {
		_, err := anthropicPolicyRequest(t, entry, `"tools":[{"type":"file_search"}],"tool_choice":{"type":"file_search"}`)
		requireUnsupportedConversion(t, err, "tool_choice")
	}
	_, err := anthropicPolicyRequest(t, "chat", `"tools":[{"type":"file_search"},{"type":"function","function":{"name":"lookup","parameters":{"type":"string"}}}]`)
	requireUnsupportedConversion(t, err, "tools[1].function.parameters.type")
}

func TestAnthropicNativeDatedToolFromChatIsDropped(t *testing.T) {
	var chat ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"run shell"}],"tools":[{"type":"bash_20250124","name":"bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]}`), &chat))
	intermediate, err := ChatCompletionsToResponses(&chat)
	require.NoError(t, err)
	out, err := ResponsesToAnthropicRequest(intermediate)
	require.NoError(t, err)
	require.Empty(t, out.Tools)
}

func TestAnthropicReasoningNoneAndSupportedEfforts(t *testing.T) {
	for _, entry := range []string{"chat", "responses"} {
		for _, tc := range []struct{ model, effort, thinking, outputEffort string }{
			{"claude-sonnet-4-6", "none", "disabled", ""},
			{"claude-opus-4-7", "none", "disabled", ""},
			{"anthropic/claude-opus-4.8", "none", "disabled", ""},
			{"claude-opus-4-8", "xhigh", "adaptive", "xhigh"},
			{"claude-sonnet-5", "xhigh", "adaptive", "xhigh"},
			{"claude-fable-5-2", "xhigh", "adaptive", "xhigh"},
			{"claude-opus-4-6", "xhigh", "adaptive", "max"},
			{"claude-sonnet-5", "none", "adaptive", "low"},
			{"claude-fable-5", "none", "adaptive", "low"},
			{"claude-mythos-preview", "none", "adaptive", "low"},
			{"claude-sonnet-4-5", "high", "enabled", ""},
			{"claude-haiku-4-5", "high", "enabled", ""},
			{"claude-opus-4-5", "xhigh", "enabled", "high"},
			{"deepseek-v4", "xhigh", "enabled", "max"},
		} {
			t.Run(entry+tc.model+tc.effort, func(t *testing.T) {
				fields := `"model":"` + tc.model + `","reasoning_effort":"` + tc.effort + `","reasoning":{"effort":"` + tc.effort + `"}`
				out, err := anthropicPolicyRequest(t, entry, fields)
				require.NoError(t, err)
				require.Equal(t, tc.thinking, out.Thinking.Type)
				if tc.outputEffort == "" {
					require.Nil(t, out.OutputConfig)
				} else {
					require.Equal(t, tc.outputEffort, out.OutputConfig.Effort)
				}
			})
		}
	}
}

func TestAnthropicPHPEmptySchemaAndSearchPreview(t *testing.T) {
	for _, entry := range []string{"chat", "responses"} {
		for _, schema := range []string{`[]`, `[ ]`, `{"type":"object","properties":[]}`} {
			tool := `{"type":"function","name":"lookup","parameters":` + schema + `}`
			if entry == "chat" {
				tool = `{"type":"function","function":{"name":"lookup","parameters":` + schema + `}}`
			}
			out, err := anthropicPolicyRequest(t, entry, `"tools":[`+tool+`]`)
			require.NoError(t, err)
			require.JSONEq(t, `{"type":"object","properties":{}}`, string(out.Tools[0].InputSchema))
		}
		out, err := anthropicPolicyRequest(t, entry, `"tools":[{"type":"web_search_preview"}]`)
		require.NoError(t, err)
		want, err := anthropicPolicyRequest(t, entry, `"tools":[{"type":"web_search"}]`)
		require.NoError(t, err)
		require.Equal(t, want.Tools, out.Tools)
		out, err = anthropicPolicyRequest(t, entry, `"model":"us.anthropic.claude-sonnet-4-6","temperature":0.7,"top_p":0.8`)
		require.NoError(t, err)
		require.Equal(t, 0.7, *out.Temperature)
		require.Nil(t, out.TopP)
		out, err = anthropicPolicyRequest(t, entry, `"model":"deepseek-v4","temperature":0.7,"top_p":0.8`)
		require.NoError(t, err)
		require.Equal(t, 0.7, *out.Temperature)
		require.Equal(t, 0.8, *out.TopP)
	}
}
