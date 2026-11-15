package apicompat

import (
	"fmt"
	"strings"
)

type bridgeAnthropicEntry struct {
	kind, text string
	slot       int
}
type bridgeAnthropicFlow struct {
	calls     map[int]*ChatToolCall
	queue     []bridgeAnthropicEntry
	toolStage bool
	active    int
}

func consumeBridgeAnthropicChunk(chunk *ChatCompletionsChunk, state *ChatCompletionsToAnthropicStreamState, parts map[int][]ChatOutputPart) []AnthropicStreamEvent {
	if state.bridgeFlow == nil {
		state.bridgeFlow = &bridgeAnthropicFlow{calls: make(map[int]*ChatToolCall), active: -1}
	}
	flow := state.bridgeFlow
	events := ensureCCAnthropicMessageStart(state)
	appendText := func(kind, text string) {
		if text == "" {
			return
		}
		if flow.toolStage {
			flow.queue = append(flow.queue, bridgeAnthropicEntry{kind: kind, text: text})
			return
		}
		events = append(events, emitBridgeAnthropicText(state, kind, text)...)
	}
	for position, choice := range chunk.Choices {
		if choice.Index != 0 {
			state.bridgeErr = bridgeConversionError("choices.index", fmt.Errorf("unsupported choice index %d", choice.Index))
			return events
		}
		if reasoning := choice.Delta.reasoningText(); reasoning != nil && !hasThinkingPart(parts[position]) {
			appendText("thinking", *reasoning)
		}
		for _, part := range parts[position] {
			appendText(part.Kind, part.Text)
		}
		if choice.Delta.Content != nil {
			appendText("text", *choice.Delta.Content)
		}
		if choice.Delta.Refusal != nil {
			appendText("text", *choice.Delta.Refusal)
			state.bridgeHasRefusal = true
		}
		for position, fragment := range choice.Delta.ToolCalls {
			slot, err := ResolveChatToolSlot(choice.Index, position, fragment.Index, fragment.ID, &state.bridgeSlots)
			if err != nil {
				state.bridgeErr = bridgeConversionError("tool_calls.index", err)
				return events
			}
			call := flow.calls[slot]
			if call == nil {
				call = &ChatToolCall{Type: "function", Index: &slot}
				flow.calls[slot] = call
				state.HasToolCall = true
				if !flow.toolStage {
					events = append(events, closeCCAnthropicBlock(state)...)
					state.bridgePendingThinking = ""
					flow.toolStage = true
					flow.active = slot
				} else {
					flow.queue = append(flow.queue, bridgeAnthropicEntry{kind: "tool", slot: slot})
				}
			}
			if state.toolAnnounced[slot] && ((fragment.ID != "" && fragment.ID != call.ID) || (fragment.Function.Name != "" && fragment.Function.Name != call.Function.Name)) {
				state.bridgeErr = bridgeConversionError("tool_calls", fmt.Errorf("announced tool identity changed"))
				return events
			}
			if fragment.ID != "" {
				call.ID = fragment.ID
			}
			if fragment.Function.Name != "" {
				call.Function.Name = fragment.Function.Name
			}
			call.Function.Arguments += fragment.Function.Arguments
			if slot != flow.active {
				continue
			}
			if !state.toolAnnounced[slot] {
				if call.ID != "" && call.Function.Name != "" {
					state.pendingToolArgs[slot] = call.Function.Arguments
					events = append(events, announceCCAnthropicToolBlock(state, slot, call.ID, call.Function.Name)...)
				}
			} else if fragment.Function.Arguments != "" {
				state.CurrentToolHadDelta = true
				events = append(events, ccAnthropicDelta(state, &AnthropicDelta{Type: "input_json_delta", PartialJSON: fragment.Function.Arguments})...)
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			state.FinishReason = *choice.FinishReason
		}
	}
	return events
}

func emitBridgeAnthropicText(state *ChatCompletionsToAnthropicStreamState, kind, text string) []AnthropicStreamEvent {
	if text == "" {
		return nil
	}
	if kind == "thinking" {
		if !state.ContentBlockOpen || state.CurrentBlockType != "thinking" {
			state.bridgePendingThinking += text
			if !bridgeNonblankThinking(state.bridgePendingThinking) {
				return nil
			}
			text = state.bridgePendingThinking
			state.bridgePendingThinking = ""
		}
		events := ensureCCAnthropicThinkingBlock(state)
		return append(events, ccAnthropicDelta(state, &AnthropicDelta{Type: "thinking_delta", Thinking: text})...)
	}
	state.bridgePendingThinking = ""
	events := ensureCCAnthropicTextBlock(state)
	return append(events, ccAnthropicDelta(state, &AnthropicDelta{Type: "text_delta", Text: text})...)
}

func finalizeBridgeAnthropicBlocks(state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	flow := state.bridgeFlow
	if flow == nil || !flow.toolStage {
		return nil
	}
	if state.FinishReason != "stop" && state.FinishReason != "tool_calls" && state.FinishReason != "function_call" {
		state.bridgeErr = bridgeConversionError("finish_reason", fmt.Errorf("tool input has no complete finish"))
		return nil
	}
	for _, call := range flow.calls {
		arguments := call.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		if err := ValidateToolArguments("function", arguments, true, false); err != nil {
			state.bridgeErr = err
			return nil
		}
	}
	var events []AnthropicStreamEvent
	publish := func(slot int) {
		call := flow.calls[slot]
		if !state.toolAnnounced[slot] {
			if call.ID == "" {
				call.ID = generateItemID()
			}
			state.pendingToolArgs[slot] = call.Function.Arguments
			events = append(events, announceCCAnthropicToolBlock(state, slot, call.ID, call.Function.Name)...)
		}
	}
	publish(flow.active)
	events = append(events, closeCCAnthropicBlock(state)...)
	for _, entry := range flow.queue {
		if entry.kind == "tool" {
			state.bridgePendingThinking = ""
			events = append(events, closeCCAnthropicBlock(state)...)
			publish(entry.slot)
			events = append(events, closeCCAnthropicBlock(state)...)
		} else {
			events = append(events, emitBridgeAnthropicText(state, entry.kind, entry.text)...)
		}
	}
	flow.queue = nil
	return events
}
