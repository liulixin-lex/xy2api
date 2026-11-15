package service

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// mergeResponsesOutputFromSSE fills missing items and fields without replacing
// terminal values (including explicit empty strings and arrays).
func mergeResponsesOutputFromSSE(finalResponse []byte, bodyText string) ([]byte, bool) {
	return mergeResponsesOutputItems(finalResponse, bodyText, false)
}

type responsesOutputMergeItem struct {
	index     int
	indexed   bool
	added     map[string]json.RawMessage
	done      map[string]json.RawMessage
	terminal  map[string]json.RawMessage
	delta     map[string]json.RawMessage
	parts     map[int]map[string]json.RawMessage
	summary   map[int]map[string]json.RawMessage
	seenSeq   map[int64]bool
	arguments string
	argsDone  bool
	textDone  map[string]bool
}

func mergeResponsesOutputItems(finalResponse []byte, bodyText string, rawOnly bool) ([]byte, bool) {
	var items []*responsesOutputMergeItem
	byIndex := make(map[int]*responsesOutputMergeItem)
	byID := make(map[string]*responsesOutputMergeItem)
	resolve := func(index gjson.Result, id string, delta bool) *responsesOutputMergeItem {
		item := byID[id]
		if item == nil && index.Exists() {
			item = byIndex[int(index.Int())]
		}
		// Older streams omit both identifiers on deltas for the first item.
		if item == nil && delta && id == "" && !index.Exists() && len(items) > 0 {
			item = items[len(items)-1]
		}
		if item == nil && index.Exists() && index.Int() == 0 && len(items) == 1 && !items[0].indexed && len(items[0].done) == 0 {
			item = items[0]
		}
		if item == nil {
			item = &responsesOutputMergeItem{delta: make(map[string]json.RawMessage), parts: make(map[int]map[string]json.RawMessage), summary: make(map[int]map[string]json.RawMessage), seenSeq: make(map[int64]bool), textDone: make(map[string]bool)}
			items = append(items, item)
		}
		if index.Exists() {
			item.index, item.indexed = int(index.Int()), true
			byIndex[item.index] = item
		}
		if id != "" {
			byID[id] = item
			item.delta["id"] = responsesMergeJSON(id)
		}
		return item
	}
	forEachOpenAISSEFrame(bodyText, func(event string, data []byte) {
		if !gjson.ValidBytes(data) {
			return
		}
		if normalized, changed := normalizeCompletedImageGenerationStatus(data); changed {
			data = normalized
		}
		value := gjson.ParseBytes(data)
		raw := value.Get("item")
		isItem := event == "response.output_item.added" || event == "response.output_item.done"
		isDelta := event == "response.output_text.delta" || event == "response.output_text.done" ||
			event == "response.refusal.delta" || event == "response.refusal.done" ||
			event == "response.function_call_arguments.delta" || event == "response.function_call_arguments.done" ||
			event == "response.reasoning_summary_text.delta" || event == "response.reasoning_summary_text.done" ||
			event == "response.content_part.added" || event == "response.content_part.done"
		if (!isItem || !raw.IsObject()) && (!isDelta || rawOnly) {
			return
		}
		id := firstNonEmpty(raw.Get("id").String(), value.Get("item_id").String())
		item := resolve(value.Get("output_index"), id, isDelta)
		if isItem {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal([]byte(raw.Raw), &fields)
			if event == "response.output_item.done" {
				item.done = fields
			} else {
				item.added = fields
			}
			return
		}
		if seq := value.Get("sequence_number"); seq.Exists() {
			if item.seenSeq[seq.Int()] {
				return
			}
			item.seenSeq[seq.Int()] = true
		}
		if strings.HasPrefix(event, "response.function_call_arguments.") {
			item.delta["type"] = responsesMergeJSON("function_call")
			for _, field := range []string{"call_id", "name"} {
				if v := value.Get(field); v.Exists() {
					item.delta[field] = json.RawMessage(v.Raw)
				}
			}
			if strings.HasSuffix(event, ".done") {
				item.arguments, item.argsDone = value.Get("arguments").String(), true
			} else if !item.argsDone {
				item.arguments += value.Get("delta").String()
			}
			item.delta["arguments"] = responsesMergeJSON(item.arguments)
			return
		}
		parts, index, kind, field := item.parts, int(value.Get("content_index").Int()), "output_text", "text"
		if strings.HasPrefix(event, "response.reasoning_summary_text.") {
			parts, index, kind = item.summary, int(value.Get("summary_index").Int()), "summary_text"
			item.delta["type"] = responsesMergeJSON("reasoning")
		} else {
			item.delta["type"] = responsesMergeJSON("message")
			item.delta["role"] = responsesMergeJSON("assistant")
		}
		if parts[index] == nil {
			parts[index] = make(map[string]json.RawMessage)
		}
		part := parts[index]
		if strings.HasPrefix(event, "response.content_part.") {
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(value.Get("part").Raw), &fields) == nil {
				parts[index] = responsesMergeMissing(fields, part)
				if strings.HasSuffix(event, ".done") {
					item.textDone[value.Get("part.type").String()+":"+strconv.Itoa(index)] = true
				}
			}
			return
		}
		if strings.HasPrefix(event, "response.refusal.") {
			kind, field = "refusal", "refusal"
		}
		part["type"] = responsesMergeJSON(kind)
		key := kind + ":" + strconv.Itoa(index)
		var text string
		_ = json.Unmarshal(part[field], &text)
		if strings.HasSuffix(event, ".done") {
			text = value.Get(field).String()
			item.textDone[key] = true
		} else if !item.textDone[key] {
			text += value.Get("delta").String()
		}
		part[field] = responsesMergeJSON(text)
	})
	for i, raw := range gjson.GetBytes(finalResponse, "output").Array() {
		if !raw.IsObject() {
			continue
		}
		id := raw.Get("id").String()
		index := raw.Get("output_index")
		if !index.Exists() && byID[id] == nil {
			index = gjson.Result{Type: gjson.Number, Num: float64(i)}
			// A partial terminal array can start with a later item. Match a
			// uniquely known type before using its array position.
			var match *responsesOutputMergeItem
			for _, candidate := range items {
				fields := responsesMergeMissing(candidate.done, candidate.added)
				if string(fields["type"]) == string(responsesMergeJSON(raw.Get("type").String())) {
					if match != nil {
						match = nil
						break
					}
					match = candidate
				}
			}
			if match != nil && match.indexed {
				index = gjson.Result{Type: gjson.Number, Num: float64(match.index)}
			}
		}
		item := resolve(index, id, false)
		_ = json.Unmarshal([]byte(raw.Raw), &item.terminal)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].indexed && items[j].indexed {
			return items[i].index < items[j].index
		}
		return items[i].indexed && !items[j].indexed
	})
	var output []map[string]json.RawMessage
	for _, item := range items {
		if rawOnly {
			if len(item.done) > 0 {
				output = append(output, item.done)
			} else if isResponsesCompactionItemType(gjson.ParseBytes(item.added["type"]).String()) {
				output = append(output, item.added)
			}
			continue
		}
		for field, parts := range map[string]map[int]map[string]json.RawMessage{"content": item.parts, "summary": item.summary} {
			if len(parts) == 0 {
				continue
			}
			indices := make([]int, 0, len(parts))
			for index := range parts {
				indices = append(indices, index)
			}
			sort.Ints(indices)
			ordered := make([]map[string]json.RawMessage, 0, len(parts))
			for _, index := range indices {
				ordered = append(ordered, parts[index])
			}
			item.delta[field] = responsesMergeJSON(ordered)
		}
		// added is an initial shell: its empty content/arguments are not final.
		fields := responsesMergeMissing(item.delta, item.added)
		fields = responsesMergeMissing(item.done, fields)
		fields = responsesMergeMissing(item.terminal, fields)
		if len(fields) > 0 {
			output = append(output, fields)
		}
	}
	if len(output) == 0 {
		return nil, false
	}
	encoded, err := json.Marshal(output)
	return encoded, err == nil
}

func responsesMergeJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func responsesMergeMissing(primary, fallback map[string]json.RawMessage) map[string]json.RawMessage {
	merged := make(map[string]json.RawMessage, len(primary)+len(fallback))
	for key, value := range fallback {
		merged[key] = value
	}
	for key, value := range primary {
		if key == "content" || key == "summary" {
			var finalParts, earlierParts []map[string]json.RawMessage
			if json.Unmarshal(value, &finalParts) == nil && len(finalParts) > 0 && json.Unmarshal(fallback[key], &earlierParts) == nil {
				for i, part := range earlierParts {
					if i < len(finalParts) {
						finalParts[i] = responsesMergeMissing(finalParts[i], part)
					} else {
						finalParts = append(finalParts, part)
					}
				}
				value = responsesMergeJSON(finalParts)
			}
		}
		merged[key] = value
	}
	return merged
}
