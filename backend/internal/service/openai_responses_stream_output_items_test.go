package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A terminal event that arrives with an empty output must be rebuilt from the
// items the stream reported, not from delta accumulation. The accumulator
// models only one reasoning and one message, so rebuilding through it collapses
// a multi-item turn into a single fabricated message.
func TestNormalizeResponsesStreamingTerminalOutputPreservesReportedItems(t *testing.T) {
	doneItems := newResponsesStreamOutputItems()

	doneItems.Observe([]byte(`{
		"type":"response.output_item.done",
		"output_index":0,
		"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"opaque"}
	}`))
	doneItems.Observe([]byte(`{
		"type":"response.output_item.done",
		"output_index":1,
		"item":{"id":"msg_1","type":"message","status":"completed","phase":"final_answer","role":"assistant","content":[{"type":"output_text","text":"shipped","annotations":[],"logprobs":[]}]}
	}`))

	normalized, changed := normalizeResponsesStreamingTerminalOutput(
		[]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`),
		nil,
		doneItems,
		nil,
	)
	require.True(t, changed)

	output := gjson.GetBytes(normalized, "response.output")
	require.True(t, output.IsArray())
	require.Len(t, output.Array(), 2, "both reported items must survive")

	require.Equal(t, "reasoning", gjson.GetBytes(normalized, "response.output.0.type").String())
	require.Equal(t, "rs_1", gjson.GetBytes(normalized, "response.output.0.id").String())
	require.Equal(t, "opaque", gjson.GetBytes(normalized, "response.output.0.encrypted_content").String(),
		"fields the gateway does not model must survive verbatim")

	require.Equal(t, "message", gjson.GetBytes(normalized, "response.output.1.type").String())
	require.Equal(t, "msg_1", gjson.GetBytes(normalized, "response.output.1.id").String(),
		"the reported id must be reused, not regenerated")
	require.Equal(t, "completed", gjson.GetBytes(normalized, "response.output.1.status").String())
	require.Equal(t, "final_answer", gjson.GetBytes(normalized, "response.output.1.phase").String())
	require.Equal(t, "shipped", gjson.GetBytes(normalized, "response.output.1.content.0.text").String())
}

// Items are ordered by output_index, not by arrival order.
func TestResponsesStreamOutputItemsOrderByOutputIndex(t *testing.T) {
	doneItems := newResponsesStreamOutputItems()
	doneItems.Observe([]byte(`{"type":"response.output_item.done","output_index":2,"item":{"id":"c","type":"message"}}`))
	doneItems.Observe([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"a","type":"reasoning"}}`))

	built, ok := doneItems.BuildOutput()
	require.True(t, ok)
	require.Equal(t, "a", gjson.GetBytes(built, "0.id").String())
	require.Equal(t, "c", gjson.GetBytes(built, "1.id").String())
}

