package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

type bridgeChatPart struct {
	output     ResponsesOutput
	index      int
	kind, text string
}

func hasThinkingPart(parts []ChatOutputPart) bool {
	for _, part := range parts {
		if part.Kind == "thinking" {
			return true
		}
	}
	return false
}

func bridgeBufferedParts(message ChatMessage) ([]ChatOutputPart, bool, error) {
	parts, err := DecodeChatOutputContent(message.Content)
	raw := bytesTrimSpace(message.Content)
	return parts, len(raw) > 0 && raw[0] == '[', err
}

func applyBridgeBufferedParts(out *ResponsesResponse, resp *ChatCompletionsResponse) error {
	if resp == nil || len(resp.Choices) == 0 {
		return nil
	}
	message := resp.Choices[0].Message
	parts, array, err := bridgeBufferedParts(message)
	if err != nil {
		return bridgeConversionError("choices.message.content", err)
	}
	if !array {
		return nil
	}
	var outputs []ResponsesOutput
	if !hasThinkingPart(parts) && message.reasoningText() != "" {
		outputs = append(outputs, ResponsesOutput{Type: "reasoning", ID: generateReasoningItemID(), Summary: []ResponsesSummary{{Type: "summary_text", Text: message.reasoningText()}}})
	}
	for _, part := range parts {
		if part.Kind == "thinking" {
			outputs = append(outputs, ResponsesOutput{Type: "reasoning", ID: generateReasoningItemID(), Summary: []ResponsesSummary{{Type: "summary_text", Text: part.Text}}})
		} else {
			outputs = append(outputs, ResponsesOutput{Type: "message", ID: generateItemID(), Role: "assistant", Status: "completed", Content: []ResponsesContentPart{{Type: "output_text", Text: part.Text}}})
		}
	}
	for _, item := range out.Output {
		if item.Type != "reasoning" && item.Type != "message" {
			outputs = append(outputs, item)
		}
	}
	out.Output = outputs
	return nil
}

func ChatCompletionsResponseToAnthropicWithBridgeOptions(resp *ChatCompletionsResponse, model string, opts ConversionOptions) (*AnthropicResponse, error) {
	if bridgeEnabled(opts.Bridge) {
		if err := validateBridgeBufferedTools(resp, opts.Bridge, nil, false, true); err != nil {
			return nil, err
		}
	}
	out := ChatCompletionsResponseToAnthropic(resp, model)
	if !bridgeEnabled(opts.Bridge) || resp == nil || len(resp.Choices) == 0 {
		return out, nil
	}
	message := resp.Choices[0].Message
	parts, array, err := bridgeBufferedParts(message)
	if err != nil {
		return nil, bridgeConversionError("choices.message.content", err)
	}
	if array {
		var blocks []AnthropicContentBlock
		if !hasThinkingPart(parts) && message.reasoningText() != "" {
			blocks = append(blocks, AnthropicContentBlock{Type: "thinking", Thinking: message.reasoningText()})
		}
		for _, part := range parts {
			if part.Kind == "thinking" {
				blocks = append(blocks, AnthropicContentBlock{Type: "thinking", Thinking: part.Text})
			} else {
				blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: part.Text})
			}
		}
		for _, block := range out.Content {
			if block.Type != "text" && block.Type != "thinking" {
				blocks = append(blocks, block)
			}
		}
		out.Content = blocks
	}
	blocks := out.Content[:0]
	for _, block := range out.Content {
		if block.Type != "thinking" || bridgeNonblankThinking(block.Thinking) {
			blocks = append(blocks, block)
		}
	}
	out.Content = blocks
	if message.Refusal != "" {
		out.Content = append(out.Content, AnthropicContentBlock{Type: "text", Text: message.Refusal})
	}
	_, reason, err := ProjectTerminal("chat", "anthropic", "", resp.Choices[0].FinishReason, len(message.ToolCalls) > 0 && resp.Choices[0].FinishReason != "length", message.Refusal != "")
	if err != nil {
		return nil, err
	}
	out.StopReason = AnthropicStopReasonPtr(reason)
	return out, nil
}

