package service

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// prepareGeminiMessagesToolHistory orders only positively paired result slots
// within their existing user message. Orphans, missing results, duplicate IDs
// and non-tool content retain the converter's established handling.
func prepareGeminiMessagesToolHistory(body []byte) ([]byte, error) {
	type position struct{ message, block int }
	var items []json.RawMessage
	var positions []position
	contents := make(map[int][]json.RawMessage)
	for messageIndex, message := range gjson.GetBytes(body, "messages").Array() {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for blockIndex, block := range content.Array() {
			contents[messageIndex] = append(contents[messageIndex], json.RawMessage(block.Raw))
			kind, id := "", ""
			switch {
			case message.Get("role").String() == "assistant" && block.Get("type").String() == "tool_use":
				kind, id = "function_call", block.Get("id").String()
			case message.Get("role").String() == "user" && block.Get("type").String() == "tool_result":
				kind, id = "function_call_output", block.Get("tool_use_id").String()
			}
			if kind == "" || id == "" {
				if kind != "" {
					return body, nil
				}
				continue
			}
			raw, _ := json.Marshal(map[string]string{"type": kind, "call_id": id})
			items = append(items, raw)
			positions = append(positions, position{messageIndex, blockIndex})
		}
	}
	paired, err := apicompat.PairToolHistory(items, false)
	if err != nil {
		return nil, err
	}
	if len(paired)*2 != len(items) {
		return body, nil
	}
	// Reuse across rounds is valid; preserve the old policy for duplicate IDs
	// within one round, where changing order could change ambiguous history.
	seen := make(map[string]bool)
	for _, pair := range paired {
		key := fmt.Sprintf("%d/%s", pair.Round, pair.CallID)
		if seen[key] {
			return body, nil
		}
		seen[key] = true
	}
	type groupKey struct{ message, round int }
	groups := make(map[groupKey][]apicompat.ToolHistoryPair)
	for _, pair := range paired {
		pos := positions[pair.ResultIndex]
		key := groupKey{pos.message, pair.Round}
		groups[key] = append(groups[key], pair)
	}
	changed := make(map[int]bool)
	for key, pairs := range groups {
		if len(pairs) < 2 {
			continue
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].Slot < pairs[j].Slot })
		slots := make([]int, len(pairs))
		results := make([]json.RawMessage, len(pairs))
		for i, pair := range pairs {
			slots[i] = positions[pair.ResultIndex].block
			results[i] = contents[key.message][slots[i]]
		}
		sort.Ints(slots)
		for i, slot := range slots {
			if string(contents[key.message][slot]) != string(results[i]) {
				changed[key.message] = true
			}
			contents[key.message][slot] = results[i]
		}
	}
	for messageIndex := range changed {
		body, err = sjson.SetBytes(body, fmt.Sprintf("messages.%d.content", messageIndex), contents[messageIndex])
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}
