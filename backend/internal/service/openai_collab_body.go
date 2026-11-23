package service

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

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

// CollabBodyItem retains original identities. A physical request resolves its
// locators before any edit. Unknown message envelopes are protected.
type CollabBodyItem struct {
	Identity    string
	WireID      string
	Index       int
	Family      resilience.CollabFamily
	Class       resilience.CollabItemClass
	Paths       []string
	references  []string // exact locators, enumerated from this physical request
	Placeholder string
}

type CollabBody struct {
	lineage  *collabInputLineage
	Original []byte
	Items    []CollabBodyItem
	Shape    resilience.CollabRequestShape
}

// inputPositions maps a physical array back to the original identities. Legacy
// repairs can delete items. Without a unique mapping the optional path falls back.
func (p CollabBody) inputPositions(body []byte) (map[int]int, bool) {
	original := gjson.GetBytes(p.Original, "input").Array()
	current := gjson.GetBytes(body, "input").Array()
	// Outbound normalization may independently strip invalid IDs and namespaces.
	// Keep every exact form while preserving the original occurrence identity.
	comparable := make([][]string, len(original))
	for i, item := range original {
		comparable[i] = collabComparableInputForms(item)
	}
	candidates := make([][]int, len(current))
	duplicates := make([][]int, len(current))
	for i, item := range current {
		id := item.Get("id").String()
		for j, candidate := range original {
			if id != "" && id == candidate.Get("id").String() || id == "" && (slices.Contains(comparable[j], item.Raw) || p.lineage.matches(item.Raw, j)) {
				candidates[i] = append(candidates[i], j)
			}
		}
		if len(candidates[i]) == 0 && id == "" {
			for j, candidate := range original {
				if candidate.Get("id").String() == "" && collabRepairedItemMatches(candidate, item) {
					candidates[i] = append(candidates[i], j)
				}
			}
		}
	}
	// Normalization must not erase the distinction between different originals.
	// Check exact normalized forms before assignment; structural repair
	// candidates may still be disambiguated by an unchanged sibling.
	for i, item := range current {
		if item.Get("id").String() != "" {
			continue
		}
		matched := -1
		for _, j := range candidates[i] {
			if !slices.Contains(comparable[j], item.Raw) {
				continue
			}
			if matched >= 0 && original[j].Raw != original[matched].Raw {
				return nil, false
			}
			matched = j
		}
	}
	// A known variant can be reached independently by another identical
	// occurrence. Keep those alternatives for collisions, without displacing
	// the stronger lineage match while it is still available.
	for i, options := range candidates {
		if current[i].Get("id").String() != "" {
			continue
		}
		for j := range original {
			for _, known := range options {
				if original[j].Raw == original[known].Raw {
					duplicates[i] = append(duplicates[i], j)
					break
				}
			}
		}
	}
	positions := make(map[int]int, len(current))
	used := map[int]bool{}
	// Resolve constrained lineage first, before indistinguishable originals can
	// consume its occurrence. Equal remaining digests retain occurrence order.
	for len(positions) < len(current) {
		selected := -1
		for i, options := range candidates {
			if _, done := positions[i]; done {
				continue
			}
			available := options[:0]
			for _, j := range options {
				if !used[j] {
					available = append(available, j)
				}
			}
			if len(available) == 0 {
				for _, j := range duplicates[i] {
					if !used[j] {
						available = append(available, j)
					}
				}
			}
			candidates[i] = available
			if len(available) == 0 {
				return nil, false
			}
			if selected < 0 || len(available) < len(candidates[selected]) {
				selected = i
			}
		}
		options := candidates[selected]
		for _, j := range options[1:] {
			if original[j].Raw != original[options[0]].Raw {
				return nil, false
			}
		}
		positions[selected], used[options[0]] = options[0], true
	}

	return positions, true
}

