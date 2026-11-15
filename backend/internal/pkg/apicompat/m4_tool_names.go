package apicompat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func bridgeEnabled(bridge *BridgeOptions) bool { return bridge != nil && bridge.Enabled }

// BuildBridgeToolNames builds one immutable identity table before filtering tools.
// Historical identities never become declarations.
func BuildBridgeToolNames(tools []ResponsesTool, input json.RawMessage, target string) (*BridgeToolNameMap, error) {
	if target != "chat" && target != "anthropic" {
		return nil, unsupportedConversion("tools", "unknown bridge target")
	}
	identities := make(map[ToolNameIdentity]bool)
	var visit func([]ResponsesTool, string)
	visit = func(declarations []ResponsesTool, namespace string) {
		for _, tool := range declarations {
			kind := strings.ToLower(strings.TrimSpace(tool.Type))
			if kind == "namespace" {
				ns := tool.Name
				if namespace != "" {
					ns = namespace + "." + ns
				}
				children := tool.Tools
				if len(children) == 0 {
					children = tool.Children
				}
				visit(children, ns)
			} else if (kind == "function" || kind == "custom") && tool.Name != "" {
				identities[ToolNameIdentity{Kind: kind, Namespace: namespace, Name: tool.Name}] = true
			} else if kind == "tool_search" {
				identities[ToolNameIdentity{Kind: kind, Name: toolSearchProxyName}] = true
			}
		}
	}
	visit(tools, "")
	var items []map[string]json.RawMessage
	if json.Unmarshal(input, &items) == nil {
		for _, item := range items {
			kind := bridgeHistoryToolKind(rawString(item["type"]))
			if (kind == "function" || kind == "custom") && rawString(item["name"]) != "" {
				identities[ToolNameIdentity{Kind: kind, Namespace: rawString(item["namespace"]), Name: rawString(item["name"])}] = true
			}
		}
	}
	ordered := make([]ToolNameIdentity, 0, len(identities))
	for identity := range identities {
		ordered = append(ordered, identity)
	}
	key := func(id ToolNameIdentity) string { raw, _ := json.Marshal(id); return string(raw) }
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Kind == "tool_search" || ordered[j].Kind == "tool_search" {
			return ordered[i].Kind == "tool_search" && ordered[j].Kind != "tool_search"
		}
		if (ordered[i].Namespace == "") != (ordered[j].Namespace == "") {
			return ordered[i].Namespace == ""
		}
		return key(ordered[i]) < key(ordered[j])
	})
	preferred := func(id ToolNameIdentity) string {
		if id.Namespace != "" {
			return id.Namespace + "__" + id.Name
		}
		return id.Name
	}
	occupied := make(map[string]string)
	for _, id := range ordered {
		name := preferred(id)
		if validBridgeToolName(name, 64) {
			if _, exists := occupied[name]; !exists {
				occupied[name] = key(id)
			}
		}
	}
	result := &BridgeToolNameMap{Forward: make(map[ToolNameIdentity]string), Reverse: make(map[string]ToolNameIdentity)}
	for _, id := range ordered {
		alias := bridgeToolAlias(key(id), preferred(id), 64, occupied)
		result.Forward[id] = alias
		result.Reverse[alias] = id
	}
	return result, nil
}

func bridgeFlattenTypedTools(tools []ResponsesTool, namespace string, names *BridgeToolNameMap) []ResponsesTool {
	var out []ResponsesTool
	for _, tool := range tools {
		if tool.Type == "namespace" {
			ns := tool.Name
			if namespace != "" {
				ns = namespace + "." + ns
			}
			children := tool.Tools
			if len(children) == 0 {
				children = tool.Children
			}
			out = append(out, bridgeFlattenTypedTools(children, ns, names)...)
		} else {
			id := ToolNameIdentity{Kind: tool.Type, Namespace: namespace, Name: tool.Name}
			if alias, ok := names.Forward[id]; ok {
				tool.Name = alias
			}
			out = append(out, tool)
		}
	}
	return out
}

func bridgeRewriteInput(input json.RawMessage, names *BridgeToolNameMap) (json.RawMessage, error) {
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return append(json.RawMessage(nil), input...), nil
	}
	out := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			out = append(out, raw)
			continue
		}
		typ := rawString(item["type"])
		if typ == "additional_tools" {
			continue
		}
		if typ == "function_call" || typ == "custom_tool_call" {
			id := ToolNameIdentity{Kind: bridgeHistoryToolKind(typ), Namespace: rawString(item["namespace"]), Name: rawString(item["name"])}
			if alias, ok := names.Forward[id]; ok {
				item["name"], _ = json.Marshal(alias)
				delete(item, "namespace")
				var err error
				raw, err = json.Marshal(item)
				if err != nil {
					return nil, err
				}
			}
		}
		out = append(out, raw)
	}
	return json.Marshal(out)
}

