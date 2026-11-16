package apicompat

import "encoding/json"

// ExtractResponsesReasoningItemWithBridgeOptions uses the foundation's readable
// carrier rule only for the explicit bridge path.
func ExtractResponsesReasoningItemWithBridgeOptions(raw json.RawMessage, opts ConversionOptions) (id, text string, ok bool) {
	if !bridgeEnabled(opts.Bridge) {
		return ExtractResponsesReasoningItem(raw)
	}
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil || rawString(item["type"]) != "reasoning" {
		return "", "", false
	}
	return rawString(item["id"]), extractBridgeResponsesReasoningText(item), true
}

func extractBridgeResponsesReasoningText(raw map[string]json.RawMessage) string {
	item := ResponsesOutput{Type: "reasoning"}
	_ = json.Unmarshal(raw["summary"], &item.Summary)
	_ = json.Unmarshal(raw["content"], &item.Content)
	return ResponsesOutputReasoningText(item)
}

func bridgeReasoningHistory(input json.RawMessage) (json.RawMessage, error) {
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return input, nil
	}
	for i, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || rawString(item["type"]) != "reasoning" {
			continue
		}
		text := extractBridgeResponsesReasoningText(item)
		delete(item, "content")
		delete(item, "summary")
		if text != "" {
			item["summary"], _ = json.Marshal([]ResponsesSummary{{Type: "summary_text", Text: text}})
		}
		var err error
		items[i], err = json.Marshal(item)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(items)
}
