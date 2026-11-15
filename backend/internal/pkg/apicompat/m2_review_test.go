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
	t.Run("v0.6.6 relay terminal", func(t *testing.T) {
		// The released Chat->Responses bridge regenerates reasoning and tool
		// item IDs in response.completed; force every terminal ID to differ.
		relay := NewChatCompletionsToResponsesStreamState("m")
		var events []ResponsesStreamEvent
		for _, raw := range []string{
			`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"pl"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"an"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"g","arguments":""}}]}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		} {
			var chunk ChatCompletionsChunk
			require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
			events = append(events, ChatCompletionsChunkToResponsesEvents(&chunk, relay)...)
		}
		events = append(events, FinalizeChatCompletionsResponsesStream(relay)...)
		state := NewResponsesEventToChatState()
		state.IncludeUsage = true
		a := NewBufferedResponseAccumulator()
		var final *ResponsesResponse
		var text, reasoning, finish string
		arguments := map[int]string{}
		finishes := 0
		streamIDs := map[string]bool{}
		for _, evt := range events {
			raw, err := json.Marshal(evt)
			require.NoError(t, err)
			event := reviewM2Event(t, string(raw))
			if event.Item != nil && event.Item.ID != "" {
				streamIDs[event.Item.ID] = true
			}
			if event.Type == "response.completed" {
				for i := range event.Response.Output {
					event.Response.Output[i].ID = "regenerated"
				}
				final = event.Response
			}
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
						arguments[*tool.Index] += tool.Function.Arguments
					}
					if choice.FinishReason != nil {
						finish = *choice.FinishReason
						finishes++
					}
				}
			}
		}
		require.NoError(t, state.Err())
		require.Equal(t, "ok", text)
		require.Equal(t, "plan", reasoning)
		require.Equal(t, map[int]string{0: `{"a":1}`, 1: `{}`}, arguments)
		require.Equal(t, "tool_calls", finish)
		require.Equal(t, 1, finishes)
		require.Equal(t, 7, state.Usage.TotalTokens)
		require.NotNil(t, final)
		a.SupplementResponseOutput(final)
		require.NoError(t, a.Err())
		require.NoError(t, ValidateResponsesForChat(final))
		require.Len(t, final.Output, 4)
		for _, item := range final.Output {
			require.True(t, streamIDs[item.ID], "terminal item %s keeps the streamed ID", item.Type)
		}
		chat := ResponsesToChatCompletions(final, "m")
		require.Equal(t, "tool_calls", chat.Choices[0].FinishReason)
		require.JSONEq(t, `"ok"`, string(chat.Choices[0].Message.Content))
		require.Equal(t, "plan", chat.Choices[0].Message.ReasoningContent)
		require.Len(t, chat.Choices[0].Message.ToolCalls, 2)
	})
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

func TestReviewM2QualifiedTerminalDefaults(t *testing.T) {
	for _, tc := range []struct{ event, status, reason string }{
		{"response.completed", "", ""}, {"response.done", "", ""}, {"response.incomplete", "incomplete", ""},
		{"response.completed", "incomplete", "max_tool_calls"}, {"response.done", "future", ""},
	} {
		for _, tools := range []bool{false, true} {
			response := &ResponsesResponse{Status: tc.status}
			if tc.reason != "" {
				response.IncompleteDetails = &ResponsesIncompleteDetails{Reason: tc.reason}
			}
			want := "stop"
			if tools {
				response.Output = []ResponsesOutput{{Type: "function_call", ID: "tool", CallID: "call", Name: "f", Arguments: `{}`}}
				if tc.status != "incomplete" || tc.reason != "" {
					want = "tool_calls"
				}
			}
			var notices []ConversionNotice
			opts := ConversionOptions{Record: func(n ConversionNotice) { notices = append(notices, n) }}
			require.NoError(t, ValidateResponsesForChatWithOptions(response, opts))
			require.Equal(t, want, ResponsesToChatCompletions(response, "m").Choices[0].FinishReason)
			expected := []ConversionNotice(nil)
			if tc.status == "future" || tc.reason != "" {
				expected = []ConversionNotice{{Field: "finish_reason", Action: "map"}}
			}
			require.Equal(t, expected, notices)
			notices = nil
			state := NewResponsesEventToChatState()
			state.Record = opts.Record
			event := &ResponsesStreamEvent{Type: tc.event, Response: response}
			chunks := ResponsesEventToChatChunks(event, state)
			require.NoError(t, state.Err())
			require.Equal(t, want, *chunks[len(chunks)-1].Choices[0].FinishReason)
			require.Empty(t, ResponsesEventToChatChunks(event, state))
			require.Equal(t, expected, notices)
			require.Equal(t, tc.status, response.Status, "terminal normalization must not mutate upstream input")
		}
	}
	for _, tools := range []bool{false, true} {
		state := NewResponsesEventToChatState()
		state.IncludeUsage = true
		want := "stop"
		if tools {
			ResponsesEventToChatChunks(reviewM2Event(t, `{"type":"response.output_item.done","output_index":0,"item":{"id":"t","type":"function_call","call_id":"c","name":"f","arguments":"{}"}}`), state)
			want = "tool_calls"
		}
		chunks := ResponsesEventToChatChunks(reviewM2Event(t, `{"type":"response.done","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`), state)
		require.NoError(t, state.Err())
		require.Equal(t, want, *chunks[len(chunks)-2].Choices[0].FinishReason)
		require.Equal(t, 7, chunks[len(chunks)-1].Usage.TotalTokens)
	}
	state := NewResponsesEventToChatState()
	require.Empty(t, ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.failed"}, state))
	require.Error(t, state.Err())
	require.Equal(t, "stop", ResponsesToChatCompletions(&ResponsesResponse{Status: "failed"}, "m").Choices[0].FinishReason, "legacy projector must never emit an empty enum")
	require.Error(t, ValidateResponsesForChat(&ResponsesResponse{Status: "failed"}))
}

func TestReviewM2VisibleSnapshotsDoNotRetractOrRepeat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		want   string
	}{
		{"trimmed done", []string{`{"type":"response.output_text.delta","delta":"Hello "}`, `{"type":"response.output_text.done","content_index":0,"text":"Hello"}`}, "Hello "},
		{"extended done", []string{`{"type":"response.output_text.delta","delta":"Hello "}`, `{"type":"response.output_text.done","text":"Hello world"}`}, "Hello world"},
		{"done adds index", []string{`{"type":"response.output_text.delta","delta":"Hello"}`, `{"type":"response.output_text.done","content_index":1,"text":"Hello"}`}, "Hello"},
		{"terminal renumbers message", []string{`{"type":"response.output_text.delta","output_index":1,"delta":"Hello"}`}, "Hello"},
		{"terminal only", nil, "Hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewResponsesEventToChatState()
			var text string
			finishes := 0
			events := append(append([]string(nil), tc.events...), `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hello"}]}]}}`)
			for _, raw := range events {
				for _, chunk := range ResponsesEventToChatChunks(reviewM2Event(t, raw), state) {
					for _, choice := range chunk.Choices {
						if choice.Delta.Content != nil {
							text += *choice.Delta.Content
						}
						if choice.FinishReason != nil {
							require.Equal(t, "stop", *choice.FinishReason)
							finishes++
						}
					}
				}
			}
			require.NoError(t, state.Err())
			require.Equal(t, tc.want, text)
			require.Equal(t, 1, finishes)
		})
	}
}

