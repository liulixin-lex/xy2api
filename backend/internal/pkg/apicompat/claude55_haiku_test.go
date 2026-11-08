package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHaiku55ResponsesAdaptiveThinkingAndSampling(t *testing.T) {
	for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max", "none"} {
		out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-haiku-5-5", Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: effort}})
		require.NoError(t, err, effort)
		require.Equal(t, "adaptive", out.Thinking.Type, effort)
		require.Zero(t, out.Thinking.BudgetTokens, effort)
		want := effort
		switch effort {
		case "":
			want = "medium"
		case "none":
			want = "low"
		}
		require.Equal(t, want, out.OutputConfig.Effort, effort)
	}
	// Haiku 5.5 accepts forced tool use.
	out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-haiku-5-5", Input: json.RawMessage(`"hello"`), ToolChoice: json.RawMessage(`"required"`)})
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"any"}`, string(out.ToolChoice))

	temperature, topP := 0.7, 1.0
	_, err = ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-haiku-5-5", Input: json.RawMessage(`"hello"`), Temperature: &temperature})
	require.ErrorContains(t, err, "temperature")
	_, err = ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-haiku-5-5", Input: json.RawMessage(`"hello"`), TopP: &topP})
	require.ErrorContains(t, err, "top_p")
	temperature, topP = 1, 0.99
	_, err = ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-haiku-5-5", Input: json.RawMessage(`"hello"`), Temperature: &temperature, TopP: &topP})
	require.ErrorContains(t, err, "together")
	_, err = ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-haiku-5-5", Input: json.RawMessage(`"hello"`), TopP: &topP})
	require.NoError(t, err)

	// Haiku 4.5 keeps the manual thinking budget.
	old, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-haiku-4-5", Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: "high"}, Temperature: &temperature})
	require.NoError(t, err)
	require.Equal(t, "enabled", old.Thinking.Type)
	require.NotZero(t, old.Thinking.BudgetTokens)
}

func TestHaiku55SignedThinkingSurvivesResponsesConversion(t *testing.T) {
	block := AnthropicContentBlock{Type: "thinking", Thinking: "", Signature: "signed-haiku-block"}
	response := AnthropicToResponsesResponse(&AnthropicResponse{Model: "claude-haiku-5-5", Content: []AnthropicContentBlock{block, {Type: "tool_use", ID: "toolu_1", Name: "lookup", Input: json.RawMessage(`{}`)}}})
	require.Len(t, response.Output, 2)
	require.NotEmpty(t, response.Output[0].EncryptedContent)
	raw, err := json.Marshal(response.Output)
	require.NoError(t, err)
	var items []ResponsesInputItem
	require.NoError(t, json.Unmarshal(raw, &items))
	items = append(items, ResponsesInputItem{Type: "function_call_output", CallID: response.Output[1].CallID, Output: "ok"})
	raw, err = json.Marshal(items)
	require.NoError(t, err)
	converted, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-haiku-5-5", Input: raw})
	require.NoError(t, err)
	var blocks []AnthropicContentBlock
	require.NoError(t, json.Unmarshal(converted.Messages[0].Content, &blocks))
	require.Equal(t, block, blocks[0])
}