func ChatCompletionsChunkToResponsesEventsWithBridgeOptions(raw []byte, state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state == nil || state.Err() != nil {
		return nil
	}
	if !bridgeEnabled(state.Bridge) {
		var chunk ChatCompletionsChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			state.bridgeErr = err
			return nil
		}
		return ChatCompletionsChunkToResponsesEvents(&chunk, state)
	}
	chunk, arrays, err := DecodeChatBridgeChunk(raw)
	if err != nil {
		state.bridgeErr = bridgeConversionError("choices.delta.content", err)
		return nil
	}
	if len(arrays) == 0 && len(state.bridgeParts) == 0 {
		return ChatCompletionsChunkToResponsesEvents(chunk, state)
	}
	header := *chunk
	header.Choices = nil
	events := ChatCompletionsChunkToResponsesEvents(&header, state)
	for position, choice := range chunk.Choices {
		if choice.Index != 0 {
			state.bridgeErr = bridgeConversionError("choices.index", fmt.Errorf("unsupported choice index %d", choice.Index))
			return events
		}
		var parts []ChatOutputPart
		if content, exists := arrays[position]; exists {
			parts, err = DecodeChatOutputContent(content)
			if err != nil {
				state.bridgeErr = bridgeConversionError("choices.delta.content", err)
				return events
			}
		} else if choice.Delta.Content != nil {
			parts = []ChatOutputPart{{Kind: "text", Text: *choice.Delta.Content}}
		}
		if !hasThinkingPart(parts) && choice.Delta.reasoningText() != nil {
			reasoning := *chunk
			reasoning.Choices = []ChatChunkChoice{{Delta: ChatDelta{ReasoningContent: choice.Delta.reasoningText()}}}
			events = append(events, ChatCompletionsChunkToResponsesEvents(&reasoning, state)...)
		}
		if len(parts) > 0 {
			events = append(events, closeChatReasoningItem(state)...)
			events = append(events, closeBridgeOriginalMessage(state)...)
			for _, part := range parts {
				events = append(events, appendBridgeChatPart(state, part)...)
			}
		}
		tail := *chunk
		tail.Choices = []ChatChunkChoice{{Index: choice.Index, Delta: ChatDelta{ToolCalls: choice.Delta.ToolCalls, Refusal: choice.Delta.Refusal}, FinishReason: choice.FinishReason}}
		if len(choice.Delta.ToolCalls) > 0 {
			events = append(events, closeBridgeChatPart(state)...)
		}
		events = append(events, ChatCompletionsChunkToResponsesEvents(&tail, state)...)
	}
	return events
}

func ChatCompletionsChunkToAnthropicEventsWithBridgeOptions(raw []byte, state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state == nil || state.Err() != nil {
		return nil
	}
	if !bridgeEnabled(state.Bridge) {
		var chunk ChatCompletionsChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			state.bridgeErr = err
			return nil
		}
		return ChatCompletionsChunkToAnthropicEvents(&chunk, state)
	}
	chunk, arrays, err := DecodeChatBridgeChunk(raw)
	if err != nil {
		state.bridgeErr = bridgeConversionError("choices.delta.content", err)
		return nil
	}
	parts := make(map[int][]ChatOutputPart)
	for index, content := range arrays {
		parts[index], err = DecodeChatOutputContent(content)
		if err != nil {
			state.bridgeErr = bridgeConversionError("choices.delta.content", err)
			return nil
		}
	}
	return chatCompletionsChunkToAnthropicEventsWithParts(chunk, state, parts)
}

