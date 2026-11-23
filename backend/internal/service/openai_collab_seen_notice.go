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
	ambiguous := !codexCollabUnambiguousJSON(root)
	for i, item := range items {
		kind := item.Get("type")
		if !item.IsObject() || !codexCollabUniqueObject(item) || (kind.Exists() && kind.Type != gjson.String) || (!kind.Exists() && item.Get("role").Type != gjson.String) {
			ambiguous = true
		}
		if !collabProvenInput(item) {
			lastOutput = i
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
			author, recipient := item.Get("author"), item.Get("recipient")
			final = final && item.Get("type").String() == "agent_message" && len(fragments) == 1 && author.Type == gjson.String && author.String() == sender && recipient.Type == gjson.String && recipient.String() != "" && senders[recipient.String()] != ""
			anchored := collabAnchoredEvent(text)
			if _, body, ok := strings.Cut(text, "\nPayload:\n"); ok {
				anchored = collabAnchoredEvent(body)
			}
			firstEvent := true
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
				out = append(out, resilience.CollabSeenNotice{Event: s.CollabDigest(resilience.CollabDigestDomainEvent, event), Sender: senders[sender], Recipient: senders[recipient.String()], FinalAnswer: final && firstEvent && event == anchored, Current: !ambiguous && i > lastOutput})
				firstEvent = false
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

// Unknown types and ambiguous objects are turn boundaries, including future
// built-in model outputs. Only explicit input shapes may preserve a notice.
func collabProvenInput(item gjson.Result) bool {
	if !item.IsObject() || !codexCollabUniqueObject(item) {
		return false
	}
	switch item.Get("type").String() {
	case "agent_message":
		return collabAgentInputContent(item.Get("content"))
	case "function_call_output", "custom_tool_call_output":
		return item.Get("call_id").Type == gjson.String && item.Get("call_id").String() != "" && (item.Get("output").Type == gjson.String || collabInputContent(item.Get("output"), false))
	case "message", "":
		role := item.Get("role")
		if role.Type != gjson.String || (role.String() != "user" && role.String() != "developer") {
			return false
		}
		content := item.Get("content")
		return collabInputContent(content, true)
	default:
		return false
	}
}

func collabInputContent(content gjson.Result, textAllowed bool) bool {
	if textAllowed && content.Type == gjson.String {
		return true
	}
	if !content.IsArray() || len(content.Array()) == 0 {
		return false
	}
	for _, part := range content.Array() {
		if !part.IsObject() || !codexCollabUniqueObject(part) {
			return false
		}
		switch part.Get("type").String() {
		case "input_text":
			if part.Get("text").Type != gjson.String {
				return false
			}
		case "input_image", "input_file", "input_audio":
		default:
			return false
		}
	}
	return true
}

func collabAgentInputContent(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	parts := content.Array()
	if len(parts) == 1 {
		return collabInputContent(content, false)
	}
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if !part.IsObject() || !codexCollabUniqueObject(part) {
			return false
		}
	}
	return parts[0].Get("type").String() == "input_text" && parts[0].Get("text").Type == gjson.String && parts[1].Get("type").String() == "encrypted_content" && parts[1].Get("encrypted_content").Type == gjson.String
}
func collabAnchoredEvent(text string) string {
	rest, ok := strings.CutPrefix(text, "Gateway notice [")
	if !ok {
		return ""
	}
	event, _, ok := strings.Cut(rest, "].")
	if !ok || len(event) < 1 || len(event) > 128 || strings.ContainsAny(event, " \t\r\n[]") {
		return ""
	}
	return event
}

// ParseCollabNoticeReceipts recognizes a notice in this request's history.
// It does not authorize borrowing and is meaningful only with a matching live
// gateway record. Later quoted events, user text and ambiguous objects do not count.
func (s *OpenAIGatewayService) ParseCollabNoticeReceipts(body []byte) []resilience.CollabDigest {
	if len(body) > 4<<20 || !gjson.ValidBytes(body) {
		return nil
	}
	root := gjson.ParseBytes(body)
	if !codexCollabUnambiguousJSON(root) {
		return nil
	}
	var receipts []resilience.CollabDigest
	seen := make(map[resilience.CollabDigest]bool)
	for _, item := range root.Get("input").Array() {
		kind := item.Get("type").String()
		own := (kind == "message" || kind == "") && item.Get("role").String() == "assistant"
		child := kind == "agent_message"
		if !own && !child {
			continue
		}
		content := item.Get("content")
		parts := content.Array()
		var texts []string
		if own && content.Type == gjson.String {
			texts = []string{content.String()}
		}
		if child && len(parts) != 1 {
			continue
		}
		for _, part := range parts {
			if (own && (part.Get("type").String() == "output_text" || part.Get("type").String() == "input_text")) || (child && part.Get("type").String() == "input_text") {
				if part.Get("text").Type == gjson.String {
					texts = append(texts, part.Get("text").String())
				}
			}
		}
		for _, text := range texts {
			if child {
				sender, valid := collabNoticeEnvelope(text)
				if !valid || item.Get("author").Type != gjson.String || item.Get("author").String() != sender || item.Get("recipient").Type != gjson.String || item.Get("recipient").String() == "" {
					continue
				}
				_, text, _ = strings.Cut(text, "\nPayload:\n")
			}
			event := collabAnchoredEvent(text)
			if event == "" {
				continue
			}
			digest := s.CollabDigest(resilience.CollabDigestDomainEvent, event)
			if digest != "" && !seen[digest] {
				seen[digest] = true
				receipts = append(receipts, digest)
			}
			if len(receipts) == 32 {
				return receipts
			}
		}
	}
	return receipts
}
