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
	WireID      string
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

// inputPositions maps a physical array back to the original identities. Legacy
// repairs can delete items. Without a unique mapping the optional path falls back.
func (p CollabBody) inputPositions(body []byte) (map[int]int, bool) {
	original := gjson.GetBytes(p.Original, "input").Array()
	current := gjson.GetBytes(body, "input").Array()
	positions := make(map[int]int, len(current))
	if len(original) == len(current) {
		ordered := true
		for i := range current {
			id, before := current[i].Get("id").String(), original[i].Get("id").String()
			if id != "" && before != "" && id != before {
				ordered = false
				break
			}
			positions[i] = i
		}
		if ordered {
			return positions, true
		}
		clear(positions)
	}
	used := map[int]bool{}
	for i, item := range current {
		match := -1
		for j, candidate := range original {
			id := item.Get("id").String()
			if id != "" && id == candidate.Get("id").String() || id == "" && item.Raw == candidate.Raw {
				if match != -1 || used[j] {
					return nil, false
				}
				match = j
			}
		}
		if match < 0 {
			return nil, false
		}
		positions[i], used[match] = match, true
	}
	return positions, true
}

func (p CollabBody) atBody(body []byte) (CollabBody, bool) {
	positions, ok := p.inputPositions(body)
	if !ok {
		return CollabBody{}, false
	}
	indices := make(map[int]int, len(positions))
	for current, original := range positions {
		indices[original] = current
	}
	p.Items = append([]CollabBodyItem(nil), p.Items...)
	items := p.Items[:0]
	for _, item := range p.Items {
		index, exists := indices[item.Index]
		if !exists {
			continue
		}
		oldPrefix := "input." + strconv.Itoa(item.Index)
		newPrefix := "input." + strconv.Itoa(index)
		item.Index = index
		item.Paths = append([]string(nil), item.Paths...)
		for i, path := range item.Paths {
			item.Paths[i] = newPrefix + strings.TrimPrefix(path, oldPrefix)
		}
		items = append(items, item)
	}
	p.Items = items
	return p, true
}

func (p CollabBody) ResolveEvidence(evidence resilience.CollabEvidence) resilience.CollabEvidence {
	if evidence.Named == nil {
		return evidence
	}
	items := make([]resilience.CollabOriginalItem, 0, len(p.Items))
	for _, item := range p.Items {
		items = append(items, resilience.CollabOriginalItem{Identity: item.Identity, Index: item.Index, Paths: item.Paths, Family: item.Family})
	}
	named := resilience.ResolveCollabNamedItem(*evidence.Named, items)
	named.Ambiguous = named.Ambiguous || !named.Located || p.Shape.Ambiguous
	evidence.Named = &named
	return evidence
}

// CollabBodyContext supplies request-local protection facts. Missing identity or
// version is conservative; consumption must refer to the original item.
type CollabBodyContext struct {
	Thread             string
	ModernTaskBoundary bool
	ClientVersion      string
	Consumed           map[string]bool
}

func ParseCollabBody(body []byte) CollabBody {
	return ParseCollabBodyWithContext(body, CollabBodyContext{})
}

func ParseCollabBodyWithContext(body []byte, facts CollabBodyContext) CollabBody {
	if facts.ClientVersion != "" {
		parts := strings.Split(strings.TrimPrefix(facts.ClientVersion, "v"), ".")
		if len(parts) >= 2 {
			major, e1 := strconv.Atoi(parts[0])
			minor, e2 := strconv.Atoi(parts[1])
			facts.ModernTaskBoundary = e1 == nil && e2 == nil && (major > 0 || minor >= 168)
		}
	}
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
		if collabModelOutput(item) {
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
		identity := id
		if identity == "" {
			identity = "@input:" + strconv.Itoa(i)
		}
		entry := CollabBodyItem{Identity: identity, WireID: id, Index: i, Class: resilience.CollabItemClassProtected, Placeholder: collabUnreadTask}
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
				entry.Family = resilience.CollabFamilyCallArgs
				out.Items = append(out.Items, entry)
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
					active := !(i <= lastOutput && facts.Thread != "" && item.Get("recipient").String() != "" && item.Get("recipient").String() != facts.Thread)
					// Unread tasks in one batch are all protected.
					if i <= lastOutput {
						for _, later := range input[i+1:] {
							if later.Get("type").String() == "compaction" && (facts.ModernTaskBoundary || facts.Consumed[identity] || facts.Thread != "" && item.Get("recipient").String() != "" && item.Get("recipient").String() != facts.Thread) || later.Get("role").String() == "assistant" && later.Get("phase").String() == "final_answer" || later.Get("recipient").String() == item.Get("recipient").String() && collabEnvelopeKind(later) == "NEW_TASK" {
								active = false
								break
							}
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
				if item.Get("type").String() != "agent_message" || collabEnvelopeKind(item) != "" && collabEnvelopeKind(item) != "NEW_TASK" {
					entry.Class = resilience.CollabItemClassReplaceable
				}
				if entry.Class == resilience.CollabItemClassReplaceable && entry.Placeholder == collabUnreadTask {
					entry.Placeholder = collabProcessedTask
				}
			}
			if facts.Consumed[identity] && !(item.Get("type").String() == "agent_message" && collabEnvelopeKind(item) == "NEW_TASK") {
				entry.Class = resilience.CollabItemClassReplaceable
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
		lines := strings.Split(text, "\n")
		if len(lines) != 5 || !strings.HasPrefix(lines[1], "Task name: ") || strings.TrimSpace(strings.TrimPrefix(lines[1], "Task name: ")) == "" || !strings.HasPrefix(lines[2], "Sender: ") || strings.TrimSpace(strings.TrimPrefix(lines[2], "Sender: ")) == "" || lines[3] != "Payload:" || lines[4] != "" {
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
	var ok bool
	p, ok = p.atBody(current)
	if !ok {
		return current, false
	}
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
			selected = item.Class != resilience.CollabItemClassProtected || item.Family == resilience.CollabFamilyReasoning
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
				if item.WireID != "" {
					replacement["id"] = item.WireID
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

func collabModelOutput(item gjson.Result) bool {
	if item.Get("role").String() == "assistant" {
		return true
	}
	switch item.Get("type").String() {
	case "compaction", "reasoning", "function_call", "custom_tool_call", "web_search_call", "file_search_call", "computer_call", "code_interpreter_call", "image_generation_call", "local_shell_call", "shell_call", "apply_patch_call", "mcp_call", "mcp_list_tools", "mcp_approval_request":
		return true
	}
	return false
}
