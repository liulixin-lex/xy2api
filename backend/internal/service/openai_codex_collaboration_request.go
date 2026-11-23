package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/mod/semver"
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
	return planCodexCollaborationLegacy(body, nil)
}

// The optional scan reuses the legacy pass even when differing declarations
// prevent a legacy plan. The disabled path does not retain any scan state.
type codexCollabDeclarationScan struct {
	root         gjson.Result
	namespaces   []codexCollabNamespace
	textEdits    []codexCollabJSONEdit
	textRewrites int
	ready        bool
}

func planCodexCollaborationLegacy(body []byte, scan *codexCollabDeclarationScan) ([]codexCollabJSONEdit, *codexCollabPlan, codexCollabOutcome) {
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
	if scan != nil {
		*scan = codexCollabDeclarationScan{
			root: root, namespaces: namespaces, textEdits: textEdits,
			textRewrites: textRewrites, ready: true,
		}
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

// CollabDeclarationOptions must be derived once from the ingress settings and
// actual client version. An empty or invalid version never uses a server default.
type CollabDeclarationOptions struct {
	IncrementalDeclarations bool
	ClientVersion           string
}

// CollabDeclarationResult is a side-effect-free decision. A nil Plan means to
// retain today's forwarding path, never to produce a client error. Observation
// consumers can deduplicate Reason by thread without retaining prompt text.
type CollabDeclarationResult struct {
	Plan             *CollabPlan
	Outcome          resilience.CollabPlanOutcome
	Reason           resilience.CollabPlanReason
	OrdinaryMentions int
	edits            []codexCollabJSONEdit
	// Transient binding only; neither source bytes nor their fingerprint are
	// exposed in observations or records. A digest detects in-place mutation.
	sourceDigest  [sha256.Size]byte
	sourceOptions CollabDeclarationOptions
	sourceBound   bool
}

// PlanCodexCollaborationDeclarations is shared by the original-byte guard and
// normalized-body planning. The guard must succeed before applying any edits.
// Keep calling planCodexCollaborationPlaintext directly when the feature is off,
// avoiding even the shared-plan adaptation cost on the disabled path.
func PlanCodexCollaborationDeclarations(body []byte, options CollabDeclarationOptions) (result CollabDeclarationResult) {
	defer func() {
		result.sourceDigest = sha256.Sum256(body)
		result.sourceOptions = options
		result.sourceBound = true
	}()
	var scan codexCollabDeclarationScan
	legacyEdits, legacyPlan, legacyOutcome := planCodexCollaborationLegacy(body, &scan)
	legacy := CollabDeclarationResult{
		Outcome: resilience.CollabPlanOutcome(legacyOutcome),
		Reason:  codexCollabLegacyReason(legacyOutcome), edits: legacyEdits,
	}
	if options.IncrementalDeclarations {
		result := planCodexCollaborationIncremental(body, options, legacyPlan, legacyEdits, scan)
		if result.Plan != nil || legacyPlan == nil || result.Reason == resilience.CollabPlanReasonClientTooOld {
			return result
		}
	}
	// This boundary applies to every incremental decline, including future
	// checks: an accepted legacy request keeps its exact plan and edit sequence.
	legacy.Plan = codexCollabSharedLegacyNamespaces(scan.namespaces, legacyPlan)
	return legacy
}

func planCodexCollaborationIncremental(body []byte, options CollabDeclarationOptions, legacyPlan *codexCollabPlan, legacyEdits []codexCollabJSONEdit, scan codexCollabDeclarationScan) CollabDeclarationResult {
	decline := func(outcome resilience.CollabPlanOutcome, reason resilience.CollabPlanReason) CollabDeclarationResult {
		return CollabDeclarationResult{Outcome: outcome, Reason: reason}
	}
	root := scan.root
	if !scan.ready {
		root = gjson.ParseBytes(body)
		if !json.Valid(body) || !root.IsObject() {
			return decline(resilience.CollabPlanOutcomeInvalid, resilience.CollabPlanReasonInvalidJson)
		}
		if !codexCollabUniqueObject(root) {
			return decline(resilience.CollabPlanOutcomeInvalid, resilience.CollabPlanReasonDuplicateKey)
		}
		if previous := root.Get("previous_response_id"); (previous.Exists() && previous.Type != gjson.Null && strings.TrimSpace(previous.String()) != "") || root.Get("store").Bool() {
			return decline(resilience.CollabPlanOutcomeUnsupported, resilience.CollabPlanReasonStateful)
		}
		if !codexCollabRequestContainersUnique(root) {
			return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonDuplicateKey)
		}
		if codexCollabUnsupportedShape(root) {
			return decline(resilience.CollabPlanOutcomeUnsupported, resilience.CollabPlanReasonUnsupportedShape)
		}
	}
	namespaces := scan.namespaces
	if !scan.ready {
		namespaces = codexCollabFindNamespaces(root)
	}
	if len(namespaces) == 0 {
		return decline(resilience.CollabPlanOutcomeAbsent, resilience.CollabPlanReasonNoNamespace)
	}
	if !scan.ready && codexCollabHasPrivateName(root) {
		return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonAliasCollision)
	}
	// Full semver preserves prerelease ordering. Parsing failure retains current
	// behavior, including malformed or missing client version headers.
	version := "v" + options.ClientVersion
	core := strings.SplitN(strings.SplitN(options.ClientVersion, "-", 2)[0], "+", 2)[0]
	if strings.Count(core, ".") == 2 && semver.IsValid(version) && semver.Compare(version, "v0.147.0-alpha.1") < 0 {
		return decline(resilience.CollabPlanOutcomeUnsupported, resilience.CollabPlanReasonClientTooOld)
	}
	var textEdits []codexCollabJSONEdit
	var rewrites int
	if scan.ready {
		rewrites = scan.textRewrites
		// The legacy planner may have appended to the original backing array.
		// Never let optional edits overwrite its saved sequence.
		textEdits = slices.Clone(scan.textEdits)
	} else {
		var err error
		textEdits, rewrites, err = codexCollabDeveloperTextEdits(root)
		if err != nil {
			return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonControlText)
		}
	}
	rewrittenText := make(map[string]string, len(textEdits))
	for _, edit := range textEdits {
		if text, ok := edit.value.(string); ok {
			rewrittenText[edit.path] = text
		}
	}
	result := CollabDeclarationResult{
		Plan: &CollabPlan{
			HistoricalTools: make(map[string]struct{}), EffectiveTools: make(map[string]CollabToolState),
			DeveloperTextRewrites: rewrites,
		},
		Outcome: resilience.CollabPlanOutcomeApplied, Reason: resilience.CollabPlanReasonNone, edits: textEdits,
	}
	var noticeEdits []codexCollabJSONEdit
	// Preserve accepted shapes, including absent/false markers and non-function
	// members. Only newly supported updates require consistently encrypted tools.
	strict := legacyPlan == nil
	byContainer := make(map[string][]codexCollabNamespace)
	for _, namespace := range namespaces {
		if strict && (!namespace.tools.IsArray() || !codexCollabUnambiguousJSON(namespace.node)) {
			return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonSchemaInvalid)
		}
		if strict && len(namespaces) > 1 && !codexCollabNamespaceHeaderCompatible(namespaces[0].node, namespace.node) {
			return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonNamespaceUpdate)
		}
		container := namespace.path[:strings.LastIndexByte(namespace.path, '.')]
		byContainer[container] = append(byContainer[container], namespace)
	}
	applyNamespace := func(namespace codexCollabNamespace) resilience.CollabPlanReason {
		result.edits = append(result.edits, codexCollabJSONEdit{path: namespace.path + ".name", value: codexCollaborationNamespaceAlias})
		declared := make(map[string]struct{})
		for index, tool := range namespace.tools.Array() {
			name := tool.Get("name").String()
			if tool.Get("name").Type != gjson.String || name == "" {
				if strict || tool.Get("type").String() == "function" {
					return resilience.CollabPlanReasonSchemaInvalid
				}
				continue
			}
			if _, duplicate := declared[name]; duplicate {
				return resilience.CollabPlanReasonToolDuplicate
			}
			declared[name] = struct{}{}
			state := codexCollabDeclarationToolState(tool)
			_, messageTool := codexCollabMessageTools[name]
			if messageTool && (strict || state.Type == CollabToolTypeFunction) {
				message := tool.Get("parameters.properties.message")
				if state.Type != CollabToolTypeFunction || !codexCollabMessageSchemaValid(tool, strict) {
					return resilience.CollabPlanReasonSchemaInvalid
				}
				if message.Get("encrypted").Bool() {
					state.MessageMarkerRemoved = true
					result.edits = append(result.edits, codexCollabJSONEdit{
						path: fmt.Sprintf("%s.tools.%d.parameters.properties.message.encrypted", namespace.path, index), delete: true,
					})
				}
			}
			result.Plan.HistoricalTools[name] = struct{}{}
			result.Plan.EffectiveTools[name] = state
		}
		return resilience.CollabPlanReasonNone
	}
	// Top-level declarations establish the baseline; additional_tools and
	// developer notices then take effect in input order. Unlisted members stay.
	for _, namespace := range byContainer["tools"] {
		if reason := applyNamespace(namespace); reason != resilience.CollabPlanReasonNone {
			return decline(resilience.CollabPlanOutcomeConflict, reason)
		}
	}
	for index, item := range root.Get("input").Array() {
		itemType := item.Get("type").String()
		if itemType == "custom_tool_call" {
			name := item.Get("name").String()
			if strings.HasPrefix(name, codexCollaborationNamespace+".") || strings.HasPrefix(name, codexCollaborationNamespace+"__") {
				return decline(resilience.CollabPlanOutcomeUnsupported, resilience.CollabPlanReasonUnsupportedShape)
			}
		}
		if strings.TrimSpace(itemType) == "additional_tools" {
			for _, namespace := range byContainer[fmt.Sprintf("input.%d.tools", index)] {
				if reason := applyNamespace(namespace); reason != resilience.CollabPlanReasonNone {
					return decline(resilience.CollabPlanOutcomeConflict, reason)
				}
			}
		}
		if item.Get("role").String() != "developer" || strings.TrimSpace(itemType) == "additional_tools" {
			continue
		}
		// The API also accepts role-only messages and string content. Until
		// those carriers have complete text and metadata handling, decline the
		// optional conversion instead of silently skipping catalog evidence.
		// The outer boundary retains every accepted legacy conversion.
		if itemType != "message" || !item.Get("content").IsArray() {
			return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonControlText)
		}
		metadata := item.Get("internal_chat_message_metadata_passthrough")
		if !codexCollabUnambiguousJSON(metadata) {
			return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonControlText)
		}
		kinds := metadata.Get("content_item_kinds").Array()
		content := item.Get("content").Array()
		for _, kind := range kinds {
			if (kind.String() == codexCollabRemovedKind || kind.String() == codexCollabUpdateKind) && len(kinds) != len(content) {
				return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonControlText)
			}
		}
		for partIndex, part := range content {
			kind := ""
			if partIndex < len(kinds) && kinds[partIndex].Type == gjson.String {
				kind = kinds[partIndex].String()
			}
			confirmed := kind == codexCollabRemovedKind || kind == codexCollabUpdateKind
			if part.Get("type").String() != "input_text" || part.Get("text").Type != gjson.String {
				if confirmed {
					return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonControlText)
				}
				continue
			}
			path := fmt.Sprintf("input.%d.content.%d.text", index, partIndex)
			text := part.Get("text").String()
			if rewritten, exists := rewrittenText[path]; exists {
				text = rewritten
			}
			notice := codexCollabParseCatalogNotice(text)
			if notice.invalid || (confirmed && (!notice.recognized || notice.kind != kind)) {
				return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonControlText)
			}
			if !notice.recognized {
				result.OrdinaryMentions += strings.Count(text, codexCollaborationNamespace)
				continue
			}
			for name, state := range result.Plan.EffectiveTools {
				if notice.removeNamespace || notice.removed[name] {
					state.Available = false
					result.Plan.EffectiveTools[name] = state
				}
			}
			if notice.text != text {
				// The notice was parsed from the already-edited text, so this
				// composes with every earlier text edit, not a replayed subset.
				edit := codexCollabJSONEdit{path: path, value: notice.text}
				result.edits = append(result.edits, edit)
				noticeEdits = append(noticeEdits, edit)
				result.Plan.DeveloperTextRewrites += notice.rewrites
			}
		}
	}
	if legacyPlan != nil {
		// An already accepted body can change only at recognized-notice paths.
		// Preserve the complete legacy sequence rather than recreating its
		// edits or guessing which future edits belong to which category.
		result.edits = append(slices.Clone(legacyEdits), noticeEdits...)
	} else {
		references, ok := codexCollabReferenceEdits(root, result.Plan.HistoricalTools)
		if !ok {
			return decline(resilience.CollabPlanOutcomeConflict, resilience.CollabPlanReasonHistoryIdentity)
		}
		result.edits = append(result.edits, references...)
	}
	return result
}

