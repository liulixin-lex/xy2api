package service

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	collabUnreadTask    = "[Gateway notice: the task content could not be read. Do not guess it. If a newer task follows, use that one; otherwise report that no task was received.]"
	collabProcessedTask = "[Gateway notice: the original task content, already processed earlier in this thread, is no longer readable here.]"
	collabMessage       = "[Gateway notice: this message could not be read. Do not guess its content.]"
	collabToolOutput    = "[Gateway notice: this tool result could not be read. It indicates neither success nor failure. Do not blindly repeat actions that have side effects.]"
	collabCompaction    = "[Gateway notice: an earlier conversation summary could not be read and is omitted.]"
)

// CollabBodyItem retains original locations. Edits replace in place, so indices
// stay stable across derivations. Unknown message envelopes are protected.
type CollabBodyItem struct {
	Identity    string
	Index       int
	Family      resilience.CollabFamily
	Class       resilience.CollabItemClass
	Paths       []string
	Placeholder string
}

type CollabBody struct {
	Original []byte
	Items    []CollabBodyItem
	Shape    resilience.CollabRequestShape
}

func (p CollabBody) ResolveEvidence(evidence resilience.CollabEvidence) resilience.CollabEvidence {
	if evidence.Named == nil || evidence.Named.InputIndex == nil {
		return evidence
	}
	named := *evidence.Named
	evidence.Named = &named
	index := *named.InputIndex
	items := gjson.GetBytes(p.Original, "input").Array()
	if index < 0 || index >= len(items) {
		named.Ambiguous = true
		return evidence
	}
	id := items[index].Get("id").String()
	if named.Identity != "" && named.Identity != id {
		named.Ambiguous = true
		return evidence
	}
	named.Identity = id
	return evidence
}

func ParseCollabBody(body []byte) CollabBody {
	out := CollabBody{Original: body}
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !root.IsObject() || !codexCollabUnambiguousJSON(root) || !root.Get("input").IsArray() {
		out.Shape.Ambiguous = true
		return out
	}
	out.Shape.ContinuationPointer = root.Get("previous_response_id").String() != "" || root.Get("conversation").Exists()
	ids := map[string]bool{}
	input := root.Get("input").Array()
	lastOutput := -1
	for i, item := range input {
		if item.Get("role").String() == "assistant" || item.Get("type").String() == "compaction" {
			lastOutput = i
		}
	}
	for i, item := range input {
		if !item.IsObject() {
			out.Shape.Ambiguous = true
			return out
		}
		id := item.Get("id").String()
		if id != "" && ids[id] {
			out.Shape.Ambiguous = true
			return out
		}
		ids[id] = id != ""
		entry := CollabBodyItem{Identity: id, Index: i, Class: resilience.CollabItemClassProtected, Placeholder: collabUnreadTask}
		path := "input." + strconv.Itoa(i)
		switch item.Get("type").String() {
		case "reasoning":
			entry.Family = resilience.CollabFamilyReasoning
			if item.Get("encrypted_content").Type == gjson.String && item.Get("encrypted_content").Str != "" {
				entry.Paths = []string{path + ".encrypted_content"}
				out.Shape.ReasoningCiphertext = true
			}
		case "compaction":
			entry.Family, entry.Placeholder = resilience.CollabFamilyCompaction, collabCompaction
			if item.Get("encrypted_content").Type == gjson.String && item.Get("encrypted_content").Str != "" {
				entry.Paths = []string{path}
				out.Shape.CompactionCiphertext = true
			}
		case "function_call", "custom_tool_call":
			if marker := item.Get("encrypted_function_args"); marker.Exists() && marker.Raw != "[]" {
				out.Shape.CallArgumentsCiphertext = true
			}
			// Validation established that these bytes are not decrypted here.
			continue
		case "agent_message", "message", "function_call_output":
			container := "content"
			entry.Family = resilience.CollabFamilyAgentMessage
			if item.Get("type").String() == "function_call_output" {
				container = "output"
				entry.Family = resilience.CollabFamilyToolOutput
				entry.Placeholder = collabToolOutput
			}
			parts := item.Get(container)
			if parts.IsArray() {
				for j, part := range parts.Array() {
					if part.Get("type").String() == "encrypted_content" {
						if part.Get("encrypted_content").Type != gjson.String || part.Get("encrypted_content").Str == "" {
							out.Shape.Ambiguous = true
							return out
						}
						entry.Paths = append(entry.Paths, path+"."+container+"."+strconv.Itoa(j))
					}
				}
			}
			if item.Get("type").String() == "message" {
				entry.Placeholder = collabMessage
				entry.Class = resilience.CollabItemClassUnreadMessage
			}
			if item.Get("type").String() == "agent_message" && len(entry.Paths) == 1 {
				kind := collabEnvelopeKind(item)
				if kind == "MESSAGE" || kind == "FINAL_ANSWER" {
					entry.Placeholder = collabMessage
					entry.Class = resilience.CollabItemClassUnreadMessage
				}
				if kind == "NEW_TASK" {
					// A task survives intermediate assistant output until a final
					// answer, compaction, or a newer task for the same recipient.
					active := true
					for _, later := range input[i+1:] {
						if later.Get("type").String() == "compaction" || later.Get("role").String() == "assistant" && later.Get("phase").String() == "final_answer" || later.Get("recipient").String() == item.Get("recipient").String() && collabEnvelopeKind(later) == "NEW_TASK" {
							active = false
							break
						}
					}
					if active {
						entry.Class = resilience.CollabItemClassProtected
					} else {
						entry.Class = resilience.CollabItemClassReplaceable
						entry.Placeholder = collabProcessedTask
					}
				}
			}
			if i < lastOutput {
				if collabEnvelopeKind(item) != "NEW_TASK" {
					entry.Class = resilience.CollabItemClassReplaceable
				}
				if entry.Placeholder == collabUnreadTask {
					entry.Placeholder = collabProcessedTask
				}
			}
			if len(entry.Paths) > 0 {
				if entry.Family == resilience.CollabFamilyToolOutput {
					out.Shape.ToolOutputCiphertext = true
				} else if item.Get("type").String() == "agent_message" {
					out.Shape.AgentCiphertext = true
				}
			}
		default:
			if bytes.Contains([]byte(item.Raw), []byte(`"encrypted_content"`)) {
				out.Shape.Ambiguous = true
				return out
			}
		}
		if len(entry.Paths) > 0 {
			out.Items = append(out.Items, entry)
		}
	}
	return out
}

