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
	if part.Detail != "" {
		part.Detail = ""
		if opts.Record != nil {
			opts.Record(ConversionNotice{Field: field + ".detail", Action: "omit"})
		}
	}
	block, err := responsesMediaToAnthropic(part)
	var unsupported *UnsupportedConversionError
	if errors.As(err, &unsupported) {
		return block, unsupportedConversion(field+strings.TrimPrefix(unsupported.Field, "content"), unsupported.Reason)
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
		source.Tools, err = responsesToolsToChatTools(copyReq.Tools)
		if err != nil {
			return nil, err
		}
		source.Functions = nil
		copyReq.anthropicChatSource = &source
	}
	return &copyReq, nil
}
