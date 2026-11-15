package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// bridgeHistoryOccurrences uses private occurrence keys only while normalizing.
// Outbound IDs remain unchanged. Ambiguous, unmatched and same-round duplicates
// retain the legacy projection, including its last-result behavior.
func bridgeHistoryOccurrences(input json.RawMessage) (json.RawMessage, map[string]string, error) {
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return input, nil, nil
	}
	pairs, err := PairToolHistory(items, false)
	if err != nil {
		return nil, nil, err
	}
	calls, results := make(map[string]int), make(map[string]int)
	occupied := make(map[string]bool)
	safe := make(map[string]bool)
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		id := rawString(item["call_id"])
		if id == "" {
			continue
		}
		occupied[id] = true
		switch rawString(item["type"]) {
		case "function_call", "custom_tool_call":
			if calls[id] == 0 {
				safe[id] = true
			}
			calls[id]++
			if rawString(item["type"]) == "function_call" {
				args := rawString(item["arguments"])
				if strings.TrimSpace(args) != "" && !json.Valid([]byte(args)) {
					safe[id] = false
				}
			}
		case "function_call_output", "custom_tool_call_output":
			results[id]++
		}
	}
	grouped := make(map[string][]ToolHistoryPair)
	for _, pair := range pairs {
		grouped[pair.CallID] = append(grouped[pair.CallID], pair)
	}
	restore := make(map[string]string)
	for id, group := range grouped {
		if id == "" || !safe[id] || len(group) < 2 || calls[id] != len(group) || results[id] != len(group) {
			continue
		}
		rounds := make(map[int]bool)
		distinct := true
		for _, pair := range group {
			if rounds[pair.Round] {
				distinct = false
			}
			rounds[pair.Round] = true
		}
		if !distinct {
			continue
		}
		for _, pair := range group {
			key := fmt.Sprintf("call_m4_occurrence_%d_%d", pair.Round, pair.Slot)
			for occupied[key] {
				key += "_"
			}
			occupied[key] = true
			restore[key] = id
			for _, index := range []int{pair.CallIndex, pair.ResultIndex} {
				var item map[string]json.RawMessage
				if err := json.Unmarshal(items[index], &item); err != nil {
					return nil, nil, err
				}
				item["call_id"], _ = json.Marshal(key)
				items[index], err = json.Marshal(item)
				if err != nil {
					return nil, nil, err
				}
			}
		}
	}
	if len(restore) == 0 {
		return input, nil, nil
	}
	raw, err := json.Marshal(items)
	return raw, restore, err
}

func restoreBridgeChatHistory(messages []ChatMessage, restore map[string]string) {
	for i := range messages {
		m := &messages[i]
		if id, ok := restore[m.ToolCallID]; ok {
			m.ToolCallID = id
		}
		for j := range m.ToolCalls {
			if id, ok := restore[m.ToolCalls[j].ID]; ok {
				m.ToolCalls[j].ID = id
			}
		}
		if m.Role == "user" {
			var parts []ChatContentPart
			if json.Unmarshal(m.Content, &parts) != nil {
				continue
			}
			changed := false
			for j := range parts {
				for key, id := range restore {
					if parts[j].Type == "text" && parts[j].Text == fmt.Sprintf(toolOutputMediaAttribution, key) {
						parts[j].Text = fmt.Sprintf(toolOutputMediaAttribution, id)
						changed = true
					}
				}
			}
			if changed {
				m.Content, _ = json.Marshal(parts)
			}
		}
	}
}

func restoreBridgeAnthropicHistory(messages []AnthropicMessage, restore map[string]string) {
	if len(restore) == 0 {
		return
	}
	ids := make(map[string]string)
	for key, id := range restore {
		ids[fromResponsesCallIDToAnthropic(key)] = fromResponsesCallIDToAnthropic(id)
	}
	for i := range messages {
		blocks := parseContentBlocks(messages[i].Content)
		changed := false
		for j := range blocks {
			b := &blocks[j]
			if b.Type == "tool_use" {
				if id, ok := ids[b.ID]; ok {
					b.ID = id
					changed = true
				}
			}
			if b.Type == "tool_result" {
				if id, ok := ids[b.ToolUseID]; ok {
					b.ToolUseID = id
					changed = true
				}
			}
		}
		if changed {
			messages[i].Content, _ = json.Marshal(blocks)
		}
	}
}
