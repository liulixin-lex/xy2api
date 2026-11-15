package apicompat

import "fmt"

func responsesToolOpaque(item ResponsesOutput) bool {
	return len(item.encryptedContentRaw) > 0 || item.EncryptedContent != "" || len(item.EncryptedFunctionArgs) > 0
}

func responsesToolArguments(item ResponsesOutput, complete bool) string {
	if item.Type == "custom_tool_call" {
		return item.Input
	}
	if complete && item.Arguments == "" && !responsesToolOpaque(item) {
		return "{}"
	}
	return item.Arguments
}

// ValidateResponsesForChat checks generated data before a non-streaming Chat
// projection. A truncated tool remains partial even when its bytes form JSON.
func ValidateResponsesForChat(resp *ResponsesResponse) error {
	return ValidateResponsesForChatWithOptions(resp, ConversionOptions{})
}

// ValidateResponsesForChatWithOptions records unknown terminal mappings once.
// The caller must first authenticate the response terminal, as for the old entry.
func ValidateResponsesForChatWithOptions(resp *ResponsesResponse, opts ConversionOptions) error {
	if resp == nil {
		return toolTrackingError("response", "missing response")
	}
	status, reason := responsesChatTerminal(resp.Status, resp.IncompleteDetails)
	hasTools, hasRefusal := false, false
	for _, item := range resp.Output {
		if item.Type == "function_call" || item.Type == "custom_tool_call" {
			hasTools = true
		}
		for _, part := range item.Content {
			hasRefusal = hasRefusal || (part.Type == "refusal" && part.Refusal != "")
		}
	}
	_, _, mapped, err := ProjectTerminalWithMapping("responses", "chat", status, reason, hasTools, hasRefusal)
	if err != nil {
		return err
	}
	if err := validateResponsesChatTools(resp); err != nil {
		return err
	}
	if mapped && opts.Record != nil {
		opts.Record(ConversionNotice{Field: "finish_reason", Action: "map"})
	}
	return nil
}

// Preserve the existing Chat projection for a qualified terminal lacking status
// or an incomplete terminal without a reason. The reader still proves completion.
func responsesChatTerminal(status string, details *ResponsesIncompleteDetails) (string, string) {
	reason := ""
	if details != nil {
		reason = details.Reason
	}
	if status == "" || status == "incomplete" && reason == "" {
		status = "completed"
	}
	return status, reason
}

func validateResponsesChatTools(resp *ResponsesResponse) error {
	if resp.Status != "completed" {
		return nil
	}
	for index, item := range resp.Output {
		if item.Type != "function_call" && item.Type != "custom_tool_call" {
			continue
		}
		if item.Status != "" && item.Status != "completed" {
			return toolTrackingError(fmt.Sprintf("output[%d].status", index), "tool call is not complete")
		}
		kind := "function"
		if item.Type == "custom_tool_call" {
			kind = "custom"
		}
		if err := ValidateToolArguments(kind, responsesToolArguments(item, true), false, responsesToolOpaque(item)); err != nil {
			return toolTrackingError(fmt.Sprintf("output[%d].arguments", index), "expected complete tool arguments")
		}
	}
	return nil
}

func resToChatValidateTools(resp *ResponsesResponse, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if resp == nil || resp.Status != "completed" {
		return nil
	}
	if err := ValidateResponsesForChat(resp); err != nil {
		state.err = err
		return nil
	}
	snapshot := &ResponsesResponse{Status: resp.Status}
	for _, call := range state.tools.calls {
		item := call.Item
		// A valid response terminal completes calls previously announced as in_progress.
		if item.Status == "in_progress" {
			item.Status = "completed"
		}
		if item.Type == "custom_tool_call" {
			item.Input = call.Sent
		} else {
			item.Arguments = call.Sent
		}
		snapshot.Output = append(snapshot.Output, item)
	}
	if err := ValidateResponsesForChat(snapshot); err != nil {
		state.err = err
		return nil
	}
	var chunks []ChatCompletionsChunk
	for _, call := range state.tools.calls {
		value := responsesToolArguments(call.Item, true)
		if call.Sent == "" && value == "{}" {
			suffix, err := state.tools.Snapshot(call, value, true)
			if err != nil {
				state.err = err
				return nil
			}
			chunks = append(chunks, resToChatToolArgumentsChunk(call, suffix, state))
		}
	}
	return chunks
}