func collabComparableInputForms(item gjson.Result) []string {
	forms := []string{item.Raw}
	id := item.Get("id")
	if id.Type == gjson.String && shouldStripOpenAIResponsesInputItemID(item.Get("type").String(), id.Str) {
		if stripped, err := sjson.Delete(item.Raw, "id"); err == nil {
			forms = append(forms, stripped)
		}
	}
	if item.Get("namespace").Exists() {
		for _, form := range forms[:len(forms):len(forms)] {
			if stripped, err := sjson.Delete(form, "namespace"); err == nil {
				forms = append(forms, stripped)
			}
		}
	}
	return forms
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
		item.references = collabItemReferences(gjson.GetBytes(body, "input").Array()[index], index)
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
		items = append(items, resilience.CollabOriginalItem{Identity: item.Identity, Anonymous: item.WireID == "", Index: item.Index, Paths: item.references, Family: item.Family})
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
	out := CollabBody{Original: body, lineage: &collabInputLineage{items: map[[32]byte]map[int]bool{}}}
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
		// The existing protocol repair rewrites standalone tool outputs in
		// place and drops their ID. Register its exact output against the
		// original identity; never infer that identity from a later position.
		if item.Get("type").String() == "function_call_output" && item.Get("output").Type == gjson.String {
			single := []byte(`{"input":[` + item.Raw + `]}`)
			rules := DefaultOpenAIFCORules([]string{item.Get("name").String()})
			if repaired, summary, err := normalizeOpenAIFCOBody(single, rules); err == nil && summary.Matched == 1 {
				out.lineage.remember(gjson.GetBytes(repaired, "input.0").Raw, i)
			}
		}
		if id == "" && item.Get("content").Exists() && item.Get("content").Type == gjson.Null {
			single := []byte(`{"input":[` + item.Raw + `]}`)
			if repaired, _, changed, err := normalizeOpenAIResponsesRejectedNullContentAtIndex(single, 0); err == nil && changed {
				out.lineage.remember(gjson.GetBytes(repaired, "input.0").Raw, i)
			}
		}
		if id == "" && item.Get("type").String() == "tool_search_call" {
			single := []byte(`{"input":[` + item.Raw + `]}`)
			if repaired, _, changed, err := convertOpenAIResponsesToolSearchCallArguments(single, 0); err == nil && changed {
				out.lineage.remember(gjson.GetBytes(repaired, "input.0").Raw, i)
			}
		}
		entry := CollabBodyItem{Identity: identity, WireID: id, Index: i, references: collabItemReferences(item, i), Class: resilience.CollabItemClassProtected, Placeholder: collabUnreadTask}
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
			// Treat these bytes as not decrypted upstream and leave them unchanged.
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
					active := i > lastOutput || facts.Thread == "" || item.Get("recipient").String() == "" || item.Get("recipient").String() == facts.Thread
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
			if facts.Consumed[identity] && (item.Get("type").String() != "agent_message" || collabEnvelopeKind(item) != "NEW_TASK") {
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
	positions, _ := p.inputPositions(current)
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
	if p.lineage != nil {
		for i, item := range gjson.GetBytes(next, "input").Array() {
			if item.Get("id").String() == "" {
				p.lineage.remember(item.Raw, positions[i])
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

// collabItemReferences enumerates both upstream locator spellings from the
// request tree. Scalars have no descendants; unusual object keys have no locator.
func collabItemReferences(item gjson.Result, index int) []string {
	var references []string
	var visit func(gjson.Result, string, string)
	visit = func(value gjson.Result, bracket, dotted string) {
		references = append(references, bracket, dotted)
		if value.IsObject() {
			value.ForEach(func(key, child gjson.Result) bool {
				if !strings.ContainsAny(key.Str, ".\\:*?[] \t\r\n'\"`/") {
					visit(child, bracket+"."+key.Str, dotted+"."+key.Str)
				}
				return true
			})
		} else if value.IsArray() {
			for i, child := range value.Array() {
				n := strconv.Itoa(i)
				visit(child, bracket+"["+n+"]", dotted+"."+n)
			}
		}
	}
	visit(item, "input["+strconv.Itoa(index)+"]", "input."+strconv.Itoa(index))
	return references
}

// Anonymous identity is a digest plus its occurrence among equal original
// digests. The displayed @input label remains only a request-local handle.
func collabInputIdentities(body []byte) []string {
	counts := map[[32]byte]int{}
	var identities []string
	for _, item := range gjson.GetBytes(body, "input").Array() {
		id := item.Get("id").String()
		if id != "" {
			identities = append(identities, "id:"+id)
			continue
		}
		digest := sha256.Sum256([]byte(item.Raw))
		identities = append(identities, fmt.Sprintf("digest:%x:%d", digest, counts[digest]))
		counts[digest]++
	}
	return identities
}

type collabInputLineage struct {
	mu       sync.Mutex
	items    map[[32]byte]map[int]bool
	variants map[int][]string
}

func (l *collabInputLineage) matches(raw string, index int) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if known := l.items[sha256.Sum256([]byte(raw))]; len(known) > 0 {
		// Recorded derivations take precedence over structural similarities to
		// other occurrences, including an unchanged copy of the same message.
		return known[index]
	}
	for _, prior := range l.variants[index] {
		if collabRepairedItemMatches(gjson.Parse(prior), gjson.Parse(raw)) {
			return true
		}
	}
	return false
}
func (l *collabInputLineage) remember(raw string, index int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	digest := sha256.Sum256([]byte(raw))
	if l.items[digest] == nil {
		l.items[digest] = map[int]bool{}
	}
	if !l.items[digest][index] {
		if l.variants == nil {
			l.variants = map[int][]string{}
		}
		l.variants[index] = append(l.variants[index], raw)
	}
	l.items[digest][index] = true
}
func collabRepairedItemMatches(before, after gjson.Result) bool {
	if before.Raw == after.Raw {
		return true
	}
	if !before.IsObject() || !after.IsObject() || after.Get("type").String() == "" || before.Get("type").String() != after.Get("type").String() {
		return false
	}
	return collabNestedRepairsMatch(before, after)
}

// Compare structure recursively, retaining the original item's identity even
// when a protocol repair removes a field or changes a boolean option inside
// content or an array element. Text and identity-bearing strings still match
// exactly; a setter must not make different messages interchangeable.
// Ambiguous matches between different originals are rejected by inputPositions.
func collabNestedRepairsMatch(before, after gjson.Result) bool {
	if before.Raw == after.Raw {
		return true
	}
	if (before.Type == gjson.True || before.Type == gjson.False) && (after.Type == gjson.True || after.Type == gjson.False) {
		return true
	}
	if before.IsObject() && after.IsObject() {
		previous := before.Map()
		valid := true
		after.ForEach(func(key, value gjson.Result) bool {
			prior, exists := previous[key.Str]
			kind := before.Get("type").String()
			if exists && (kind == "agent_message" && key.Str == "content" || kind == "function_call_output" && key.Str == "output") && prior.IsArray() && value.IsArray() {
				valid = collabLegacyPartsMatch(prior.Array(), value.Array())
			} else {
				valid = exists && collabNestedRepairsMatch(prior, value)
			}
			return valid
		})
		return valid
	}
	if before.IsArray() && after.IsArray() {
		previous, next := before.Array(), after.Array()
		at := 0
		for _, item := range next {
			for at < len(previous) && !collabNestedRepairsMatch(previous[at], item) {
				at++
			}
			if at == len(previous) {
				return false
			}
			at++
		}
		return true
	}
	return false
}

// ProtectedNamedItem identifies the protected handled-item endpoint for callers that
// must stop before constructing a controller (for example an expired budget).
func (p CollabBody) ProtectedNamedItem(e resilience.CollabEvidence) bool {
	e = p.ResolveEvidence(e)
	return e.Named != nil && e.Named.Located && collabProtectedItem(p, e.Named.Identity)
}

// fullyDisposed requires a recognizable placeholder for every replaceable
// unreadable item, with at least one such item. Immutable call arguments neither
// establish nor prevent disposal; unmapped or removed items cannot prove it.
func (p CollabBody) fullyDisposed(body []byte) bool {
	physical, ok := p.atBody(body)
	if !ok || len(p.Items) == 0 || len(physical.Items) != len(p.Items) {
		return false
	}
	seen := false
	for _, item := range physical.Items {
		for _, path := range item.Paths {
			seen = true
			value := gjson.GetBytes(body, path)
			switch item.Family {
			case resilience.CollabFamilyReasoning:
				if value.Exists() {
					return false
				}
			case resilience.CollabFamilyCompaction:
				if value.Get("type").String() != "message" || value.Get("content.0.text").String() != item.Placeholder {
					return false
				}
			case resilience.CollabFamilyAgentMessage, resilience.CollabFamilyToolOutput:
				text := value.Get("text").String()
				if value.Get("type").String() != "input_text" || (text != item.Placeholder && text != "[encrypted content omitted]") {
					return false
				}
			default:
				return false
			}
		}
	}
	return seen
}

// Legacy ciphertext repair changes only these protocol parts. Match its exact
// placeholder while retaining all other scalar fields and relative part order.
func collabLegacyPartsMatch(before, after []gjson.Result) bool {
	at := 0
	for _, part := range after {
		matched := false
		for at < len(before) {
			prior := before[at]
			at++
			replacement := prior.Get("type").String() == "encrypted_content" && prior.Get("encrypted_content").Type == gjson.String &&
				part.Get("type").String() == "input_text" && part.Get("text").String() == "[encrypted content omitted]" && len(part.Map()) == 2
			if replacement || collabNestedRepairsMatch(prior, part) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
