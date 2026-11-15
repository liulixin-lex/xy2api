package apicompat

import "encoding/json"

// splitChatAssistantContent separates supported plaintext reasoning without
// rewriting visible parts or their cache checkpoints. The caller opts in only
// after establishing the final target's plaintext reasoning capability.
func splitChatAssistantContent(m ChatMessage) (string, json.RawMessage, error) {
	if _, err := parseAssistantContent(m.Content); err != nil {
		return "", nil, err
	}
	reasoning := m.ReasoningContent
	var parts []json.RawMessage
	if json.Unmarshal(m.Content, &parts) != nil {
		return reasoning, m.Content, nil
	}
	visible := make([]json.RawMessage, 0, len(parts))
	for _, raw := range parts {
		var part struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		if err := json.Unmarshal(raw, &part); err != nil {
			return "", nil, err
		}
		if part.Type == "thinking" || part.Type == "reasoning" {
			if part.Thinking != "" {
				reasoning += part.Thinking
			} else {
				reasoning += part.Text
			}
		} else {
			visible = append(visible, raw)
		}
	}
	raw, err := json.Marshal(visible)
	return reasoning, raw, err
}