// A stream that never reports a done item keeps the previous rebuild path.
func TestNormalizeResponsesStreamingTerminalOutputIgnoresNonDoneEvents(t *testing.T) {
	doneItems := newResponsesStreamOutputItems()
	doneItems.Observe([]byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message"}}`))
	doneItems.Observe([]byte(`{"type":"response.output_text.delta","output_index":0,"delta":"hi"}`))
	require.False(t, doneItems.HasItems())

	raw := []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`)
	normalized, changed := normalizeResponsesStreamingTerminalOutput(raw, nil, doneItems, nil)
	require.False(t, changed)
	require.Equal(t, string(raw), string(normalized))
}

// The terminal event can arrive with a non-empty but truncated output: the
// stream reported two items, the terminal carries one, and its id was not the
// one the stream reported. The reported items win.
func TestNormalizeResponsesStreamingTerminalOutputRepairsTruncatedOutput(t *testing.T) {
	doneItems := newResponsesStreamOutputItems()
	doneItems.Observe([]byte(`{
		"type":"response.output_item.done","output_index":0,
		"item":{"id":"rs_real","type":"reasoning","status":"in_progress","summary":[]}
	}`))
	doneItems.Observe([]byte(`{
		"type":"response.output_item.done","output_index":1,
		"item":{"id":"msg_real","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"shipped","annotations":[],"logprobs":[]}]}
	}`))

	normalized, changed := normalizeResponsesStreamingTerminalOutput([]byte(`{
		"type":"response.completed",
		"response":{"status":"completed","output":[{"type":"message","role":"assistant","id":"msg_fabricated","status":"completed","content":[{"type":"output_text","text":"shipped","annotations":[],"logprobs":[]}]}]}
	}`), nil, doneItems, nil)
	require.True(t, changed)

	require.Len(t, gjson.GetBytes(normalized, "response.output").Array(), 2)
	require.Equal(t, "reasoning", gjson.GetBytes(normalized, "response.output.0.type").String())
	require.Equal(t, "rs_real", gjson.GetBytes(normalized, "response.output.0.id").String())
	require.Equal(t, "msg_real", gjson.GetBytes(normalized, "response.output.1.id").String(),
		"the id the stream reported must replace the fabricated one")
}

// A terminal output that is already complete is never rewritten.
func TestNormalizeResponsesStreamingTerminalOutputLeavesCompleteOutputAlone(t *testing.T) {
	doneItems := newResponsesStreamOutputItems()
	doneItems.Observe([]byte(`{
		"type":"response.output_item.done","output_index":0,
		"item":{"id":"msg_real","type":"message","status":"completed"}
	}`))

	raw := []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_upstream","status":"completed","vendor":"keep"}]}}`)
	normalized, changed := normalizeResponsesStreamingTerminalOutput(raw, nil, doneItems, nil)
	require.False(t, changed)
	require.Equal(t, string(raw), string(normalized))
}

func TestReviewNativeStreamOutputJSONKeepsReconstruction(t *testing.T) {
	text := strings.Repeat("escaped \"value\" <&> 中文\n", 2048)
	newAccumulator := func() *apicompat.BufferedResponseAccumulator {
		acc := apicompat.NewBufferedResponseAccumulator()
		acc.ProcessEvent(&apicompat.ResponsesStreamEvent{Type: "response.reasoning_text.delta", Delta: "reason <&> 中文"})
		acc.ProcessEvent(&apicompat.ResponsesStreamEvent{Type: "response.output_text.delta", Delta: text})
		acc.ProcessEvent(&apicompat.ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 2, Item: &apicompat.ResponsesOutput{Type: "function_call", CallID: "call_original", Name: "fixture_tool"}})
		acc.ProcessEvent(&apicompat.ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 2, Delta: "{\"x\":1}"})
		return acc
	}
	image := json.RawMessage("{\"id\":\"image_original\",\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"fixture-image\"}")
	for _, withImage := range []bool{false, true} {
		name := "text_reasoning_tools"
		var images []json.RawMessage
		if withImage {
			name = "mixed_image"
			images = []json.RawMessage{image}
		}
		t.Run(name, func(t *testing.T) {
			payload := []byte("{\"type\":\"response.completed\",\"sequence_number\":19,\"response\":{\"id\":\"resp_original\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}}")
			normalized, changed := normalizeResponsesStreamingTerminalOutput(payload, newAccumulator(), newResponsesStreamOutputItems(), images)
			require.True(t, changed)
			require.True(t, json.Valid(normalized))
			require.Equal(t, "resp_original", gjson.GetBytes(normalized, "response.id").String())
			require.Equal(t, int64(19), gjson.GetBytes(normalized, "sequence_number").Int())
			require.Equal(t, int64(4), gjson.GetBytes(normalized, "response.usage.input_tokens").Int())
			require.Equal(t, int64(1), gjson.GetBytes(normalized, "response.usage.output_tokens").Int())
			output := gjson.GetBytes(normalized, "response.output").Array()
			require.Len(t, output, 3+len(images))
			require.Equal(t, "reasoning", output[0].Get("type").String())
			require.Equal(t, "reason <&> 中文", output[0].Get("summary.0.text").String())
			require.Equal(t, "message", output[1].Get("type").String())
			require.Equal(t, text, output[1].Get("content.0.text").String())
			require.Equal(t, "function_call", output[2].Get("type").String())
			require.Equal(t, "call_original", output[2].Get("call_id").String())
			require.Equal(t, "fixture_tool", output[2].Get("name").String())
			require.Equal(t, "{\"x\":1}", output[2].Get("arguments").String())
			if withImage {
				require.Equal(t, "image_original", output[3].Get("id").String())
				require.Equal(t, "fixture-image", output[3].Get("result").String())
			}
		})
	}

	t.Run("authoritative_custom_tool", func(t *testing.T) {
		done := newResponsesStreamOutputItems()
		item := "{\"id\":\"tool_original\",\"type\":\"custom_tool_call\",\"call_id\":\"call_custom\",\"name\":\"custom_fixture\",\"input\":\"print(\\\"<&>中文\\\")\",\"vendor_field\":\"preserve\"}"
		done.Observe([]byte("{\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + item + "}"))
		normalized, changed := normalizeResponsesStreamingTerminalOutput([]byte("{\"type\":\"response.completed\",\"sequence_number\":20,\"response\":{\"id\":\"resp_custom\",\"status\":\"completed\",\"output\":[]}}"), newAccumulator(), done, nil)
		require.True(t, changed)
		require.Len(t, gjson.GetBytes(normalized, "response.output").Array(), 1, "authoritative tool item must win over fallback text/reasoning")
		require.JSONEq(t, item, gjson.GetBytes(normalized, "response.output.0").Raw)
		require.Equal(t, "resp_custom", gjson.GetBytes(normalized, "response.id").String())
		require.Equal(t, int64(20), gjson.GetBytes(normalized, "sequence_number").Int())
	})
	t.Run("image_only", func(t *testing.T) {
		built, ok := buildResponsesOutputJSON(nil, []json.RawMessage{image})
		require.True(t, ok)
		require.JSONEq(t, "["+string(image)+"]", string(built))
	})
	t.Run("empty", func(t *testing.T) {
		built, ok := buildResponsesOutputJSON(apicompat.NewBufferedResponseAccumulator(), nil)
		require.False(t, ok)
		require.Nil(t, built)
	})
}