func TestReviewM2UnspecifiedIncompleteRetainsPartialArguments(t *testing.T) {
	for _, tc := range []struct {
		status, reason, arguments, want string
	}{
		{"incomplete", "", `{"a":`, "stop"},
		{"incomplete", "", `{}`, "stop"},
		{"incomplete", "max_tool_calls", `{"a":`, "stop"},
		{"incomplete", "max_tool_calls", `{}`, "tool_calls"},
		{"future", "", `{"a":`, "stop"},
		{"future", "", `{}`, "tool_calls"},
		{"failed", "", `{}`, "stop"},
	} {
		response := &ResponsesResponse{Status: tc.status, Output: []ResponsesOutput{{Type: "function_call", CallID: "c", Name: "f", Arguments: tc.arguments}}}
		if tc.reason != "" {
			response.IncompleteDetails = &ResponsesIncompleteDetails{Reason: tc.reason}
		}
		chat := ResponsesToChatCompletions(response, "m")
		require.Equal(t, tc.want, chat.Choices[0].FinishReason, "%s/%s %s", tc.status, tc.reason, tc.arguments)
		require.Equal(t, tc.arguments, chat.Choices[0].Message.ToolCalls[0].Function.Arguments)
		if tc.status == "failed" {
			require.Error(t, ValidateResponsesForChat(response))
			continue
		}
		require.NoError(t, ValidateResponsesForChat(response))
		state := NewResponsesEventToChatState()
		chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.incomplete", Response: response}, state)
		require.NoError(t, state.Err())
		require.Equal(t, tc.want, *chunks[len(chunks)-1].Choices[0].FinishReason, "stream %s/%s %s", tc.status, tc.reason, tc.arguments)
	}
}

func TestReviewM2InvalidTextFormatIsOmitted(t *testing.T) {
	for _, tc := range []struct{ fields, verbosity string }{
		{`"text":{"format":{"type":"bogus"}}`, ""},
		{`"text":{"format":"json"}`, ""},
		{`"text":{"format":{"type":"json_schema"},"verbosity":"low"}`, "low"},
	} {
		var req ChatCompletionsRequest
		require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],`+tc.fields+`}`), &req))
		for _, record := range []bool{false, true} {
			var notices []ConversionNotice
			opts := ConversionOptions{}
			if record {
				opts.Record = func(n ConversionNotice) { notices = append(notices, n) }
			}
			out, err := ChatCompletionsToResponsesWithOptions(&req, opts)
			require.NoError(t, err, tc.fields)
			if tc.verbosity == "" {
				require.Nil(t, out.Text)
			} else {
				require.Empty(t, out.Text.Format)
				require.Equal(t, tc.verbosity, out.Text.Verbosity)
			}
			if record {
				require.Equal(t, []ConversionNotice{{Field: "text.format", Action: "omit"}}, notices)
			}
		}
	}
	var req ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"bogus"}}`), &req))
	_, err := ChatCompletionsToResponses(&req)
	var conversionErr *UnsupportedConversionError
	require.ErrorAs(t, err, &conversionErr, "an invalid response_format keeps the baseline error")
	require.Equal(t, "response_format.type", conversionErr.Field)
}
