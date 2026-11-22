package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

type codexCollabNamespace struct {
	path  string
	tools gjson.Result
	node  gjson.Result
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
	edits, newPlan, outcome := planCodexCollaborationPlaintext(body)
	if newPlan == nil {
		return body, nil, outcome
	}
	var err error
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

// planCodexCollaborationPlaintext runs all atomic checks without editing body.
func planCodexCollaborationPlaintext(body []byte) ([]codexCollabJSONEdit, *codexCollabPlan, codexCollabOutcome) {
	if !json.Valid(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, nil, codexCollabInvalid
	}
	root := gjson.ParseBytes(body)
	if !codexCollabUniqueObject(root) {
		return nil, nil, codexCollabInvalid
	}
	if previous := root.Get("previous_response_id"); previous.Exists() && previous.Type != gjson.Null && strings.TrimSpace(previous.String()) != "" {
		return nil, nil, codexCollabUnsupported
	}
	if root.Get("store").Bool() {
		return nil, nil, codexCollabUnsupported
	}
	if !codexCollabRequestContainersUnique(root) {
		return nil, nil, codexCollabConflict
	}
	if codexCollabUnsupportedShape(root) {
		return nil, nil, codexCollabUnsupported
	}

	namespaces := codexCollabFindNamespaces(root)
	if len(namespaces) == 0 {
		return nil, nil, codexCollabAbsent
	}
	// Alias declarations are never accepted. Only historical call items may
	// carry names emitted by an earlier transformed request.
	if codexCollabHasPrivateName(root) {
		return nil, nil, codexCollabConflict
	}
	textEdits, textRewrites, err := codexCollabDeveloperTextEdits(root)
	if err != nil {
		return nil, nil, codexCollabConflict
	}

	if !namespaces[0].tools.IsArray() {
		return nil, nil, codexCollabConflict
	}
	if len(namespaces) > 1 {
		canonicalSchema, err := codexCollabCanonicalJSON(namespaces[0].node.Raw)
		if err != nil {
			return nil, nil, codexCollabConflict
		}
		for _, namespace := range namespaces[1:] {
			candidate, err := codexCollabCanonicalJSON(namespace.node.Raw)
			if !namespace.tools.IsArray() || err != nil || !bytes.Equal(candidate, canonicalSchema) {
				return nil, nil, codexCollabConflict
			}
		}
	}
	for _, namespace := range namespaces {
		if !codexCollabUnambiguousJSON(namespace.node) {
			return nil, nil, codexCollabConflict
		}
	}
	for _, item := range root.Get("input").Array() {
		if item.Get("type").String() == "function_call" && !codexCollabUniqueObject(item) {
			return nil, nil, codexCollabConflict
		}
	}
	if !codexCollabUnambiguousJSON(root.Get("tool_choice")) {
		return nil, nil, codexCollabConflict
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
					return nil, nil, codexCollabConflict
				}
				continue
			}
			if _, duplicate := declared[name]; duplicate {
				return nil, nil, codexCollabConflict
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
				return nil, nil, codexCollabConflict
			}
			for _, keyword := range []string{"anyOf", "oneOf", "allOf", "$ref"} {
				if message.Get(keyword).Exists() || tool.Get("parameters").Get(keyword).Exists() {
					return nil, nil, codexCollabConflict
				}
			}
			encrypted := message.Get("encrypted")
			if encrypted.Exists() && encrypted.Type != gjson.True && encrypted.Type != gjson.False {
				return nil, nil, codexCollabConflict
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
	referenceEdits, ok := codexCollabReferenceEdits(root, newPlan.tools)
	if !ok {
		return nil, nil, codexCollabConflict
	}
	edits = append(edits, referenceEdits...)

	return edits, newPlan, codexCollabApplied
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
			if !codexCollabUniqueObject(part) {
				return nil, 0, errors.New("ambiguous collaboration system text")
			}
			if part.Get("type").String() != "input_text" {
				continue
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
		}
	}
	return false
}

// codexCollabReferenceEdits accepts only historical function/custom call
// identities that can be tied to a declared collaboration tool. Declaration
// aliases and developer text are rejected elsewhere, and an explicit
// external namespace is never rewritten.
func codexCollabReferenceEdits(root gjson.Result, knownTools map[string]struct{}) ([]codexCollabJSONEdit, bool) {
	var edits []codexCollabJSONEdit
	input := root.Get("input")
	if input.IsArray() {
		for index, item := range input.Array() {
			itemType := item.Get("type").String()
			if itemType != "function_call" && itemType != "custom_tool_call" {
				continue
			}
			nameResult, namespaceResult := item.Get("name"), item.Get("namespace")
			name, namespace := nameResult.String(), namespaceResult.String()
			aliasName := codexCollabHasAliasPrefix(name)
			aliasNamespace := namespace == codexCollaborationNamespaceAlias
			if !aliasName && !aliasNamespace && namespace != codexCollaborationNamespace {
				continue
			}
			if aliasName && namespace != "" && namespace != codexCollaborationNamespace && !aliasNamespace {
				return nil, false
			}
			if nameResult.Type != gjson.String || name == "" || (namespaceResult.Exists() && namespaceResult.Type != gjson.String) {
				return nil, false
			}
			toolName := name
			if aliasName {
				var ok bool
				toolName, ok = codexCollabRestoreAliasName(name)
				if !ok {
					return nil, false
				}
			}
			if aliasName || aliasNamespace {
				if _, known := knownTools[toolName]; !known {
					return nil, false
				}
				if aliasName {
					edits = append(edits, codexCollabJSONEdit{path: fmt.Sprintf("input.%d.name", index), value: toolName})
				}
			}
			if namespace == codexCollaborationNamespace || aliasNamespace || aliasName {
				edits = append(edits, codexCollabJSONEdit{path: fmt.Sprintf("input.%d.namespace", index), value: codexCollaborationNamespaceAlias})
			}
		}
	}
	return edits, true
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