func bridgeRewriteChoice(raw json.RawMessage, names *BridgeToolNameMap) json.RawMessage {
	var choice map[string]json.RawMessage
	if json.Unmarshal(raw, &choice) != nil {
		return raw
	}
	kind := rawString(choice["type"])
	if kind != "function" && kind != "custom" {
		return raw
	}
	name := rawString(choice["name"])
	if name == "" {
		name = rawNestedString(choice["function"], "name")
	}
	if alias, ok := names.Forward[ToolNameIdentity{Kind: kind, Namespace: rawString(choice["namespace"]), Name: name}]; ok {
		choice["name"], _ = json.Marshal(alias)
		delete(choice, "namespace")
		delete(choice, "function")
		encoded, _ := json.Marshal(choice)
		return encoded
	}
	return raw
}

func prepareBridgeRequest(req *ResponsesRequest, opts ConversionOptions, target string) (*ResponsesRequest, error) {
	tools, err := effectiveBridgeTools(req, opts)
	if err != nil {
		return nil, err
	}
	if opts.Bridge.ToolNames == nil {
		opts.Bridge.ToolNames, err = BuildBridgeToolNames(tools, req.Input, target)
		if err != nil {
			return nil, err
		}
	}
	copyReq := *req
	copyReq.Tools = bridgeFlattenTypedTools(tools, "", opts.Bridge.ToolNames)
	copyReq.Input, err = bridgeRewriteInput(req.Input, opts.Bridge.ToolNames)
	if err != nil {
		return nil, err
	}
	copyReq.ToolChoice = bridgeRewriteChoice(req.ToolChoice, opts.Bridge.ToolNames)
	return &copyReq, nil
}

