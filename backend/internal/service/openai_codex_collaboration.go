package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexCollaborationNamespace      = "collaboration"
	codexCollaborationNamespaceAlias = "collaboration-xy"
	codexCollabAttemptContextKey     = "openai_codex_collaboration_attempt"
	codexCollabOpaqueMinLen          = 24
)

var codexCollabMessageTools = map[string]struct{}{
	"spawn_agent":   {},
	"send_message":  {},
	"followup_task": {},
}

type codexCollabOutcome string

const (
	codexCollabApplied     codexCollabOutcome = "applied"
	codexCollabAbsent      codexCollabOutcome = "absent"
	codexCollabConflict    codexCollabOutcome = "conflict"
	codexCollabUnsupported codexCollabOutcome = "unsupported"
	codexCollabInvalid     codexCollabOutcome = "invalid"
)

type codexCollabPlan struct {
	// tools are the function names declared by the transformed namespace.
	// messageTools contains only tools whose encrypted marker was removed.
	tools           map[string]struct{}
	messageTools    map[string]struct{}
	extraEncrypted  map[string]bool
	devTextRewrites int
}

type codexCollabRestorer struct {
	plan  *codexCollabPlan
	stats codexCollabStats
	items map[string]*codexCollabItemState
}

type codexCollabItemState struct {
	sawNonEmpty          bool
	decision             codexCollabMarkerDecision
	counted              bool
	contradictionCounted bool
	vetoCounted          bool
	uncoveredCounted     bool
	finalArguments       string
	anonymous            bool
	tool                 string
	itemType             string
	efaKind              codexCollabMarkerDecision
	messageKind          int
}

type codexCollabMarkerDecision uint8

const (
	codexCollabMarkerUndecided codexCollabMarkerDecision = iota
	codexCollabMarkerKeep
	codexCollabMarkerAdd
	codexCollabMarkerVeto
)

type codexCollabStats struct {
	Outcome                                      codexCollabOutcome
	Applied                                      bool
	Calls                                        int
	EFAAbsent, EFAEmpty, EFANonEmpty             int
	MarkerAdded, Vetoed, Contradicted, Uncovered int
	DevTextRewrites                              int
	MsgOpaque, MsgText                           int
}

type codexCollabNamespace struct {
	path  string
	tools gjson.Result
	node  gjson.Result
}

type codexCollabItem struct {
	path           string
	index          int
	result         gjson.Result
	countFinal     bool
	argumentsEvent bool
}

type codexCollabJSONEdit struct {
	path   string
	value  any
	delete bool
}

