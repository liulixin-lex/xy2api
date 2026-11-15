package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func reviewM2Event(t *testing.T, raw string) *ResponsesStreamEvent {
	t.Helper()
	var event ResponsesStreamEvent
	require.NoError(t, json.Unmarshal([]byte(raw), &event))
	return &event
}

func TestReviewM2ReasoningContentParts(t *testing.T) {
	events := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning"}}`,
		`{"type":"response.content_part.added","output_index":0,"item_id":"rs_1","part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","output_index":0,"item_id":"rs_1","delta":"think"}`,
		`{"type":"response.content_part.done","output_index":0,"item_id":"rs_1","part":{"type":"reasoning_text","text":"think"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"think"}]}}`,
		`{"type":"response.output_text.delta","output_index":1,"item_id":"m","delta":"Answer"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[]}}`,
	}
	a := NewBufferedResponseAccumulator()
	state := NewResponsesEventToChatState()
	var answer, reasoning string
	for _, raw := range events {
		event := reviewM2Event(t, raw)
		a.ProcessEvent(event)
		for _, chunk := range ResponsesEventToChatChunks(event, state) {
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != nil {
					answer += *choice.Delta.Content
				}
				if choice.Delta.ReasoningContent != nil {
					reasoning += *choice.Delta.ReasoningContent
				}
			}
		}
	}
	require.NoError(t, a.Err())
	require.NoError(t, state.Err())
	response := &ResponsesResponse{Status: "completed"}
	a.SupplementResponseOutput(response)
	chat := ResponsesToChatCompletions(response, "m")
	require.JSONEq(t, `"Answer"`, string(chat.Choices[0].Message.Content))
	require.Equal(t, "think", chat.Choices[0].Message.ReasoningContent)
	require.Equal(t, "Answer", answer)
	require.Equal(t, "think", reasoning)
}

func TestReviewM2AnonymousDeltaUsesCurrentItem(t *testing.T) {
	for _, index := range []int{0, 1} {
		a := NewBufferedResponseAccumulator()
		if index == 1 {
			a.ProcessEvent(reviewM2Event(t, `{"type":"response.output_item.done","output_index":0,"item":{"id":"r","type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}}`))
		}
		a.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: index, OutputIndexPresent: true, Item: &ResponsesOutput{ID: "msg_1", Type: "message", Content: []ResponsesContentPart{}}})
		a.ProcessEvent(reviewM2Event(t, `{"type":"response.output_text.delta","delta":"hi"}`))
		a.ProcessEvent(reviewM2Event(t, `{"type":"response.completed","response":{"status":"completed","output":[]}}`))
		response := &ResponsesResponse{Status: "completed"}
		a.SupplementResponseOutput(response)
		require.NoError(t, a.Err())
		require.Len(t, response.Output, index+1)
		message := response.Output[index]
		require.Equal(t, "msg_1", message.ID)
		require.Len(t, message.Content, 1)
		require.Equal(t, "hi", message.Content[0].Text)
	}
}

func TestReviewM2TerminalRegeneratedIDs(t *testing.T) {
	for _, kind := range []string{"function_call", "custom_tool_call", "reasoning", "message"} {
		t.Run(kind, func(t *testing.T) {
			item := ResponsesOutput{ID: "stream-id", Type: kind}
			switch kind {
			case "function_call":
				item.CallID = "call"
				item.Name = "f"
				item.Arguments = `{}`
			case "custom_tool_call":
				item.CallID = "call"
				item.Name = "f"
				item.Input = "patch"
			case "reasoning":
				item.Summary = []ResponsesSummary{{Type: "summary_text", Text: "think"}}
			case "message":
				item.Role = "assistant"
				item.Content = []ResponsesContentPart{{Type: "output_text", Text: "Hello"}}
			}
			for _, terminalID := range []string{"new-terminal-id", ""} {
				a := NewBufferedResponseAccumulator()
				state := NewResponsesEventToChatState()
				state.IncludeUsage = true
				var text, reasoning, arguments, finish string
				finishes := 0
				consume := func(event *ResponsesStreamEvent) {
					a.ProcessEvent(event)
					for _, chunk := range ResponsesEventToChatChunks(event, state) {
						for _, choice := range chunk.Choices {
							if choice.Delta.Content != nil {
								text += *choice.Delta.Content
							}
							if choice.Delta.ReasoningContent != nil {
								reasoning += *choice.Delta.ReasoningContent
							}
							for _, tool := range choice.Delta.ToolCalls {
								arguments += tool.Function.Arguments
							}
							if choice.FinishReason != nil {
								finish = *choice.FinishReason
								finishes++
							}
						}
					}
				}
				consume(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndexPresent: true, Item: &item})
				consume(&ResponsesStreamEvent{Type: "response.output_item.done", OutputIndexPresent: true, Item: &item})
				terminal := item
				terminal.ID = terminalID
				response := &ResponsesResponse{Status: "incomplete", IncompleteDetails: &ResponsesIncompleteDetails{Reason: "max_output_tokens"}, Output: []ResponsesOutput{terminal}, Usage: &ResponsesUsage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7}}
				consume(&ResponsesStreamEvent{Type: "response.completed", Response: response})
				require.NoError(t, state.Err())
				require.NoError(t, a.Err())
				require.Equal(t, "length", finish)
				require.Equal(t, 1, finishes)
				require.Equal(t, 5, state.Usage.PromptTokens)
				a.SupplementResponseOutput(response)
				require.NoError(t, a.Err())
				require.Len(t, response.Output, 1)
				require.Equal(t, "stream-id", response.Output[0].ID)
				switch kind {
				case "message":
					require.Equal(t, "Hello", text)
				case "reasoning":
					require.Equal(t, "think", reasoning)
				case "function_call":
					require.Equal(t, `{}`, arguments)
				case "custom_tool_call":
					require.Equal(t, "patch", arguments)
				}
			}
		})
	}
	t.Run("partial terminal matches content before index", func(t *testing.T) {
		a := NewBufferedResponseAccumulator()
		for _, raw := range []string{
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"first","type":"message","content":[{"type":"output_text","text":"First"}]}}`,
			`{"type":"response.output_item.done","output_index":2,"item":{"id":"last","type":"message","content":[{"type":"output_text","text":"Last"}]}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"id":"new","type":"message","content":[{"type":"output_text","text":"Last"}]}]}}`,
		} {
			a.ProcessEvent(reviewM2Event(t, raw))
		}
		require.NoError(t, a.Err())
		out := a.BuildOutput()
		require.Len(t, out, 2)
		require.Equal(t, "first", out[0].ID)
		require.Equal(t, "First", out[0].Content[0].Text)
		require.Equal(t, "last", out[1].ID)
		require.Equal(t, "Last", out[1].Content[0].Text)
	})
}