func responsesToChatBridgeRequest(req *ResponsesRequest, opts *ResponsesToChatOptions) (*ChatCompletionsRequest, error) {
	prepared, err := prepareBridgeRequest(req, ConversionOptions{Bridge: opts.Bridge, Record: opts.Record}, "chat")
	if err != nil {
		return nil, err
	}
	selectionOpts := ConversionOptions{Bridge: opts.Bridge, Record: opts.Record}
	if allowed := anthropicAllowedToolIdentities(prepared, prepared.Tools, selectionOpts); allowed != nil {
		selected := make([]ResponsesTool, 0, len(prepared.Tools))
		for _, tool := range prepared.Tools {
			if allowed[anthropicToolIdentity(tool, selectionOpts)] {
				selected = append(selected, tool)
			}
		}
		prepared.Tools = selected
		var choice struct {
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal(prepared.ToolChoice, &choice)
		if choice.Mode == "required" {
			prepared.ToolChoice = json.RawMessage(`"required"`)
		} else {
			prepared.ToolChoice = json.RawMessage(`"auto"`)
		}
		if len(selected) == 0 && (choice.Mode == "" || choice.Mode == "auto") {
			prepared.ToolChoice = json.RawMessage(`"none"`)
		}
	}
	prepared.Input, err = bridgeReasoningHistory(prepared.Input)
	if err != nil {
		return nil, err
	}
	history, restore, err := bridgeHistoryOccurrences(prepared.Input)
	prepared.Input = history
	if err != nil {
		return nil, err
	}
	legacyOpts := *opts
	legacyOpts.Bridge = nil
	out, err := ResponsesToChatCompletionsRequestWithOptions(prepared, &legacyOpts)
	if err != nil {
		return nil, err
	}
	if len(prepared.Tools) == 0 && string(prepared.ToolChoice) == `"none"` {
		out.ToolChoice = json.RawMessage(`"none"`)
	}
	restoreBridgeChatHistory(out.Messages, restore)
	out.User = append(json.RawMessage(nil), normalizedRawJSON(req.User)...)
	out.Metadata = append(json.RawMessage(nil), normalizedRawJSON(req.Metadata)...)
	if req.Text != nil {
		out.Verbosity = req.Text.Verbosity
	}
	return out, nil
}

// ChatCompletionsResponseToResponsesWithBridgeOptions restores the request's
// aliases. The legacy single-result entry retains its original contract.
func ChatCompletionsResponseToResponsesWithBridgeOptions(resp *ChatCompletionsResponse, model string, customTools, functionTools map[string]bool, toolSearch bool, namespaceTools map[string]NamespacedToolName, opts ConversionOptions) (*ResponsesResponse, error) {
	if bridgeEnabled(opts.Bridge) && opts.Bridge.ToolNames != nil {
		customTools, functionTools, namespaceTools = bridgeChatNameIndexes(opts.Bridge.ToolNames)
	}
	if bridgeEnabled(opts.Bridge) {
		if err := validateBridgeBufferedTools(resp, opts.Bridge, customTools, toolSearch, false); err != nil {
			return nil, err
		}
	}
	out := ChatCompletionsResponseToResponses(resp, model, customTools, functionTools, toolSearch, namespaceTools)
	if bridgeEnabled(opts.Bridge) {
		if err := applyBridgeBufferedParts(out, resp); err != nil {
			return nil, err
		}
		bridgeLengthToolGate(out, resp, opts.Bridge, customTools, toolSearch)
		if resp != nil && len(resp.Choices) > 0 {
			choice := resp.Choices[0]
			var err error
			out.Status, out.IncompleteDetails, err = bridgeResponsesTerminal("chat", choice.FinishReason, len(choice.Message.ToolCalls) > 0 && choice.FinishReason != "length", choice.Message.Refusal != "", opts.Record)
			if err != nil {
				return nil, err
			}
			appendBridgeBufferedRefusal(out, choice.Message.Refusal)
		}
		for i := range out.Output {
			if out.Output[i].Type == "reasoning" {
				out.Output[i].ID = generateReasoningItemID()
			}
			restoreBridgeOutputIdentity(&out.Output[i], opts.Bridge.ToolNames)
		}
	}
	return out, nil
}

func restoreBridgeOutputIdentity(item *ResponsesOutput, names *BridgeToolNameMap) {
	if item == nil || names == nil {
		return
	}
	if item.Namespace != "" {
		return
	}
	if item.Type != "function_call" && item.Type != "custom_tool_call" {
		return
	}
	if identity, ok := names.Reverse[item.Name]; ok {
		item.Name, item.Namespace = identity.Name, identity.Namespace
		if identity.Kind == "tool_search" {
			item.Type = "tool_search_call"
			item.Name = ""
			item.Namespace = ""
		}
		if identity.Kind == "custom" && item.Type == "function_call" {
			item.Type = "custom_tool_call"
			item.Input = extractCustomToolCallInput(item.Arguments)
			item.Arguments = ""
		}
	}
}

// FlattenResponsesNamespacesWithBridgeOptions is the opt-in raw counterpart of
// the typed bridge preparation. Unknown declaration fields remain intact.
func FlattenResponsesNamespacesWithBridgeOptions(req map[string]any, opts ConversionOptions) (map[string]ResponsesNamespaceName, bool, error) {
	if !bridgeEnabled(opts.Bridge) {
		return FlattenResponsesNamespaces(req)
	}
	if req == nil {
		return nil, false, nil
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, false, err
	}
	var typed ResponsesRequest
	if err := json.Unmarshal(raw, &typed); err != nil {
		return nil, false, err
	}
	tools, err := effectiveBridgeTools(&typed, opts)
	if err != nil {
		return nil, false, err
	}
	if opts.Bridge.ToolNames == nil {
		opts.Bridge.ToolNames, err = BuildBridgeToolNames(tools, typed.Input, "anthropic")
		if err != nil {
			return nil, false, err
		}
	}
	var batches [][]json.RawMessage
	appendBatch := func(value any) error {
		encoded, e := json.Marshal(value)
		if e != nil {
			return e
		}
		var b []json.RawMessage
		if e = json.Unmarshal(encoded, &b); e != nil {
			return e
		}
		batches = append(batches, b)
		return nil
	}
	if err := appendBatch(req["tools"]); err != nil {
		return nil, false, err
	}
	input, _ := req["input"].([]any)
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if stringValue(item["type"]) == "additional_tools" {
			if err := appendBatch(item["tools"]); err != nil {
				return nil, false, err
			}
		} else {
			batches = append(batches, nil)
		}
	}
	merged, err := MergeResponsesToolDeclarations(opts, batches...)
	if err != nil {
		return nil, false, err
	}
	var flatten func([]json.RawMessage, string) ([]any, error)
	flatten = func(declarations []json.RawMessage, namespace string) ([]any, error) {
		var result []any
		for _, raw := range declarations {
			var tool map[string]any
			if err := json.Unmarshal(raw, &tool); err != nil {
				return nil, err
			}
			kind, name := stringValue(tool["type"]), stringValue(tool["name"])
			if kind == "namespace" {
				ns := name
				if namespace != "" {
					ns = namespace + "." + name
				}
				childrenRaw, _ := json.Marshal(namespaceChildren(tool))
				var children []json.RawMessage
				_ = json.Unmarshal(childrenRaw, &children)
				childrenFlat, e := flatten(children, ns)
				if e != nil {
					return nil, e
				}
				result = append(result, childrenFlat...)
			} else {
				if alias, ok := opts.Bridge.ToolNames.Forward[ToolNameIdentity{Kind: kind, Namespace: namespace, Name: name}]; ok {
					tool["name"] = alias
				}
				result = append(result, tool)
			}
		}
		return result, nil
	}
	flattened, err := flatten(merged, "")
	if err != nil {
		return nil, false, err
	}
	req["tools"] = flattened
	rewritten, err := bridgeRewriteInput(typed.Input, opts.Bridge.ToolNames)
	if err != nil {
		return nil, false, err
	}
	if len(typed.Input) > 0 {
		var value any
		if err = json.Unmarshal(rewritten, &value); err != nil {
			return nil, false, err
		}
		req["input"] = value
	}
	if len(typed.ToolChoice) > 0 {
		var value any
		if err = json.Unmarshal(bridgeRewriteChoice(typed.ToolChoice, opts.Bridge.ToolNames), &value); err != nil {
			return nil, false, err
		}
		req["tool_choice"] = value
	}
	names := make(map[string]ResponsesNamespaceName)
	for alias, id := range opts.Bridge.ToolNames.Reverse {
		names[alias] = ResponsesNamespaceName{Kind: id.Kind, Namespace: id.Namespace, Name: id.Name}
	}
	return names, true, nil
}

