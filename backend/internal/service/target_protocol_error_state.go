package service

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

// targetProtocolErrorState keeps only the envelope needed to finish a delivered
// Responses stream. It never retains generated content or tool arguments.
type targetProtocolErrorState struct {
	mu           sync.Mutex
	pending      []byte
	id, model    string
	createdAt    int64
	nextSequence int64
	usage        json.RawMessage
	opened       bool
}

func (s *targetProtocolErrorState) observe(contentType string, data []byte) {
	if !strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, data...)
	for {
		end, sep := sseBlockEnd(s.pending)
		if end < 0 {
			break
		}
		block := s.pending[:end]
		s.pending = s.pending[end+sep:]
		event, payload := sseBlockFields(block)
		if !gjson.ValidBytes(payload) {
			continue
		}
		root := gjson.ParseBytes(payload)
		kind := root.Get("type").String()
		if kind == "" {
			kind = event
		}
		if !strings.HasPrefix(kind, "response.") {
			continue
		}
		s.opened = true
		if id := root.Get("response.id"); id.Type == gjson.String && id.Str != "" {
			s.id = id.Str
		}
		if model := root.Get("response.model"); model.Type == gjson.String {
			s.model = model.Str
		}
		if at := root.Get("response.created_at"); at.Type == gjson.Number {
			s.createdAt = at.Int()
		}
		if seq := root.Get("sequence_number"); seq.Type == gjson.Number && seq.Int() >= s.nextSequence {
			s.nextSequence = seq.Int() + 1
		}
		for _, path := range []string{"usage", "response.usage"} {
			if u := root.Get(path); u.IsObject() {
				// Empty or unobserved counters cannot replace retained usage evidence.
				if _, present := openAIResponsesUsageEvidence(payload, "upstream_sse_progressive"); present {
					s.usage = append(s.usage[:0], u.Raw...)
				}
				break
			}
		}
	}
	if len(s.pending) > ssePendingLimit {
		s.pending = nil
	}
}

// targetProtocolFailureUsage normalizes only the three required wire counters.
// Keep observed details unchanged; normalization does not set request usage or billing facts.
func targetProtocolFailureUsage(raw json.RawMessage) json.RawMessage {
	envelope, err := json.Marshal(struct {
		Usage json.RawMessage `json:"usage"`
	}{raw})
	if err != nil {
		return nil
	}
	if _, present := openAIResponsesUsageEvidence(envelope, "upstream_sse_progressive"); !present {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	counters := make(map[string]json.RawMessage)
	for _, field := range []string{"input_tokens", "output_tokens", "total_tokens", "prompt_tokens", "completion_tokens"} {
		if v, ok := fields[field]; ok {
			counters[field] = v
		}
	}
	encoded, _ := json.Marshal(counters)
	var usage apicompat.ResponsesUsage
	if json.Unmarshal(encoded, &usage) != nil {
		return nil
	}
	for name, value := range map[string]int{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "total_tokens": usage.TotalTokens} {
		fields[name], _ = json.Marshal(value)
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return normalized
}
