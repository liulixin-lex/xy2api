package apicompat

func bridgeResponsesTerminal(source, reason string, tools, refusal bool, record func(ConversionNotice)) (string, *ResponsesIncompleteDetails, error) {
	status, projected, err := bridgeProjectTerminal(source, "responses", reason, tools, refusal, record)
	if err != nil {
		return "", nil, err
	}
	if status == "incomplete" {
		return status, &ResponsesIncompleteDetails{Reason: projected}, nil
	}
	return status, nil, nil
}

func bridgeAnthropicHasTools(blocks []AnthropicContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

func bridgeOutputsHaveTools(outputs []ResponsesOutput) bool {
	for _, item := range outputs {
		if item.Type == "function_call" {
			return true
		}
	}
	return false
}

func appendBridgeBufferedRefusal(out *ResponsesResponse, text string) {
	if text == "" {
		return
	}
	for i := range out.Output {
		if out.Output[i].Type == "message" {
			out.Output[i].Content = append(out.Output[i].Content, ResponsesContentPart{Type: "refusal", Refusal: text})
			return
		}
	}
	out.Output = append(out.Output, ResponsesOutput{Type: "message", ID: generateItemID(), Role: "assistant", Status: "completed", Content: []ResponsesContentPart{{Type: "refusal", Refusal: text}}})
}

func bridgeProjectTerminal(source, target, reason string, tools, refusal bool, record func(ConversionNotice)) (string, string, error) {
	status, projected, mapped, err := ProjectTerminalWithMapping(source, target, "", reason, tools, refusal)
	if err == nil && mapped && record != nil {
		field := "finish_reason"
		if source == "anthropic" {
			field = "stop_reason"
		}
		record(ConversionNotice{Field: field, Action: "map"})
	}
	return status, projected, err
}

// SetConversionRecorder supplies the request's notice sink to the Chat stream
// bridge. It does not alter the default entry's protocol behavior.
func (state *ChatCompletionsToResponsesStreamState) SetConversionRecorder(record func(ConversionNotice)) {
	state.bridgeRecord = record
}
func (state *ChatCompletionsToAnthropicStreamState) SetConversionRecorder(record func(ConversionNotice)) {
	state.bridgeRecord = record
}