// AdaptResponsesClientToolsWithBridgeOptions lowers tools using the same table
// later carried by both buffered and streaming restorers.
func AdaptResponsesClientToolsWithBridgeOptions(req map[string]any, opts ConversionOptions) (ResponsesClientToolMapping, bool, error) {
	if !bridgeEnabled(opts.Bridge) {
		return AdaptResponsesClientTools(req)
	}
	names, changed, err := FlattenResponsesNamespacesWithBridgeOptions(req, opts)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	mapping, lowered, err := AdaptResponsesClientTools(req)
	if err != nil {
		return mapping, false, err
	}
	mapping.Bridge = opts.Bridge
	mapping.NamespaceTools = names
	// Complete historical custom identities are lowered even when absent from
	// the current declarations; they do not become callable tools.
	if mapping.CustomTools == nil {
		mapping.CustomTools = make(map[string]bool)
	}
	for alias, id := range opts.Bridge.ToolNames.Reverse {
		if id.Kind == "custom" {
			mapping.CustomTools[alias] = true
		}
	}
	historyChanged, err := rewriteClientToolHistory(req["input"], &mapping)
	return mapping, changed || lowered || historyChanged, err
}

func bridgeConversionError(field string, err error) error {
	return &UpstreamConversionError{Field: field, Reason: fmt.Sprint(err), SourceProtocol: "chat"}
}

func restoreBridgeRawValue(value any, names *BridgeToolNameMap) bool {
	if names == nil {
		return false
	}
	changed := false
	switch v := value.(type) {
	case []any:
		for _, child := range v {
			changed = restoreBridgeRawValue(child, names) || changed
		}
	case map[string]any:
		typ := stringValue(v["type"])
		if typ == "function_call" || typ == "custom_tool_call" {
			if id, ok := names.Reverse[stringValue(v["name"])]; ok {
				v["name"] = id.Name
				if id.Namespace != "" {
					v["namespace"] = id.Namespace
				} else {
					delete(v, "namespace")
				}
				if id.Kind == "tool_search" {
					v["type"] = "tool_search_call"
					v["execution"] = "client"
					delete(v, "name")
					delete(v, "namespace")
				}
				if id.Kind == "custom" && typ == "function_call" {
					v["type"] = "custom_tool_call"
					v["input"] = extractCustomToolCallInput(rawObjectString(v["arguments"]))
					delete(v, "arguments")
				}
				changed = true
			}
		}
		for _, child := range v {
			changed = restoreBridgeRawValue(child, names) || changed
		}
	}
	return changed
}

func bridgeChatNameIndexes(names *BridgeToolNameMap) (map[string]bool, map[string]bool, map[string]NamespacedToolName) {
	custom, functions := make(map[string]bool), make(map[string]bool)
	namespaces := make(map[string]NamespacedToolName)
	if names != nil {
		for alias, id := range names.Reverse {
			if id.Kind == "custom" {
				custom[alias] = true
			} else if id.Kind == "function" {
				functions[alias] = true
			}
			if id.Namespace != "" {
				namespaces[alias] = NamespacedToolName{Kind: id.Kind, Namespace: id.Namespace, Name: id.Name}
			}
		}
	}
	return custom, functions, namespaces
}

func bridgeRestoredToolName(name string, bridge *BridgeOptions) string {
	if bridgeEnabled(bridge) && bridge.ToolNames != nil {
		if id, ok := bridge.ToolNames.Reverse[name]; ok {
			return id.Name
		}
	}
	return name
}

func bridgeHistoryToolKind(typ string) string {
	switch typ {
	case "function_call":
		return "function"
	case "custom_tool_call":
		return "custom"
	case "tool_search_call":
		return "tool_search"
	default:
		return ""
	}
}
