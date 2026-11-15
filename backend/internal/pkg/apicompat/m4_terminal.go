package apicompat

func bridgeResponsesTerminal(source, reason string, tools, refusal bool) (string, *ResponsesIncompleteDetails, error) {
	status, projected, err := ProjectTerminal(source, "responses", "", reason, tools, refusal)
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
