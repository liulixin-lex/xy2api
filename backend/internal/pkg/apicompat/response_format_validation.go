package apicompat

import "encoding/json"

// Validate only the format wrapper. Keep schema bytes intact, including numeric
// precision, annotations and nested constraints; the provider validates schemas.
func validateResponseFormat(raw json.RawMessage, chat bool) error {
	raw = normalizedRawJSON(raw)
	if len(raw) == 0 {
		return nil
	}
	field := "text.format"
	if chat {
		field = "response_format"
	}
	obj, ok := rawJSONObject(raw)
	if !ok || obj == nil {
		return unsupportedConversion(field, "expected a format object")
	}
	switch rawString(obj["type"]) {
	case "text", "json_object":
		if len(obj) != 1 {
			return unsupportedConversion(field, "unexpected format fields")
		}
		return nil
	case "json_schema":
		if nested, exists := obj["json_schema"]; exists {
			if len(obj) != 2 {
				return unsupportedConversion(field, "unexpected schema wrapper fields")
			}
			obj, ok = rawJSONObject(nested)
			if !ok || obj == nil {
				return unsupportedConversion(field+".json_schema", "expected a schema descriptor")
			}
			if _, exists := obj["type"]; exists {
				return unsupportedConversion(field+".json_schema.type", "schema descriptors do not have a type field")
			}
		} else if chat {
			return unsupportedConversion(field+".json_schema", "missing schema descriptor")
		}
		for key := range obj {
			switch key {
			case "type", "schema", "name", "description", "strict":
			default:
				return unsupportedConversion(field+"."+key, "unknown schema descriptor field")
			}
		}
		schema, ok := rawJSONObject(obj["schema"])
		if !ok || schema == nil {
			return unsupportedConversion(field+".schema", "expected a JSON Schema object")
		}
		for _, key := range []string{"name", "description"} {
			if value, exists := obj[key]; exists {
				var s string
				if len(normalizedRawJSON(value)) == 0 || json.Unmarshal(value, &s) != nil {
					return unsupportedConversion(field+"."+key, "expected a string")
				}
			}
		}
		if value, exists := obj["strict"]; exists {
			var strict bool
			if len(normalizedRawJSON(value)) == 0 || json.Unmarshal(value, &strict) != nil {
				return unsupportedConversion(field+".strict", "expected a boolean")
			}
		}
		return nil
	default:
		return unsupportedConversion(field+".type", "unknown output format")
	}
}

func responsesTextFormatToAnthropic(raw json.RawMessage) (json.RawMessage, error) {
	if err := validateResponseFormat(raw, false); err != nil {
		return nil, err
	}
	if len(normalizedRawJSON(raw)) == 0 {
		return nil, nil
	}
	obj, _ := rawJSONObject(raw)
	switch rawString(obj["type"]) {
	case "text":
		return nil, nil
	case "json_object":
		return nil, unsupportedConversion("text.format.type", "unconstrained JSON object mode has no exact Anthropic equivalent; supply a JSON schema")
	case "json_schema":
		if nested, exists := obj["json_schema"]; exists {
			obj, _ = rawJSONObject(nested)
		}
		// Anthropic enforces output schemas; explicit non-strict intent cannot
		// be strengthened silently. Name is a protocol identifier only.
		if string(bytesTrimSpace(obj["strict"])) == "false" {
			return nil, unsupportedConversion("text.format.strict", "Anthropic structured output cannot disable schema enforcement")
		}
		if rawString(obj["description"]) != "" {
			return nil, unsupportedConversion("text.format.description", "Anthropic has no format-description field; use a schema annotation")
		}
		return json.Marshal(map[string]json.RawMessage{"type": rawJSONString("json_schema"), "schema": obj["schema"]})
	}
	return nil, unsupportedConversion("text.format", "unknown output format")
}
