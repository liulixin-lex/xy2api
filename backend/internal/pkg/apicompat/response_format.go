package apicompat

import "encoding/json"

func chatResponseFormatToResponsesTextFormat(raw json.RawMessage) (json.RawMessage, error) {
	if err := validateResponseFormat(raw, true); err != nil {
		return nil, err
	}
	return chatResponseFormatToResponsesTextFormatUnchecked(raw), nil
}

func chatResponseFormatToResponsesTextFormatUnchecked(raw json.RawMessage) json.RawMessage {
	raw = normalizedRawJSON(raw)
	if len(raw) == 0 {
		return nil
	}

	obj, ok := rawJSONObject(raw)
	if !ok {
		return raw
	}
	formatType := normalizedResponseFormatType(obj["type"])
	if formatType != "json_schema" {
		return json.RawMessage(`{"type":"` + formatType + `"}`)
	}

	schemaRaw := normalizedRawJSON(obj["json_schema"])
	if len(schemaRaw) == 0 {
		return raw
	}

	var schema map[string]json.RawMessage
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return raw
	}
	schema["type"] = rawJSONString("json_schema")

	out, err := json.Marshal(schema)
	if err != nil {
		return raw
	}
	return out
}

func responsesTextFormatToChatResponseFormat(raw json.RawMessage) (json.RawMessage, error) {
	if err := validateResponseFormat(raw, false); err != nil {
		return nil, err
	}
	return responsesTextFormatToChatResponseFormatUnchecked(raw), nil
}

func responsesTextFormatToChatResponseFormatUnchecked(raw json.RawMessage) json.RawMessage {
	raw = normalizedRawJSON(raw)
	if len(raw) == 0 {
		return nil
	}

	obj, ok := rawJSONObject(raw)
	if !ok {
		return raw
	}
	formatType := normalizedResponseFormatType(obj["type"])
	if formatType != "json_schema" {
		return json.RawMessage(`{"type":"` + formatType + `"}`)
	}
	if _, alreadyChatShape := obj["json_schema"]; alreadyChatShape {
		return raw
	}

	schema := make(map[string]json.RawMessage, len(obj))
	for key, value := range obj {
		if key == "type" {
			continue
		}
		schema[key] = value
	}
	if len(schema) == 0 {
		return raw
	}

	schemaRaw, err := json.Marshal(schema)
	if err != nil {
		return raw
	}
	out, err := json.Marshal(map[string]json.RawMessage{
		"type":        rawJSONString("json_schema"),
		"json_schema": schemaRaw,
	})
	if err != nil {
		return raw
	}
	return out
}

func normalizedRawJSON(raw json.RawMessage) json.RawMessage {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func rawJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	return obj, true
}

func rawJSONString(value string) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