// applyCodexCollaborationPlaintext renames the collaboration namespace and
// removes the encryption marker from only the three supported message tools.
// All checks run before edits are applied so failures leave body untouched.
func applyCodexCollaborationPlaintext(body []byte) (out []byte, plan *codexCollabPlan, outcome codexCollabOutcome) {
	if !json.Valid(body) || !gjson.ParseBytes(body).IsObject() {
		return body, nil, codexCollabInvalid
	}
	root := gjson.ParseBytes(body)
	if !codexCollabUniqueObject(root) {
		return body, nil, codexCollabInvalid
	}
	if previous := root.Get("previous_response_id"); previous.Exists() && previous.Type != gjson.Null && strings.TrimSpace(previous.String()) != "" {
		return body, nil, codexCollabUnsupported
	}
	if root.Get("store").Bool() {
		return body, nil, codexCollabUnsupported
	}
	if !codexCollabRequestContainersUnique(root) {
		return body, nil, codexCollabConflict
	}
	if codexCollabUnsupportedShape(root) {
		return body, nil, codexCollabUnsupported
	}

	namespaces := codexCollabFindNamespaces(root)
	if len(namespaces) == 0 {
		return body, nil, codexCollabAbsent
	}
	if codexCollabHasPrivateName(root) {
		return body, nil, codexCollabConflict
	}
	textEdits, textRewrites, err := codexCollabDeveloperTextEdits(root)
	if err != nil {
		return body, nil, codexCollabConflict
	}

	if !namespaces[0].tools.IsArray() {
		return body, nil, codexCollabConflict
	}
	if len(namespaces) > 1 {
		canonicalSchema, err := codexCollabCanonicalJSON(namespaces[0].node.Raw)
		if err != nil {
			return body, nil, codexCollabConflict
		}
		for _, namespace := range namespaces[1:] {
			candidate, err := codexCollabCanonicalJSON(namespace.node.Raw)
			if !namespace.tools.IsArray() || err != nil || !bytes.Equal(candidate, canonicalSchema) {
				return body, nil, codexCollabConflict
			}
		}
	}
	for _, namespace := range namespaces {
		if !codexCollabUnambiguousJSON(namespace.node) {
			return body, nil, codexCollabConflict
		}
	}
	for _, item := range root.Get("input").Array() {
		if item.Get("type").String() == "function_call" && !codexCollabUniqueObject(item) {
			return body, nil, codexCollabConflict
		}
	}
	if !codexCollabUnambiguousJSON(root.Get("tool_choice")) {
		return body, nil, codexCollabConflict
	}

	newPlan := &codexCollabPlan{
		tools:           make(map[string]struct{}),
		messageTools:    make(map[string]struct{}),
		extraEncrypted:  make(map[string]bool),
		devTextRewrites: textRewrites,
	}
	edits := textEdits
	for _, namespace := range namespaces {
		edits = append(edits, codexCollabJSONEdit{path: namespace.path + ".name", value: codexCollaborationNamespaceAlias})
		declared := make(map[string]struct{})
		for index, tool := range namespace.tools.Array() {
			name := tool.Get("name").String()
			if tool.Get("name").Type != gjson.String || name == "" {
				if tool.Get("type").String() == "function" {
					return body, nil, codexCollabConflict
				}
				continue
			}
			if _, duplicate := declared[name]; duplicate {
				return body, nil, codexCollabConflict
			}
			declared[name] = struct{}{}
			if strings.TrimSpace(tool.Get("type").String()) != "function" {
				continue
			}
			newPlan.tools[name] = struct{}{}
			if _, isMessageTool := codexCollabMessageTools[name]; !isMessageTool {
				continue
			}
			toolPath := fmt.Sprintf("%s.tools.%d", namespace.path, index)
			message := tool.Get("parameters.properties.message")
			if !tool.Get("parameters").IsObject() || !tool.Get("parameters.properties").IsObject() || !message.IsObject() || message.Get("type").Type != gjson.String || message.Get("type").String() != "string" {
				return body, nil, codexCollabConflict
			}
			for _, keyword := range []string{"anyOf", "oneOf", "allOf", "$ref"} {
				if message.Get(keyword).Exists() || tool.Get("parameters").Get(keyword).Exists() {
					return body, nil, codexCollabConflict
				}
			}
			encrypted := message.Get("encrypted")
			if encrypted.Exists() && encrypted.Type != gjson.True && encrypted.Type != gjson.False {
				return body, nil, codexCollabConflict
			}
			if encrypted.Bool() {
				edits = append(edits, codexCollabJSONEdit{path: toolPath + ".parameters.properties.message.encrypted", delete: true})
				newPlan.messageTools[name] = struct{}{}
				properties := tool.Get("parameters.properties")
				properties.ForEach(func(key, value gjson.Result) bool {
					if key.String() != "message" && codexCollabSchemaEncrypted(value) {
						newPlan.extraEncrypted[name] = true
					}
					return true
				})
			}
		}
	}
	edits = append(edits, codexCollabReferenceEdits(root)...)

	changed := append([]byte(nil), body...)
	for _, edit := range edits {
		if edit.delete {
			changed, err = sjson.DeleteBytes(changed, edit.path)
		} else {
			changed, err = sjson.SetBytes(changed, edit.path, edit.value)
		}
		if err != nil {
			return body, nil, codexCollabInvalid
		}
	}
	return changed, newPlan, codexCollabApplied
}

func codexCollabRequestContainersUnique(root gjson.Result) bool {
	var checkTools func(gjson.Result) bool
	checkTools = func(tools gjson.Result) bool {
		for _, tool := range tools.Array() {
			if !codexCollabUniqueObject(tool) || !checkTools(tool.Get("tools")) {
				return false
			}
		}
		return true
	}
	if !checkTools(root.Get("tools")) || !codexCollabUnambiguousJSON(root.Get("tool_choice")) {
		return false
	}
	for _, item := range root.Get("input").Array() {
		if !codexCollabUniqueObject(item) || !checkTools(item.Get("tools")) {
			return false
		}
	}
	return true
}

func codexCollabUnsupportedShape(root gjson.Result) bool {
	if choice := root.Get("tool_choice"); choice.Exists() && (choice.Type != gjson.String || (choice.String() != "auto" && choice.String() != "none" && choice.String() != "required")) {
		return true
	}
	if root.Get("functions").Exists() {
		return true
	}
	var chatTools func(gjson.Result) bool
	chatTools = func(tools gjson.Result) bool {
		for _, tool := range tools.Array() {
			if tool.Get("function").Exists() || chatTools(tool.Get("tools")) {
				return true
			}
		}
		return false
	}
	if chatTools(root.Get("tools")) {
		return true
	}
	for _, item := range root.Get("input").Array() {
		if item.Get("function").Exists() || chatTools(item.Get("tools")) {
			return true
		}
		if item.Get("type").String() == "function_call" {
			name := item.Get("name").String()
			if strings.HasPrefix(name, codexCollaborationNamespace+".") || strings.HasPrefix(name, codexCollaborationNamespace+"__") {
				return true
			}
		}
	}
	return false
}