// ApplyCodexCollaborationDeclarations edits only the planned JSON paths. A new
// edit failure falls back to legacy output; it never introduces an error exit.
// The disabled production path keeps calling applyCodexCollaborationPlaintext.
func ApplyCodexCollaborationDeclarations(body []byte, options CollabDeclarationOptions) ([]byte, CollabDeclarationResult) {
	result := PlanCodexCollaborationDeclarations(body, options)
	return applyCodexCollaborationDeclarationPlan(body, options, result)
}

// ApplyCodexCollaborationNormalizedDeclarations consumes the original-byte
// decision made before normalization, validating its source and options. A raw
// decline is binding, even if normalization removed metadata or duplicate keys.
// Equal immutable bodies reuse that decision without a second planning pass.
// Production uses this only when incremental declarations are enabled.
func ApplyCodexCollaborationNormalizedDeclarations(original, normalized []byte, raw CollabDeclarationResult, options CollabDeclarationOptions) ([]byte, CollabDeclarationResult) {
	result := planCodexCollaborationNormalizedDeclarations(original, normalized, raw, options)
	return applyCodexCollaborationDeclarationPlan(normalized, options, result)
}

func planCodexCollaborationNormalizedDeclarations(original, normalized []byte, raw CollabDeclarationResult, options CollabDeclarationOptions) CollabDeclarationResult {
	if !raw.sourceBound || raw.sourceOptions != options || raw.sourceDigest != sha256.Sum256(original) {
		raw = PlanCodexCollaborationDeclarations(original, options)
	}
	if raw.Plan == nil || bytes.Equal(original, normalized) {
		return raw
	}
	return PlanCodexCollaborationDeclarations(normalized, options)
}

