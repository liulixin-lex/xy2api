package apicompat

import (
	"encoding/json"
	"fmt"
)

// UnsupportedConversionError identifies input that cannot be converted without
// losing client intent. Callers can use errors.As, including through json.Marshal.
// Field is a protocol field path; Reason never includes user content.
type UnsupportedConversionError struct {
	Field  string
	Reason string
}

func (e *UnsupportedConversionError) Error() string {
	return fmt.Sprintf("unsupported protocol conversion at %s: %s", e.Field, e.Reason)
}

func unsupportedConversion(field, reason string) error {
	return &UnsupportedConversionError{Field: field, Reason: reason}
}

func parseStopSequences(raw json.RawMessage) ([]string, error) {
	if len(normalizedRawJSON(raw)) == 0 {
		return nil, nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		if single == "" {
			return nil, unsupportedConversion("stop", "stop strings must not be empty")
		}
		return []string{single}, nil
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil, unsupportedConversion("stop", "expected a string or an array of strings")
	}
	stops := make([]string, 0, len(values))
	for _, value := range values {
		var stop string
		if json.Unmarshal(value, &stop) != nil || stop == "" {
			return nil, unsupportedConversion("stop", "stop strings must not be empty or null")
		}
		stops = append(stops, stop)
	}
	return stops, nil
}

func validateTokenLimit(field string, value *int) error {
	if value != nil && *value <= 0 {
		return unsupportedConversion(field, "an explicit token limit must be positive")
	}
	return nil
}

func applyAnthropicOutputControls(req *AnthropicRequest, out *ResponsesRequest) error {
	if req.MaxTokens < 0 {
		return unsupportedConversion("max_tokens", "token limit must be positive")
	}
	if len(req.ToolChoice) > 0 {
		var choice struct {
			DisableParallel *bool `json:"disable_parallel_tool_use"`
		}
		if json.Unmarshal(req.ToolChoice, &choice) != nil {
			return unsupportedConversion("tool_choice", "invalid parallel tool control")
		}
		if choice.DisableParallel != nil {
			parallel := !*choice.DisableParallel
			out.ParallelToolCalls = &parallel
		}
	}
	if req.OutputConfig != nil && len(normalizedRawJSON(req.OutputConfig.Format)) > 0 {
		format, ok := rawJSONObject(req.OutputConfig.Format)
		if !ok || rawString(format["type"]) != "json_schema" || len(format) != 2 {
			return unsupportedConversion("output_config.format", "expected type and schema fields")
		}
		format["name"] = rawJSONString("response")
		format["strict"] = json.RawMessage("true")
		raw, _ := json.Marshal(format)
		if err := validateResponseFormat(raw, false); err != nil {
			return err
		}
		out.Text.Format = raw
	}
	return nil
}

// MarshalJSON rejects bridge-only stop sequences at the native Responses wire
// boundary. Chat and Anthropic can represent stops, but Responses cannot.
func (r ResponsesRequest) MarshalJSON() ([]byte, error) {
	if stops, err := parseStopSequences(r.bridgeStop); err != nil {
		return nil, err
	} else if len(stops) > 0 {
		return nil, unsupportedConversion("stop", "the Responses wire protocol has no stop-sequence equivalent")
	}
	type wire ResponsesRequest
	return json.Marshal(wire(r))
}
