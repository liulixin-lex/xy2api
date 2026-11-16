package apicompat

func bridgeLengthToolGate(out *ResponsesResponse, resp *ChatCompletionsResponse, bridge *BridgeOptions, custom map[string]bool, search bool) {
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].FinishReason != "length" {
		return
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) == 0 {
		return
	}
	retained := out.Output[:0]
	for _, item := range out.Output {
		if item.Type == "function_call" || item.Type == "custom_tool_call" || item.Type == "tool_search_call" {
			continue
		}
		if item.Type == "message" && len(item.Content) == 1 && item.Content[0].Text == "" && item.Content[0].Refusal == "" {
			continue
		}
		retained = append(retained, item)
	}
	for _, call := range calls {
		kind := bridgeOutputToolKind(call.Function.Name, bridge, custom, search)
		item := ResponsesOutput{Type: "function_call", ID: generateItemID(), CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments, Status: "incomplete"}
		switch kind {
		case "custom":
			item.Type = "custom_tool_call"
			item.Input = extractCustomToolCallInput(call.Function.Arguments)
			item.Arguments = ""
		case "tool_search":
			item.Type = "tool_search_call"
			item.Name = ""
		}
		restoreBridgeOutputIdentity(&item, bridge.ToolNames)
		retained = append(retained, item)
	}
	out.Output = retained
}