func applyCodexCollaborationDeclarationPlan(body []byte, options CollabDeclarationOptions, result CollabDeclarationResult) ([]byte, CollabDeclarationResult) {
	if result.Plan == nil {
		return body, result
	}
	changed := bytes.Clone(body)
	for _, edit := range result.edits {
		var err error
		if edit.delete {
			changed, err = sjson.DeleteBytes(changed, edit.path)
		} else {
			changed, err = sjson.SetBytes(changed, edit.path, edit.value)
		}
		if err != nil {
			if options.IncrementalDeclarations {
				// Optional edit failure is subject to the same legacy floor as
				// planning failure; discard all partially applied new edits.
				return ApplyCodexCollaborationDeclarations(body, CollabDeclarationOptions{})
			}
			return body, CollabDeclarationResult{Outcome: resilience.CollabPlanOutcomeInvalid, Reason: resilience.CollabPlanReasonEditFailed}
		}
	}
	return changed, result
}

func codexCollabLegacyReason(outcome codexCollabOutcome) resilience.CollabPlanReason {
	switch outcome {
	case codexCollabApplied:
		return resilience.CollabPlanReasonNone
	case codexCollabAbsent:
		return resilience.CollabPlanReasonNoNamespace
	case codexCollabInvalid:
		return resilience.CollabPlanReasonInvalidJson
	case codexCollabUnsupported:
		return resilience.CollabPlanReasonUnsupportedShape
	default:
		return resilience.CollabPlanReasonSchemaInvalid
	}
}