// Only these system prompt carriers may have tool-address examples rewritten.
// User messages, historical tool payloads, and other text stay opaque.
func codexCollabDeveloperTextEdits(root gjson.Result) ([]codexCollabJSONEdit, int, error) {
	const original = "functions.collaboration."
	const alias = "functions.collaboration-xy."
	var edits []codexCollabJSONEdit
	rewrites := 0
	appendText := func(node gjson.Result, path string) error {
		if node.Type != gjson.String {
			return nil
		}
		text := node.String()
		if strings.Contains(text, codexCollaborationNamespaceAlias) {
			return errors.New("private collaboration namespace in system prompt")
		}
		if count := strings.Count(text, original); count > 0 {
			edits = append(edits, codexCollabJSONEdit{path: path, value: strings.ReplaceAll(text, original, alias)})
			rewrites += count
		}
		return nil
	}
	if err := appendText(root.Get("instructions"), "instructions"); err != nil {
		return nil, 0, err
	}
	for index, item := range root.Get("input").Array() {
		if item.Get("type").String() != "message" {
			continue
		}
		role := item.Get("role").String()
		if role != "developer" && role != "system" {
			continue
		}
		if !codexCollabUniqueObject(item) {
			return nil, 0, errors.New("ambiguous collaboration system message")
		}
		path := fmt.Sprintf("input.%d.content", index)
		content := item.Get("content")
		if err := appendText(content, path); err != nil {
			return nil, 0, err
		}
		if !content.IsArray() {
			continue
		}
		for partIndex, part := range content.Array() {
			if part.Get("type").String() != "input_text" {
				continue
			}
			if !codexCollabUniqueObject(part) {
				return nil, 0, errors.New("ambiguous collaboration system text")
			}
			if err := appendText(part.Get("text"), fmt.Sprintf("%s.%d.text", path, partIndex)); err != nil {
				return nil, 0, err
			}
		}
	}
	return edits, rewrites, nil
}

func codexCollabFindNamespaces(root gjson.Result) []codexCollabNamespace {
	var found []codexCollabNamespace
	appendFromTools := func(tools gjson.Result, path string) {
		if !tools.IsArray() {
			return
		}
		for index, tool := range tools.Array() {
			if strings.TrimSpace(tool.Get("type").String()) != "namespace" || tool.Get("name").String() != codexCollaborationNamespace {
				continue
			}
			found = append(found, codexCollabNamespace{
				path:  fmt.Sprintf("%s.%d", path, index),
				tools: tool.Get("tools"),
				node:  tool,
			})
		}
	}
	appendFromTools(root.Get("tools"), "tools")
	input := root.Get("input")
	if input.IsArray() {
		for index, item := range input.Array() {
			if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
				continue
			}
			appendFromTools(item.Get("tools"), fmt.Sprintf("input.%d.tools", index))
		}
	}
	return found
}

func codexCollabHasPrivateName(root gjson.Result) bool {
	var scanTools func(gjson.Result) bool
	scanTools = func(tools gjson.Result) bool {
		if !tools.IsArray() {
			return false
		}
		for _, tool := range tools.Array() {
			typ := strings.TrimSpace(tool.Get("type").String())
			name := tool.Get("name").String()
			if typ == "namespace" && name == codexCollaborationNamespaceAlias {
				return true
			}
			if (typ == "function" || typ == "custom") && (codexCollabHasAliasPrefix(name) || tool.Get("namespace").String() == codexCollaborationNamespaceAlias) {
				return true
			}
			if typ == "namespace" && scanTools(tool.Get("tools")) {
				return true
			}
		}
		return false
	}
	if scanTools(root.Get("tools")) {
		return true
	}
	input := root.Get("input")
	if input.IsArray() {
		for _, item := range input.Array() {
			typ := strings.TrimSpace(item.Get("type").String())
			if typ == "additional_tools" && scanTools(item.Get("tools")) {
				return true
			}
			if (typ == "function_call" || typ == "custom_tool_call") && (item.Get("namespace").String() == codexCollaborationNamespaceAlias || codexCollabHasAliasPrefix(item.Get("name").String())) {
				return true
			}
		}
	}
	return false
}

func codexCollabReferenceEdits(root gjson.Result) []codexCollabJSONEdit {
	var edits []codexCollabJSONEdit
	input := root.Get("input")
	if input.IsArray() {
		for index, item := range input.Array() {
			if item.Get("type").String() == "function_call" && item.Get("namespace").String() == codexCollaborationNamespace {
				edits = append(edits, codexCollabJSONEdit{path: fmt.Sprintf("input.%d.namespace", index), value: codexCollaborationNamespaceAlias})
			}
		}
	}
	return edits
}

func codexCollabRestoreAliasName(name string) (string, bool) {
	for _, separator := range []string{".", "__"} {
		prefix := codexCollaborationNamespaceAlias + separator
		if strings.HasPrefix(name, prefix) {
			return strings.TrimPrefix(name, prefix), true
		}
	}
	return name, false
}

