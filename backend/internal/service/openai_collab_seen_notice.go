package service

import (
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// ParseCollabSeenNotices extracts event digests for the single record batch.
// Senders maps envelope names to verified tree members; unknown names cannot
// authorize borrowing. Only one plaintext fragment with the exact final-answer
// envelope can establish a parent notice. Other appearances remain seen_other.
func (s *OpenAIGatewayService) ParseCollabSeenNotices(body []byte, senders map[string]resilience.CollabDigest) []resilience.CollabSeenNotice {
	if len(body) > 4<<20 || !gjson.ValidBytes(body) {
		return nil
	}
	root := gjson.ParseBytes(body)
	if !codexCollabUniqueObject(root) {
		return nil
	}
	var out []resilience.CollabSeenNotice
	items := root.Get("input").Array()
	lastOutput := -1
	for i, item := range items {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call", "reasoning", "compaction":
			lastOutput = i
		case "message", "":
			if item.Get("role").String() == "assistant" {
				lastOutput = i
			}
		}
	}
	for i, item := range items {
		if !codexCollabUniqueObject(item) {
			continue
		}
		content := item.Get("content")
		fragments := content.Array()
		if content.Type == gjson.String {
			fragments = []gjson.Result{content}
		}
		for _, part := range fragments {
			text := part.String()
			if part.IsObject() {
				if !codexCollabUniqueObject(part) {
					continue
				}
				if part.Get("type").String() != "input_text" {
					continue
				}
				text = part.Get("text").String()
			}
			sender, final := collabNoticeEnvelope(text)
			final = final && item.Get("type").String() == "agent_message" && len(fragments) == 1
			for remaining := text; len(out) < 32; {
				_, after, ok := strings.Cut(remaining, "Gateway notice [")
				if !ok {
					break
				}
				event, rest, ok := strings.Cut(after, "].")
				if !ok {
					break
				}
				remaining = rest
				if len(event) < 1 || len(event) > 128 || strings.ContainsAny(event, " \t\r\n[]") {
					continue
				}
				out = append(out, resilience.CollabSeenNotice{Event: s.CollabDigest(resilience.CollabDigestDomainEvent, event), Sender: senders[sender], FinalAnswer: final, Current: i > lastOutput})
			}
		}
	}
	return out
}
func collabNoticeEnvelope(text string) (string, bool) {
	lines := strings.SplitN(text, "\n", 5)
	if len(lines) != 5 || lines[0] != "Message Type: FINAL_ANSWER" || !strings.HasPrefix(lines[1], "Task name: ") || strings.TrimPrefix(lines[1], "Task name: ") == "" || !strings.HasPrefix(lines[2], "Sender: ") || lines[3] != "Payload:" {
		return "", false
	}
	sender := strings.TrimPrefix(lines[2], "Sender: ")
	if sender == "" || strings.ContainsAny(sender, "\r\n") || !strings.HasPrefix(lines[4], "Gateway notice [") {
		return "", false
	}
	return sender, true
}
