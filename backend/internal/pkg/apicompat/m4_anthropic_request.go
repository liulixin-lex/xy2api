package apicompat

import (
	"encoding/json"
	"errors"
	"strings"
)

func bridgeMediaToAnthropic(part ResponsesContentPart, opts *ConversionOptions, field string) (AnthropicContentBlock, error) {
	if opts == nil {
		return responsesMediaToAnthropic(part)
	}
	block, err := responsesMediaToAnthropicWithOptions(part, *opts, field+".detail")
	var unsupported *UnsupportedConversionError
	if errors.As(err, &unsupported) {
		if strings.HasPrefix(unsupported.Field, "content") {
			return block, unsupportedConversion(field+strings.TrimPrefix(unsupported.Field, "content"), unsupported.Reason)
		}
		return block, err
	}
	return block, err
}

// Keep private policy inputs (aliases, original Chat parameters, target marker)
// while lowering only declarations, choice and history on a request copy.
func prepareBridgeAnthropicRequest(req *ResponsesRequest, opts ConversionOptions) (*ResponsesRequest, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if _, _, err = AdaptResponsesClientToolsWithBridgeOptions(value, opts); err != nil {
		return nil, err
	}
	raw, err = json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var adapted ResponsesRequest
	if err = json.Unmarshal(raw, &adapted); err != nil {
		return nil, err
	}
	copyReq := *req
	copyReq.Tools, copyReq.Input, copyReq.ToolChoice = adapted.Tools, adapted.Input, adapted.ToolChoice
	if req.anthropicChatSource != nil {
		source := *req.anthropicChatSource
		source.Tools = append([]ChatTool(nil), source.Tools...)
		alias := func(name string) string {
			if mapped, ok := opts.Bridge.ToolNames.Forward[ToolNameIdentity{Kind: "function", Name: name}]; ok {
				return mapped
			}
			return name
		}
		for i := range source.Tools {
			if source.Tools[i].Function != nil {
				function := *source.Tools[i].Function
				function.Name = alias(function.Name)
				source.Tools[i].Function = &function
			}
		}
		source.Functions = append([]ChatFunction(nil), source.Functions...)
		for i := range source.Functions {
			source.Functions[i].Name = alias(source.Functions[i].Name)
		}
		copyReq.anthropicChatSource = &source
	}
	return &copyReq, nil
}

func validateLegacyAnthropicDetails(input json.RawMessage) error {
	var items []map[string]json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return nil
	}
	for _, item := range items {
		for _, field := range []string{"content", "output"} {
			var parts []ResponsesContentPart
			if json.Unmarshal(item[field], &parts) != nil {
				continue
			}
			for _, part := range parts {
				if (part.Type == "input_image" || part.Type == "input_file") && part.Detail != "" && part.Detail != "auto" {
					return unsupportedConversion("content.detail", "Anthropic has no image/file detail equivalent")
				}
			}
		}
	}
	return nil
}
