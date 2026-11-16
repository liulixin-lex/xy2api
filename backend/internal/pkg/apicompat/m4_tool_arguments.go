package apicompat

import (
	"encoding/json"
	"strings"
)

func bridgeOutputToolKind(name string, bridge *BridgeOptions, custom map[string]bool, search bool) string {
	if bridgeEnabled(bridge) && bridge.ToolNames != nil {
		if id, ok := bridge.ToolNames.Reverse[name]; ok {
			return id.Kind
		}
	}
	if custom[name] {
		return "custom"
	}
	if search && name == toolSearchProxyName {
		return "tool_search"
	}
	return "function"
}

func validateBridgeToolArguments(kind, arguments string, requireObject bool) error {
	if kind == "tool_search" {
		// tool_search has object arguments on the wire; it is not a third
		// ValidateToolArguments kind (foundation contract B7).
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(arguments), &object) != nil || object == nil {
			return &UpstreamConversionError{Field: "arguments", Reason: "tool_search requires a complete JSON object", SourceProtocol: "chat"}
		}
		return nil
	}
	return ValidateToolArguments(kind, arguments, requireObject, false)
}

func validateBridgeBufferedTools(resp *ChatCompletionsResponse, bridge *BridgeOptions, custom map[string]bool, search, requireObject bool) error {
	if resp == nil {
		return nil
	}
	for _, choice := range resp.Choices {
		if choice.Index != 0 {
			return &UpstreamConversionError{Field: "choices.index", Reason: "only choice zero is supported", SourceProtocol: "chat"}
		}
		if choice.FinishReason == "length" || (requireObject && choice.FinishReason == "content_filter") {
			continue
		}
		for _, call := range choice.Message.ToolCalls {
			kind := bridgeOutputToolKind(call.Function.Name, bridge, custom, search)
			arguments := call.Function.Arguments
			if strings.TrimSpace(arguments) == "" && kind != "custom" {
				arguments = "{}"
			}
			if err := validateBridgeToolArguments(kind, arguments, requireObject); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBridgeStreamTools(state *ChatCompletionsToResponsesStreamState) error {
	if state.Err() != nil {
		return state.Err()
	}
	if len(state.ToolCalls) == 0 || state.FinishReason == "length" {
		return nil
	}
	for _, call := range state.ToolCalls {
		if call == nil {
			continue
		}
		kind := bridgeOutputToolKind(call.Function.Name, state.Bridge, state.CustomTools, state.ToolSearchDeclared)
		arguments := call.Function.Arguments
		if strings.TrimSpace(arguments) == "" && kind != "custom" {
			arguments = "{}"
		}
		if err := validateBridgeToolArguments(kind, arguments, false); err != nil {
			return err
		}
	}
	return nil
}