func codexCollabHasAliasPrefix(name string) bool {
	return strings.HasPrefix(name, codexCollaborationNamespaceAlias+".") || strings.HasPrefix(name, codexCollaborationNamespaceAlias+"__")
}

func codexCollabCanonicalJSON(raw string) ([]byte, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func codexCollabSchemaEncrypted(node gjson.Result) bool {
	if node.IsObject() && node.Get("encrypted").Bool() {
		return true
	}
	if !node.IsObject() && !node.IsArray() {
		return false
	}
	found := false
	node.ForEach(func(_, value gjson.Result) bool {
		found = codexCollabSchemaEncrypted(value)
		return !found
	})
	return found
}

func newCodexCollabRestorer(plan *codexCollabPlan) *codexCollabRestorer {
	r := &codexCollabRestorer{plan: plan, items: make(map[string]*codexCollabItemState)}
	if plan == nil {
		r.stats.Outcome = codexCollabAbsent
		return r
	}
	r.stats.Outcome = codexCollabApplied
	r.stats.Applied = true
	r.stats.DevTextRewrites = plan.devTextRewrites
	return r
}

func (r *codexCollabRestorer) SSEData(eventType string, data []byte) ([]byte, error) {
	if r == nil {
		return data, errors.New("nil collaboration restorer")
	}
	return r.transform(data, strings.TrimSpace(eventType), true)
}

func (r *codexCollabRestorer) ResponseJSON(body []byte) ([]byte, error) {
	if r == nil {
		return body, errors.New("nil collaboration restorer")
	}
	return r.transform(body, "response.json", false)
}

// ErrorBody restores only protocol error text; callers retain the original body
// for classification and logging. Malformed or ambiguous bodies stay untouched.
func (r *codexCollabRestorer) ErrorBody(body []byte) []byte {
	if r == nil || r.plan == nil || !json.Valid(body) {
		return body
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() || !codexCollabUniqueObject(root) || !codexCollabUniqueObject(root.Get("response")) {
		return body
	}
	changed := body
	for _, base := range []string{"error", "response.error"} {
		object := root.Get(base)
		if !codexCollabUniqueObject(object) {
			return body
		}
		for _, field := range []string{"message", "param"} {
			path := base + "." + field
			value := root.Get(path)
			if value.Type != gjson.String {
				continue
			}
			restored := codexCollabRestoreErrorText(value.String())
			if restored != value.String() {
				var err error
				changed, err = sjson.SetBytes(changed, path, restored)
				if err != nil {
					return body
				}
			}
		}
	}
	return changed
}

func codexCollabRestoreErrorText(value string) string {
	const alias = codexCollaborationNamespaceAlias
	var out strings.Builder
	for start := 0; start < len(value); {
		index := strings.Index(value[start:], alias)
		if index < 0 {
			_, _ = out.WriteString(value[start:])
			break
		}
		index += start
		end := index + len(alias)
		boundary := func(b byte) bool {
			return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
		}
		beforeOK := index == 0 || !boundary(value[index-1])
		afterOK := end == len(value) || !boundary(value[end]) || strings.HasPrefix(value[end:], "__")
		_, _ = out.WriteString(value[start:index])
		if beforeOK && afterOK {
			_, _ = out.WriteString(codexCollaborationNamespace)
		} else {
			_, _ = out.WriteString(alias)
		}
		start = end
	}
	return out.String()
}

func (r *codexCollabRestorer) Stats() codexCollabStats {
	if r == nil {
		return codexCollabStats{}
	}
	return r.stats
}

// transform walks protocol containers only. All byte edits use sjson so opaque
// values (including escaped argument strings and tool output) stay byte exact.
func (r *codexCollabRestorer) transform(body []byte, eventType string, streaming bool) ([]byte, error) {
	if bytes.Equal(bytes.TrimSpace(body), []byte("[DONE]")) && streaming {
		return body, nil
	}
	if !json.Valid(body) || !gjson.ParseBytes(body).IsObject() {
		if r.plan == nil {
			return body, nil
		}
		return nil, errors.New("invalid collaboration response JSON")
	}
	root := gjson.ParseBytes(body)
	if r.plan != nil && (!codexCollabUniqueObject(root) || !codexCollabUniqueObject(root.Get("response"))) {
		return nil, errors.New("ambiguous collaboration response JSON fields")
	}
	if eventType == "" {
		eventType = root.Get("type").String()
	}
	if streaming && (eventType == "error" || eventType == "response.failed" || root.Get("type").String() == "error" || root.Get("type").String() == "response.failed") {
		body = r.ErrorBody(body)
		root = gjson.ParseBytes(body)
	}
	items, err := codexCollabResponseItems(root, eventType, streaming)
	if err != nil {
		if r.plan == nil {
			return body, nil
		}
		return nil, err
	}
	updated := body
	for _, item := range items {
		updated, err = r.restoreItem(updated, item)
		if err != nil {
			return nil, err
		}
	}
	return r.restoreResponseEchoes(updated, root)
}

func codexCollabResponseItems(root gjson.Result, eventType string, streaming bool) ([]codexCollabItem, error) {
	var items []codexCollabItem
	index := -1
	if value := root.Get("output_index"); value.Type == gjson.Number && value.Int() >= 0 {
		index = int(value.Int())
	}
	if item := root.Get("item"); item.Exists() {
		if !item.IsObject() {
			return nil, errors.New("invalid collaboration event item")
		}
		items = append(items, codexCollabItem{path: "item", index: index, result: item,
			countFinal: !streaming || eventType == "response.output_item.done"})
	}
	if strings.HasPrefix(eventType, "response.function_call_arguments.") || strings.HasPrefix(eventType, "response.custom_tool_call_input.") {
		items = append(items, codexCollabItem{index: index, result: root, argumentsEvent: true})
	}
	final := !streaming || eventType == "response.completed" || eventType == "response.done"
	for _, base := range []string{"response.output", "output"} {
		array := root.Get(base)
		if !array.Exists() || array.Type == gjson.Null {
			continue
		}
		if !array.IsArray() {
			return nil, errors.New("invalid collaboration response output")
		}
		for outputIndex, item := range array.Array() {
			if !item.IsObject() {
				return nil, errors.New("invalid collaboration output item")
			}
			items = append(items, codexCollabItem{path: fmt.Sprintf("%s.%d", base, outputIndex), index: outputIndex, result: item, countFinal: final})
		}
	}
	return items, nil
}

func (r *codexCollabRestorer) restoreItem(body []byte, item codexCollabItem) ([]byte, error) {
	if r.plan != nil && !codexCollabUniqueObject(item.result) {
		return nil, errors.New("ambiguous collaboration item fields")
	}
	itemType := item.result.Get("type").String()
	if r.plan != nil && itemType == "namespace" && item.result.Get("name").String() == codexCollaborationNamespaceAlias {
		return sjson.SetBytes(body, codexCollabPath(item.path, "name"), codexCollaborationNamespace)
	}
	if itemType != "function_call" && itemType != "custom_tool_call" && !item.argumentsEvent {
		return body, nil
	}
	nameValue, namespaceValue := item.result.Get("name"), item.result.Get("namespace")
	name, namespace := nameValue.String(), namespaceValue.String()
	bareName, aliasName := codexCollabRestoreAliasName(name)
	aliasNamespace := namespace == codexCollaborationNamespaceAlias
	canonical := namespace == codexCollaborationNamespace
	if r.plan == nil {
		if _, messageTool := codexCollabMessageTools[name]; canonical && itemType == "function_call" && messageTool {
			state, err := r.itemState(item)
			if err == nil {
				r.countFunctionItem(state, item.result.Get("encrypted_function_args"))
				r.observeMessage(state, item.result.Get("arguments"))
			}
		}
		return body, nil
	}
	// Arguments events may carry only item_id and a nonempty marker. Remember
	// that evidence even before an output item has supplied its name.
	if item.argumentsEvent && !aliasName && !aliasNamespace && !canonical {
		if item.result.Get("encrypted_function_args").Exists() {
			state, err := r.itemState(item)
			if err != nil {
				return nil, err
			}
			if codexCollabEFAState(item.result.Get("encrypted_function_args")) == codexCollabMarkerVeto {
				state.sawNonEmpty = true
			}
		}
		return body, nil
	}
	if !aliasName && !aliasNamespace && !canonical {
		return body, nil
	}
	if nameValue.Type != gjson.String || (namespaceValue.Exists() && namespaceValue.Type != gjson.String) || name == "" {
		return nil, errors.New("invalid collaboration call identity")
	}
	if aliasName && namespace != "" && namespace != "functions" && !aliasNamespace && !canonical {
		return nil, errors.New("ambiguous collaboration call namespace")
	}
	toolName := name
	if aliasName {
		toolName = bareName
	}
	state, err := r.itemState(item)
	if err != nil {
		return nil, err
	}
	_, known := r.plan.tools[toolName]
	if !known {
		r.noteUncovered(state)
	}
	if state.tool != "" && state.tool != toolName {
		return nil, errors.New("collaboration item identity changed")
	}
	state.tool = toolName
	if !item.argumentsEvent {
		if state.itemType != "" && state.itemType != itemType {
			return nil, errors.New("collaboration item type changed")
		}
		state.itemType = itemType
	}
	if codexCollabEFAState(item.result.Get("encrypted_function_args")) == codexCollabMarkerVeto {
		state.sawNonEmpty = true
	}
	updated := body
	if aliasName || aliasNamespace {
		updated, err = sjson.SetBytes(updated, codexCollabPath(item.path, "name"), toolName)
		if err != nil {
			return nil, err
		}
		updated, err = sjson.SetBytes(updated, codexCollabPath(item.path, "namespace"), codexCollaborationNamespace)
		if err != nil {
			return nil, err
		}
	}
	if item.argumentsEvent {
		return updated, nil
	}
	if itemType == "custom_tool_call" {
		r.noteUncovered(state)
		return updated, nil
	}
	if _, messageTool := codexCollabMessageTools[toolName]; messageTool {
		r.countFunctionItem(state, item.result.Get("encrypted_function_args"))
	}
	_, mayPatch := r.plan.messageTools[toolName]
	if !known || !mayPatch || !item.countFinal {
		return updated, nil
	}
	return r.patchFunctionItem(updated, item, state)
}

func (r *codexCollabRestorer) observeMessage(state *codexCollabItemState, arguments gjson.Result) {
	if arguments.Type != gjson.String || !json.Valid([]byte(arguments.String())) {
		return
	}
	parsed := gjson.Parse(arguments.String())
	if !parsed.IsObject() || !codexCollabUniqueObject(parsed) {
		return
	}
	message := parsed.Get("message")
	if message.Type != gjson.String {
		return
	}
	kind := 1
	if openAICodexCollabMessageLooksOpaque(message.String()) {
		kind = 2
	}
	if state.messageKind == kind || state.messageKind == 2 {
		return
	}
	if state.messageKind == 1 {
		r.stats.MsgText--
	}
	state.messageKind = kind
	if kind == 2 {
		r.stats.MsgOpaque++
	} else {
		r.stats.MsgText++
	}
}

func codexCollabPath(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}

func (r *codexCollabRestorer) noteUncovered(state *codexCollabItemState) {
	if !state.uncoveredCounted {
		r.stats.Uncovered++
		state.uncoveredCounted = true
	}
}

func (r *codexCollabRestorer) noteVeto(state *codexCollabItemState) {
	if !state.vetoCounted {
		r.stats.Vetoed++
		state.vetoCounted = true
	}
}

func (r *codexCollabRestorer) patchFunctionItem(body []byte, item codexCollabItem, state *codexCollabItemState) ([]byte, error) {
	efa := item.result.Get("encrypted_function_args")
	efaState := codexCollabEFAState(efa)
	if state.sawNonEmpty {
		r.noteVeto(state)
		if efaState != codexCollabMarkerVeto || state.decision == codexCollabMarkerAdd {
			r.noteContradiction(state)
		}
		state.decision = codexCollabMarkerVeto
		// The gateway never synthesizes a marker after ciphertext evidence, but
		// an explicit upstream [] is preserved even when it contradicts history.
		return body, nil
	}
	if efaState == codexCollabMarkerKeep {
		if state.decision == codexCollabMarkerVeto {
			r.noteContradiction(state)
		}
		if state.decision == codexCollabMarkerUndecided {
			state.decision = codexCollabMarkerKeep
		}
		return body, nil
	}
	if state.decision == codexCollabMarkerVeto {
		return body, nil
	}
	if state.decision == codexCollabMarkerAdd {
		if item.result.Get("arguments").String() != state.finalArguments {
			r.noteContradiction(state)
			r.noteVeto(state)
			state.decision = codexCollabMarkerVeto
			return body, nil
		}
		return codexCollabSetMarker(body, item.path)
	}
	if state.decision == codexCollabMarkerKeep {
		// The upstream supplied an empty marker earlier, but removed it later.
		// Do not manufacture a replacement for the upstream declaration.
		r.noteContradiction(state)
		return body, nil
	}
	if state.anonymous || r.plan.extraEncrypted[state.tool] || !codexCollabMessageIsSafe(item.result.Get("arguments")) {
		state.decision = codexCollabMarkerVeto
		r.noteVeto(state)
		return body, nil
	}
	state.decision = codexCollabMarkerAdd
	state.finalArguments = item.result.Get("arguments").String()
	r.stats.MarkerAdded++
	return codexCollabSetMarker(body, item.path)
}

func (r *codexCollabRestorer) noteContradiction(state *codexCollabItemState) {
	if !state.contradictionCounted {
		r.stats.Contradicted++
		state.contradictionCounted = true
	}
}

func codexCollabSetMarker(body []byte, path string) ([]byte, error) {
	return sjson.SetRawBytes(body, codexCollabPath(path, "encrypted_function_args"), []byte("[]"))
}

// Count unique calls. Later nonempty evidence upgrades the EFA classification,
// so added/done/completed do not inflate the denominator or hide ciphertext.
func (r *codexCollabRestorer) countFunctionItem(state *codexCollabItemState, efa gjson.Result) {
	kind := codexCollabEFAState(efa)
	if kind == codexCollabMarkerVeto {
		state.sawNonEmpty = true
	}
	if state.sawNonEmpty {
		kind = codexCollabMarkerVeto
	}
	if !state.counted {
		r.stats.Calls++
		state.counted = true
	} else {
		if state.efaKind == kind {
			return
		}
		r.adjustEFACount(state.efaKind, -1)
	}
	state.efaKind = kind
	r.adjustEFACount(kind, 1)
}

func (r *codexCollabRestorer) adjustEFACount(kind codexCollabMarkerDecision, delta int) {
	switch kind {
	case codexCollabMarkerKeep:
		r.stats.EFAEmpty += delta
	case codexCollabMarkerVeto:
		r.stats.EFANonEmpty += delta
	default:
		r.stats.EFAAbsent += delta
	}
}

// Associate all protocol identifiers with one state, even when added has only
// id and done later supplies call_id. The identifiers themselves are never edited.
func (r *codexCollabRestorer) itemState(item codexCollabItem) (*codexCollabItemState, error) {
	var keys []string
	for _, field := range []string{"call_id", "id", "item_id"} {
		if id := item.result.Get(field); id.Type == gjson.String && id.String() != "" {
			prefix := "item:"
			if field == "call_id" {
				prefix = "call:"
			}
			keys = append(keys, prefix+id.String())
		}
	}
	if item.index >= 0 {
		keys = append(keys, fmt.Sprintf("index:%d", item.index))
	}
	if len(keys) == 0 {
		// Identity restoration is safe without IDs, but cross-event marker
		// synthesis requires a stable key. Never pool anonymous stream items.
		return &codexCollabItemState{anonymous: true}, nil
	}
	var state *codexCollabItemState
	for _, key := range keys {
		if known := r.items[key]; known != nil {
			if state != nil && state != known {
				return nil, errors.New("conflicting collaboration item identifiers")
			}
			state = known
		}
	}
	if state == nil {
		state = &codexCollabItemState{}
	}
	for _, key := range keys {
		r.items[key] = state
	}
	return state, nil
}

func codexCollabEFAState(value gjson.Result) codexCollabMarkerDecision {
	if !value.Exists() {
		return codexCollabMarkerUndecided
	}
	if value.IsArray() && len(value.Array()) == 0 {
		return codexCollabMarkerKeep
	}
	// null, scalars and objects are abnormal types, never an absent marker.
	return codexCollabMarkerVeto
}

func codexCollabMessageIsSafe(arguments gjson.Result) bool {
	if arguments.Type != gjson.String {
		return false
	}
	if !codexCollabUnambiguousJSON(gjson.Parse(arguments.String())) {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(arguments.String()))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return false
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return false
	}
	message, ok := object["message"].(string)
	if !ok || openAICodexCollabMessageLooksOpaque(message) {
		return false
	}
	for key, value := range object {
		if key == "message" {
			continue
		}
		if codexCollabContainsEncryptionEvidence(key, value) {
			return false
		}
	}
	return true
}

func codexCollabUnambiguousJSON(node gjson.Result) bool {
	if !node.IsObject() && !node.IsArray() {
		return true
	}
	keys := make(map[string]struct{})
	valid := true
	node.ForEach(func(key, value gjson.Result) bool {
		if node.IsObject() {
			if _, duplicate := keys[key.String()]; duplicate {
				valid = false
				return false
			}
			keys[key.String()] = struct{}{}
		}
		valid = codexCollabUnambiguousJSON(value)
		return valid
	})
	return valid
}

// Check only the protocol object's fields; nested tool output stays opaque.
func codexCollabUniqueObject(node gjson.Result) bool {
	if !node.IsObject() {
		return true
	}
	keys := make(map[string]struct{})
	unique := true
	node.ForEach(func(key, _ gjson.Result) bool {
		if _, found := keys[key.String()]; found {
			unique = false
			return false
		}
		keys[key.String()] = struct{}{}
		return true
	})
	return unique
}

func codexCollabContainsEncryptionEvidence(key string, value any) bool {
	if strings.Contains(strings.ToLower(key), "encrypt") {
		return true
	}
	switch typed := value.(type) {
	case map[string]any:
		if kind, ok := typed["type"].(string); ok && strings.EqualFold(kind, "encrypted_content") {
			return true
		}
		for childKey, child := range typed {
			if codexCollabContainsEncryptionEvidence(childKey, child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if codexCollabContainsEncryptionEvidence("", child) {
				return true
			}
		}
	case string:
		if strings.HasPrefix(typed, "gAAAA") {
			return true
		}
	}
	return false
}

func openAICodexCollabMessageLooksOpaque(message string) bool {
	if strings.HasPrefix(strings.TrimSpace(message), "gAAAA") {
		return true
	}
	if len(message) < codexCollabOpaqueMinLen {
		return false
	}
	for index := 0; index < len(message); index++ {
		b := message[index]
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '=', b == '+', b == '/':
		default:
			return false
		}
	}
	return true
}

func (r *codexCollabRestorer) restoreResponseEchoes(body []byte, root gjson.Result) ([]byte, error) {
	if r.plan == nil {
		return body, nil
	}
	var edits []codexCollabJSONEdit
	if r.plan.devTextRewrites > 0 {
		for _, path := range []string{"instructions", "response.instructions"} {
			if value := root.Get(path); value.Type == gjson.String {
				restored := strings.ReplaceAll(value.String(), "functions.collaboration-xy.", "functions.collaboration.")
				if restored != value.String() {
					edits = append(edits, codexCollabJSONEdit{path: path, value: restored})
				}
			}
		}
	}
	// Only response tool declarations and tool_choice are identity-bearing
	// echoes. Arbitrary prose, arguments, input, and output values are opaque.
	for _, rootPath := range []string{"tools", "response.tools"} {
		tools := root.Get(rootPath)
		if !tools.IsArray() {
			continue
		}
		if err := codexCollabCollectEchoEdits(tools, rootPath, &edits); err != nil {
			return nil, err
		}
	}
	for _, rootPath := range []string{"tool_choice", "response.tool_choice"} {
		choice := root.Get(rootPath)
		if err := codexCollabCollectChoiceEchoEdits(choice, rootPath, &edits); err != nil {
			return nil, err
		}
	}
	var err error
	for _, edit := range edits {
		body, err = sjson.SetBytes(body, edit.path, edit.value)
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func codexCollabCollectEchoEdits(tools gjson.Result, path string, edits *[]codexCollabJSONEdit) error {
	if !tools.IsArray() {
		return nil
	}
	for index, tool := range tools.Array() {
		if !codexCollabUniqueObject(tool) {
			return errors.New("ambiguous collaboration tool echo")
		}
		toolPath := fmt.Sprintf("%s.%d", path, index)
		if tool.Get("type").String() == "namespace" {
			if tool.Get("name").String() == codexCollaborationNamespaceAlias {
				*edits = append(*edits, codexCollabJSONEdit{path: toolPath + ".name", value: codexCollaborationNamespace})
			}
			if err := codexCollabCollectEchoEdits(tool.Get("tools"), toolPath+".tools", edits); err != nil {
				return err
			}
			continue
		}
		if typ := tool.Get("type").String(); typ == "function" || typ == "custom" {
			if err := codexCollabCollectChoiceEchoEdits(tool, toolPath, edits); err != nil {
				return err
			}
		}
	}
	return nil
}

func codexCollabCollectChoiceEchoEdits(choice gjson.Result, path string, edits *[]codexCollabJSONEdit) error {
	if choice.IsArray() {
		for index, child := range choice.Array() {
			if err := codexCollabCollectChoiceEchoEdits(child, fmt.Sprintf("%s.%d", path, index), edits); err != nil {
				return err
			}
		}
		return nil
	}
	if choice.Type == gjson.String {
		if choice.String() == codexCollaborationNamespaceAlias {
			*edits = append(*edits, codexCollabJSONEdit{path: path, value: codexCollaborationNamespace})
		} else if bare, ok := codexCollabRestoreAliasName(choice.String()); ok {
			// String tool_choice cannot express separate namespace/name fields.
			*edits = append(*edits, codexCollabJSONEdit{path: path, value: codexCollaborationNamespace + "." + bare})
		}
		return nil
	}
	if !choice.IsObject() {
		return nil
	}
	if !codexCollabUniqueObject(choice) {
		return errors.New("ambiguous collaboration tool choice echo")
	}
	namespace := choice.Get("namespace").String()
	if namespace == codexCollaborationNamespaceAlias {
		*edits = append(*edits, codexCollabJSONEdit{path: path + ".namespace", value: codexCollaborationNamespace})
	}
	if name := choice.Get("name"); name.Type == gjson.String {
		if bare, ok := codexCollabRestoreAliasName(name.String()); ok {
			if namespace != "" && namespace != "functions" && namespace != codexCollaborationNamespace && namespace != codexCollaborationNamespaceAlias {
				return errors.New("ambiguous collaboration tool echo namespace")
			}
			*edits = append(*edits, codexCollabJSONEdit{path: path + ".name", value: bare},
				codexCollabJSONEdit{path: path + ".namespace", value: codexCollaborationNamespace})
		} else if name.String() == codexCollaborationNamespaceAlias {
			*edits = append(*edits, codexCollabJSONEdit{path: path + ".name", value: codexCollaborationNamespace})
		}
	}
	for _, field := range []string{"tools", "function"} {
		if child := choice.Get(field); child.Exists() {
			if err := codexCollabCollectChoiceEchoEdits(child, path+"."+field, edits); err != nil {
				return err
			}
		}
	}
	return nil
}

func setOpenAICodexCollabAttempt(c *gin.Context, stats codexCollabStats) {
	if c != nil {
		c.Set(codexCollabAttemptContextKey, stats)
	}
}

func openAICodexCollabAttempt(c *gin.Context) (codexCollabStats, bool) {
	if c == nil {
		return codexCollabStats{}, false
	}
	value, exists := c.Get(codexCollabAttemptContextKey)
	if !exists {
		return codexCollabStats{}, false
	}
	stats, ok := value.(codexCollabStats)
	return stats, ok
}