func collabEnvelopeKind(item gjson.Result) string {
	if item.Get("type").String() != "agent_message" {
		return ""
	}
	var kind string
	for _, part := range item.Get("content").Array() {
		if part.Get("type").String() != "input_text" {
			continue
		}
		text := part.Get("text").String()
		if !strings.HasPrefix(text, "Message Type: ") {
			continue
		}
		if kind != "" || strings.Count(text, "Message Type: ") != 1 {
			return ""
		}
		line, _, _ := strings.Cut(text, "\n")
		candidate := strings.TrimPrefix(line, "Message Type: ")
		if candidate != "NEW_TASK" && candidate != "MESSAGE" && candidate != "FINAL_ANSWER" {
			return ""
		}
		if !strings.Contains(text, "\nTask name: ") || !strings.Contains(text, "\nSender: ") || !strings.HasSuffix(text, "\nPayload:\n") {
			return ""
		}
		kind = candidate
	}
	return kind
}

// Derive returns a fresh per-account body without learning or mutation. A
// claimed error can change only nested agent/tool ciphertext. Call arguments
// are never rewritten. Compaction is replaced, never removed.
func (p CollabBody) Derive(current []byte, action resilience.CollabAction, named string, claimed bool) ([]byte, bool) {
	if p.Shape.Ambiguous || p.Shape.ContinuationPointer {
		return current, false
	}
	next := current
	for _, item := range p.Items {
		if claimed && item.Family != resilience.CollabFamilyAgentMessage && item.Family != resilience.CollabFamilyToolOutput {
			continue
		}
		selected := false
		switch action {
		case resilience.CollabActionProbeReasoning:
			selected = item.Family == resilience.CollabFamilyReasoning
		case resilience.CollabActionNamedRepair:
			selected = item.Identity != "" && item.Identity == named
		case resilience.CollabActionDropUnprotected:
			selected = item.Class != resilience.CollabItemClassProtected || item.Family == resilience.CollabFamilyReasoning || item.Family == resilience.CollabFamilyCompaction
		case resilience.CollabActionReplaceAll:
			selected = true
		}
		if !selected {
			continue
		}
		for _, path := range item.Paths {
			var err error
			switch item.Family {
			case resilience.CollabFamilyReasoning:
				next, err = sjson.DeleteBytes(next, path)
			case resilience.CollabFamilyCompaction:
				if !gjson.GetBytes(next, path+".encrypted_content").Exists() {
					continue
				}
				replacement := map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": item.Placeholder}}}
				if item.Identity != "" {
					replacement["id"] = item.Identity
				}
				next, err = sjson.SetBytes(next, path, replacement)
			default:
				if gjson.GetBytes(next, path+".type").String() != "encrypted_content" {
					continue
				}
				next, err = sjson.SetBytes(next, path, map[string]string{"type": "input_text", "text": item.Placeholder})
			}
			if err != nil {
				return current, false
			}
		}
	}
	return next, !bytes.Equal(next, current)
}