func codexCollabSharedLegacyNamespaces(namespaces []codexCollabNamespace, legacy *codexCollabPlan) *CollabPlan {
	if legacy == nil {
		return nil
	}
	plan := &CollabPlan{
		HistoricalTools: make(map[string]struct{}), EffectiveTools: make(map[string]CollabToolState),
		DeveloperTextRewrites: legacy.devTextRewrites,
	}
	for _, namespace := range namespaces {
		for _, tool := range namespace.tools.Array() {
			name := tool.Get("name").String()
			if _, exists := legacy.tools[name]; !exists {
				continue
			}
			state := codexCollabDeclarationToolState(tool)
			_, state.MessageMarkerRemoved = legacy.messageTools[name]
			state.ExtraEncrypted = legacy.extraEncrypted[name]
			plan.HistoricalTools[name] = struct{}{}
			plan.EffectiveTools[name] = state
		}
	}
	return plan
}

func codexCollabDeclarationToolState(tool gjson.Result) CollabToolState {
	state := CollabToolState{Available: true, Type: CollabToolTypeUnknown, HasMessage: tool.Get("parameters.properties.message").Exists()}
	switch strings.TrimSpace(tool.Get("type").String()) {
	case "function":
		state.Type = CollabToolTypeFunction
	case "custom":
		state.Type = CollabToolTypeCustom
	}
	tool.Get("parameters.properties").ForEach(func(key, value gjson.Result) bool {
		if key.String() != "message" && codexCollabSchemaEncrypted(value) {
			state.ExtraEncrypted = true
		}
		return true
	})
	return state
}