func appendBridgeChatPart(state *ChatCompletionsToResponsesStreamState, part ChatOutputPart) []ResponsesStreamEvent {
	if part.Text == "" {
		return nil
	}
	var events []ResponsesStreamEvent
	if state.bridgePartOpen && state.bridgeParts[len(state.bridgeParts)-1].kind != part.Kind {
		events = append(events, closeBridgeChatPart(state)...)
	}
	if !state.bridgePartOpen {
		item := ResponsesOutput{ID: generateItemID(), Type: "message", Role: "assistant", Status: "in_progress"}
		if part.Kind == "thinking" {
			item = ResponsesOutput{ID: generateReasoningItemID(), Type: "reasoning", Status: "in_progress"}
		}
		entry := bridgeChatPart{output: item, index: state.allocOutputIndex(), kind: part.Kind}
		state.bridgeParts = append(state.bridgeParts, entry)
		state.bridgePartOpen = true
		events = append(events, chatToResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{OutputIndex: entry.index, Item: &item}))
		typ, partType := "response.content_part.added", "output_text"
		if part.Kind == "refusal" {
			partType = "refusal"
		}
		if part.Kind == "thinking" {
			typ, partType = "response.reasoning_summary_part.added", "summary_text"
		}
		events = append(events, chatToResponsesEvent(state, typ, &ResponsesStreamEvent{OutputIndex: entry.index, ItemID: item.ID, Part: &ResponsesContentPart{Type: partType}}))
	}
	entry := &state.bridgeParts[len(state.bridgeParts)-1]
	entry.text += part.Text
	typ := "response.output_text.delta"
	if part.Kind == "refusal" {
		typ = "response.refusal.delta"
	}
	if part.Kind == "thinking" {
		typ = "response.reasoning_summary_text.delta"
	}
	return append(events, chatToResponsesEvent(state, typ, &ResponsesStreamEvent{OutputIndex: entry.index, ItemID: entry.output.ID, Delta: part.Text}))
}

func closeBridgeChatPart(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if !state.bridgePartOpen {
		return nil
	}
	state.bridgePartOpen = false
	entry := &state.bridgeParts[len(state.bridgeParts)-1]
	entry.output.Status = "completed"
	textType, partType, contentType := "response.output_text.done", "response.content_part.done", "output_text"
	if entry.kind == "thinking" {
		textType, partType, contentType = "response.reasoning_summary_text.done", "response.reasoning_summary_part.done", "summary_text"
		entry.output.Summary = []ResponsesSummary{{Type: "summary_text", Text: entry.text}}
	} else {
		entry.output.Content = []ResponsesContentPart{{Type: "output_text", Text: entry.text}}
	}
	if entry.kind == "refusal" {
		textType, contentType = "response.refusal.done", "refusal"
		entry.output.Content = []ResponsesContentPart{{Type: "refusal", Refusal: entry.text}}
	}
	item := entry.output
	done := &ResponsesStreamEvent{OutputIndex: entry.index, ItemID: item.ID, Text: entry.text}
	part := &ResponsesContentPart{Type: contentType, Text: entry.text}
	if entry.kind == "refusal" {
		done.Text = ""
		done.Refusal = entry.text
		part.Text = ""
		part.Refusal = entry.text
	}
	return []ResponsesStreamEvent{
		chatToResponsesEvent(state, textType, done),
		chatToResponsesEvent(state, partType, &ResponsesStreamEvent{OutputIndex: entry.index, ItemID: item.ID, Part: part}),
		chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{OutputIndex: entry.index, Item: &item}),
	}
}

func closeBridgeOriginalMessage(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.MessageItemID == "" || state.bridgeMessageClosed {
		return nil
	}
	state.bridgeMessageClosed = true
	text := state.Text.String()
	item := ResponsesOutput{Type: "message", ID: state.MessageItemID, Role: "assistant", Status: "completed", Content: []ResponsesContentPart{{Type: "output_text", Text: text}}}
	return []ResponsesStreamEvent{
		chatToResponsesEvent(state, "response.output_text.done", &ResponsesStreamEvent{OutputIndex: state.MessageIndex, ItemID: item.ID, Text: text}),
		chatToResponsesEvent(state, "response.content_part.done", &ResponsesStreamEvent{OutputIndex: state.MessageIndex, ItemID: item.ID, Part: &ResponsesContentPart{Type: "output_text", Text: text}}),
		chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{OutputIndex: state.MessageIndex, Item: &item}),
	}
}

func bridgeNonblankThinking(text string) bool { return strings.TrimSpace(text) != "" }
