package apicompat

import (
	"encoding/json"
	"sort"
)

type ToolHistoryPair struct {
	Round, CallIndex, ResultIndex, Slot int
	Kind, CallID                        string
}

// PairToolHistory matches earlier pending occurrences without validating closure.
// Unmatched and unrecognized items remain the caller's original responsibility.
func PairToolHistory(items []json.RawMessage, serverContinuation bool) ([]ToolHistoryPair, error) {
	type identity struct{ kind, id, name string }
	pending := make(map[identity][]ToolHistoryPair)
	var pairs []ToolHistoryPair
	round, slot, count := -1, 0, 0
	for index, raw := range items {
		var item struct{ Type, CallID, Name string }
		var wire struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		}
		if json.Unmarshal(raw, &wire) != nil {
			continue
		}
		item.Type, item.CallID, item.Name = wire.Type, wire.CallID, wire.Name
		kind, result := "", false
		switch item.Type {
		case "function_call":
			kind = "function"
		case "function_call_output":
			kind = "function"
			result = true
		case "custom_tool_call":
			kind = "custom"
		case "custom_tool_call_output":
			kind = "custom"
			result = true
		default:
			continue
		}
		key := identity{kind: kind, id: item.CallID}
		if key.id == "" {
			key.name = item.Name
			if key.name == "" {
				continue
			}
		}
		if !result {
			if count == 0 {
				round++
				slot = 0
			}
			pending[key] = append(pending[key], ToolHistoryPair{Round: round, CallIndex: index, ResultIndex: -1, Slot: slot, Kind: kind, CallID: item.CallID})
			slot++
			count++
			continue
		}
		queue := pending[key]
		if len(queue) == 0 {
			continue
		}
		pair := queue[0]
		pair.ResultIndex = index
		pairs = append(pairs, pair)
		pending[key] = queue[1:]
		count--
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].CallIndex < pairs[j].CallIndex })
	return pairs, nil
}