func codexCollabMessageSchemaValid(tool gjson.Result, requireEncrypted bool) bool {
	parameters := tool.Get("parameters")
	message := parameters.Get("properties.message")
	if !parameters.IsObject() || !parameters.Get("properties").IsObject() || !message.IsObject() {
		return false
	}
	typ := message.Get("type")
	validType := typ.Type == gjson.String && typ.String() == "string"
	if typ.IsArray() {
		seen := make(map[string]bool)
		for _, member := range typ.Array() {
			if member.Type != gjson.String || (member.String() != "string" && member.String() != "null") || seen[member.String()] {
				return false
			}
			seen[member.String()] = true
		}
		validType = seen["string"]
	}
	if !validType {
		return false
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf", "$ref"} {
		if message.Get(keyword).Exists() || parameters.Get(keyword).Exists() {
			return false
		}
	}
	marker := message.Get("encrypted")
	return (!marker.Exists() || marker.Type == gjson.True || marker.Type == gjson.False) && (!requireEncrypted || marker.Type == gjson.True)
}

// Only description and member changes have established incremental semantics.
func codexCollabNamespaceHeaderCompatible(first, next gjson.Result) bool {
	header := func(node gjson.Result) map[string]string {
		fields := make(map[string]string)
		node.ForEach(func(key, value gjson.Result) bool {
			if key.String() != "description" && key.String() != "tools" {
				canonical, err := codexCollabCanonicalJSON(value.Raw)
				if err == nil {
					fields[key.String()] = string(canonical)
				}
			}
			return true
		})
		return fields
	}
	return maps.Equal(header(first), header(next))
}

// Bundled notice strings from OpenAI Codex, Copyright OpenAI, licensed under
// Apache-2.0: https://github.com/openai/codex (codex-rs/prompts/src/model_messages/incremental_tools.rs).
// License: https://www.apache.org/licenses/LICENSE-2.0 . Only these five strings
// are quoted; the parser and all request fixtures are independently written.
const (
	// Positional client kinds from core/src/context/world_state/top_level_tools.rs.
	// Namespace guidance uses generic.developer_instructions, which alone does
	// not establish that arbitrary developer text is a catalog operation.
	codexCollabRemovedKind               = "tools.removed_definition"
	codexCollabUpdateKind                = "tools.incremental_update"
	codexCollabUpdateHint                = "This is an incremental tools update. Previously declared tools remain available for direct calls unless explicitly marked unavailable. If a tool is redefined here, its latest definition replaces the earlier one."
	codexCollabRemovedTools              = "The following tools are no longer available. Do not call them:"
	codexCollabRemovedNamespaces         = "The following namespaces are no longer available. Do not call tools in them unless those tools are declared in a later update:"
	codexCollabInstructionsPrefix        = "Updated instructions for the "
	codexCollabInstructionsSuffix        = " namespace:\n"
	codexCollabInstructionsClearedPrefix = "The "
	codexCollabInstructionsClearedSuffix = " namespace no longer has additional instructions."
)

