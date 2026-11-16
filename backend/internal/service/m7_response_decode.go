package service

import (
	"encoding/json"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

// A malformed consumed business field cannot be discarded as an extension.
func m7ResponseDecodeError(payload string, terminal OpenAIResponsesTerminal, decodeErr error) error {
	known := terminal.Seen
	if gjson.Valid(payload) {
		switch gjson.Get(payload, "type").String() {
		case "response.created", "response.output_item.added", "response.output_item.done",
			"response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done",
			"response.content_part.added", "response.content_part.done",
			"response.function_call_arguments.delta", "response.function_call_arguments.done",
			"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done",
			"response.reasoning_text.delta", "response.reasoning_text.done",
			"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
			"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
			known = true
		}
	}
	if !known {
		return nil
	}
	field := "response"
	var typed *json.UnmarshalTypeError
	if errors.As(decodeErr, &typed) && typed.Field != "" {
		field = typed.Field
	}
	return &apicompat.UpstreamConversionError{Field: field, Reason: "invalid upstream business event", SourceProtocol: "responses"}
}
