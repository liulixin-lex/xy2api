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

func (p CollabBody) ResolveEvidence(evidence resilience.CollabEvidence) resilience.CollabEvidence {
	if evidence.Named == nil || evidence.Named.InputIndex == nil {
		return evidence
	}
	named := *evidence.Named
	evidence.Named = &named
	index := *named.InputIndex
	var located *CollabBodyItem
	for i := range p.Items {
		if p.Items[i].Index == index {
			located = &p.Items[i]
			break
		}
	}
	if located == nil || named.Identity != "" && named.Identity != located.Identity {
		named.Ambiguous = true
		return evidence
	}
	if named.Path != "" {
		path := strings.NewReplacer("[", ".", "]", "").Replace(named.Path)
		base := "input." + strconv.Itoa(index)
		valid := path == base
		for _, candidate := range located.Paths {
			valid = valid || path == candidate || path == candidate+".encrypted_content"
		}
		if located.Family == resilience.CollabFamilyCallArgs {
			valid = valid || path == base+".encrypted_function_args" || path == base+".arguments"
		}
		if !valid {
			named.Ambiguous = true
		}
	}
	named.Identity = located.Identity
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
					active := true
					// Unread tasks in one batch are all protected.
					if i <= lastOutput {
						for _, later := range input[i+1:] {
							if later.Get("type").String() == "compaction" && (facts.ModernTaskBoundary || facts.Thread != "" && item.Get("recipient").String() != "" && item.Get("recipient").String() != facts.Thread) || later.Get("role").String() == "assistant" && later.Get("phase").String() == "final_answer" || later.Get("recipient").String() == item.Get("recipient").String() && collabEnvelopeKind(later) == "NEW_TASK" {
								active = false
								break
							}
						}
					}
					if facts.Consumed[identity] {
						active = false
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
			if facts.Consumed[identity] {
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