type codexCollabCatalogNotice struct {
	text            string
	kind            string
	recognized      bool
	invalid         bool
	removeNamespace bool
	removed         map[string]bool
	rewrites        int
}

func codexCollabParseCatalogNotice(text string) codexCollabCatalogNotice {
	notice := codexCollabCatalogNotice{text: text}
	if text == codexCollabUpdateHint {
		notice.recognized = true
		notice.kind = codexCollabUpdateKind
		return notice
	}
	// Guidance changes do not re-enable withdrawn members. Only the template's
	// identity changes; the instructions themselves remain opaque.
	for _, template := range [][2]string{
		{codexCollabInstructionsPrefix, codexCollabInstructionsSuffix},
		{codexCollabInstructionsClearedPrefix, codexCollabInstructionsClearedSuffix},
	} {
		if !strings.HasPrefix(text, template[0]) {
			continue
		}
		rest := strings.TrimPrefix(text, template[0])
		name, tail, found := strings.Cut(rest, template[1])
		if !found || !codexCollabCatalogName(name) || (template[0] == codexCollabInstructionsClearedPrefix && tail != "") {
			continue
		}
		notice.recognized = true
		if name == codexCollaborationNamespace {
			notice.text = template[0] + codexCollaborationNamespaceAlias + template[1] + tail
			notice.rewrites = 1
		}
		return notice
	}
	// Recognize the entire part before interpreting identities. Embedded headers,
	// quoted examples and lists containing prose are ordinary mentions.
	sections := strings.Split(text, "\n\n")
	for _, section := range sections {
		lines := strings.Split(section, "\n")
		if len(lines) < 2 {
			return notice
		}
		switch lines[0] {
		case codexCollabRemovedTools, codexCollabRemovedNamespaces:
		default:
			return notice
		}
		for _, line := range lines[1:] {
			if !strings.HasPrefix(line, "- ") {
				return notice
			}
			for _, name := range strings.Split(strings.TrimPrefix(line, "- "), ".") {
				if !codexCollabCatalogName(name) {
					return notice
				}
			}
		}
	}
	notice.recognized = true
	notice.kind = codexCollabRemovedKind
	for sectionIndex, section := range sections {
		lines := strings.Split(section, "\n")
		for index, line := range lines[1:] {
			name := strings.TrimPrefix(line, "- ")
			if lines[0] == codexCollabRemovedNamespaces {
				if !codexCollabCatalogName(name) {
					notice.invalid = true
				}
				if name == codexCollaborationNamespace {
					notice.removeNamespace = true
					lines[index+1] = "- " + codexCollaborationNamespaceAlias
					notice.rewrites++
				}
			} else {
				namespace, member, qualified := strings.Cut(name, ".")
				if qualified && !codexCollabCatalogName(member) {
					notice.invalid = true
				}
				if namespace == codexCollaborationNamespace {
					if !qualified {
						notice.invalid = true
						continue
					}
					if notice.removed == nil {
						notice.removed = make(map[string]bool)
					}
					notice.removed[member] = true
					lines[index+1] = "- " + codexCollaborationNamespaceAlias + "." + member
					notice.rewrites++
				}
			}
		}
		sections[sectionIndex] = strings.Join(lines, "\n")
	}
	notice.text = strings.Join(sections, "\n\n")
	return notice
}

func codexCollabCatalogName(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' && ch != '-' {
			return false
		}
	}
	return true
}
