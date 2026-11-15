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
