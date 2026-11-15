package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func geminiCompatConversionOptions(ctx context.Context) apicompat.ConversionOptions {
	return apicompat.ConversionOptions{Record: func(notice apicompat.ConversionNotice) {
		resilience.RecordConversionNotices(ctx, notice)
	}}
}

func buildGeminiAPIKeyCompatRequest(claudeBody []byte, mappedModel string, opts apicompat.ConversionOptions) ([]byte, error) {
	body, err := convertClaudeMessagesToGeminiGenerateContent(claudeBody)
	if err != nil {
		return nil, err
	}
	body = ensureGeminiFunctionCallThoughtSignatures(body)
	for toolIndex, tool := range gjson.GetBytes(body, "tools").Array() {
		for declarationIndex, declaration := range tool.Get("functionDeclarations").Array() {
			parameters := declaration.Get("parameters")
			if !parameters.IsObject() {
				continue
			}
			var schema map[string]any
			decoder := json.NewDecoder(bytes.NewReader([]byte(parameters.Raw)))
			decoder.UseNumber()
			if err := decoder.Decode(&schema); err != nil {
				return nil, err
			}
			field := fmt.Sprintf("tools[%d].functionDeclarations[%d].parameters", toolIndex, declarationIndex)
			omitted := pruneGeminiEmptyEnums(schema, field)
			if len(omitted) == 0 {
				continue
			}
			path := fmt.Sprintf("tools.%d.functionDeclarations.%d.parameters", toolIndex, declarationIndex)
			body, err = sjson.SetBytes(body, path, schema)
			if err != nil {
				return nil, err
			}
			for _, field := range omitted {
				if opts.Record != nil {
					opts.Record(apicompat.ConversionNotice{Field: field, Action: "omit"})
				}
			}
		}
	}
	body, err = applyGeminiCompatToolChoice(claudeBody, body, opts)
	if err != nil {
		return nil, err
	}
	capabilities := geminiCompatCapabilities(mappedModel)
	body, err = applyGeminiCompatThinkingConfig(claudeBody, body, capabilities, opts)
	if err != nil {
		return nil, err
	}
	return applyGeminiCompatResponseFormat(claudeBody, body, capabilities, opts)
}

type GeminiCompatCapabilities struct {
	ThinkingLevels                          []string
	Budget, Dynamic, JSONMIME, JSONSchema   bool
	MinBudget, MinPositiveBudget, MaxBudget int
}

func geminiCompatCapabilities(model string) GeminiCompatCapabilities {
	model = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	switch model {
	case "gemini-2.0-flash", "gemini-2.0-flash-lite":
		return GeminiCompatCapabilities{JSONMIME: true}
	case "gemini-2.5-flash":
		return GeminiCompatCapabilities{Budget: true, Dynamic: true, MaxBudget: 24576, JSONMIME: true, JSONSchema: true}
	case "gemini-2.5-flash-lite":
		return GeminiCompatCapabilities{Budget: true, Dynamic: true, MinPositiveBudget: 512, MaxBudget: 24576, JSONMIME: true, JSONSchema: true}
	case "gemini-2.5-pro":
		return GeminiCompatCapabilities{Budget: true, Dynamic: true, MinBudget: 128, MaxBudget: 32768, JSONMIME: true, JSONSchema: true}
	case "gemini-3-pro", "gemini-3-pro-preview":
		return GeminiCompatCapabilities{ThinkingLevels: []string{"low", "high"}, Budget: true, JSONMIME: true, JSONSchema: true}
	case "gemini-3.1-pro", "gemini-3.1-pro-preview":
		return GeminiCompatCapabilities{ThinkingLevels: []string{"low", "medium", "high"}, Budget: true, JSONMIME: true, JSONSchema: true}
	case "gemini-3-flash", "gemini-3-flash-preview", "gemini-3.5-flash":
		return GeminiCompatCapabilities{ThinkingLevels: []string{"minimal", "low", "medium", "high"}, Budget: true, JSONMIME: true, JSONSchema: true}
	default:
		return GeminiCompatCapabilities{}
	}
}

func recordGeminiConversion(opts apicompat.ConversionOptions, field, action string) {
	if opts.Record != nil {
		opts.Record(apicompat.ConversionNotice{Field: field, Action: action})
	}
}

func applyGeminiCompatThinkingConfig(claudeBody, body []byte, caps GeminiCompatCapabilities, opts apicompat.ConversionOptions) ([]byte, error) {
	kind := gjson.GetBytes(claudeBody, "thinking.type").String()
	effort := gjson.GetBytes(claudeBody, "output_config.effort").String()
	budgetValue := gjson.GetBytes(claudeBody, "thinking.budget_tokens")
	hasBudget := budgetValue.Type == gjson.Number
	disabled := kind == "disabled" || effort == "none"
	if !disabled && effort == "" && !hasBudget && kind != "adaptive" {
		return body, nil
	}
	field := "thinking"
	if effort != "" && kind != "disabled" {
		field = "output_config.effort"
	}
	if !caps.Budget && len(caps.ThinkingLevels) == 0 {
		recordGeminiConversion(opts, field, "omit")
		return body, nil
	}
	thinking := make(map[string]any)
	if disabled {
		effort = "none"
	}
	if effort != "" {
		supported := caps.ThinkingLevels
		if len(supported) == 0 {
			supported = []string{"none", "low", "medium", "high"}
		}
		mapped, err := openai.NearestSupportedReasoningEffort(effort, supported)
		if err != nil {
			return nil, &apicompat.UnsupportedConversionError{Field: field, Reason: "unsupported reasoning effort"}
		}
		if mapped != effort {
			recordGeminiConversion(opts, field, "map")
		}
		if len(caps.ThinkingLevels) > 0 {
			thinking["thinkingLevel"] = mapped
		} else {
			budget := map[string]int{"none": 0, "low": 1024, "medium": 8192, "high": caps.MaxBudget}[mapped]
			bounded := max(budget, caps.MinBudget)
			if bounded != budget {
				recordGeminiConversion(opts, field, "map")
			}
			thinking["thinkingBudget"] = bounded
		}
	} else if hasBudget {
		budget := int(budgetValue.Int())
		if budget == -1 && caps.Dynamic {
			thinking["thinkingBudget"] = -1
		} else if budget >= 0 && caps.Budget {
			bounded := max(budget, caps.MinBudget)
			if budget > 0 {
				bounded = max(bounded, caps.MinPositiveBudget)
			}
			if caps.MaxBudget > 0 {
				bounded = min(bounded, caps.MaxBudget)
			}
			if bounded != budget {
				recordGeminiConversion(opts, "thinking.budget_tokens", "map")
			}
			thinking["thinkingBudget"] = bounded
		} else {
			recordGeminiConversion(opts, "thinking.budget_tokens", "omit")
		}
	} else if kind == "adaptive" && caps.Dynamic {
		thinking["thinkingBudget"] = -1
	} else {
		recordGeminiConversion(opts, "thinking", "omit")
	}
	if disabled {
		thinking["includeThoughts"] = false
	} else if kind == "enabled" || kind == "adaptive" {
		thinking["includeThoughts"] = true
	}
	if len(thinking) == 0 {
		return body, nil
	}
	return sjson.SetBytes(body, "generationConfig.thinkingConfig", thinking)
}

func applyGeminiCompatResponseFormat(claudeBody, body []byte, caps GeminiCompatCapabilities, opts apicompat.ConversionOptions) ([]byte, error) {
	format := gjson.GetBytes(claudeBody, "output_config.format")
	if !format.Exists() || format.Type == gjson.Null {
		return body, nil
	}
	kind := format.Get("type").String()
	if !format.IsObject() || (kind != "json_schema" && kind != "json_object" && kind != "text") {
		return nil, &apicompat.UnsupportedConversionError{Field: "output_config.format", Reason: "expected text, json_object or json_schema"}
	}
	if kind == "text" {
		return body, nil
	}
	schema := format.Get("schema")
	if kind == "json_schema" && !schema.IsObject() && schema.Type != gjson.True && schema.Type != gjson.False {
		return nil, &apicompat.UnsupportedConversionError{Field: "output_config.format.schema", Reason: "expected a JSON schema"}
	}
	if !caps.JSONMIME {
		recordGeminiConversion(opts, "output_config.format", "omit")
		return body, nil
	}
	body, err := sjson.SetBytes(body, "generationConfig.responseMimeType", "application/json")
	if err != nil || kind != "json_schema" {
		return body, err
	}
	if !caps.JSONSchema {
		recordGeminiConversion(opts, "output_config.format.schema", "omit")
		return body, nil
	}
	return sjson.SetRawBytes(body, "generationConfig.responseJsonSchema", []byte(schema.Raw))
}

func applyGeminiCompatToolChoice(claudeBody, body []byte, opts apicompat.ConversionOptions) ([]byte, error) {
	choice := gjson.GetBytes(claudeBody, "tool_choice")
	if !choice.Exists() || choice.Type == gjson.Null {
		return body, nil
	}
	invalid := func(field, reason string) ([]byte, error) {
		return nil, &apicompat.UnsupportedConversionError{Field: field, Reason: reason}
	}
	if !choice.IsObject() || choice.Get("type").Type != gjson.String {
		return invalid("tool_choice", "expected a tool choice object with a type")
	}
	if parallel := choice.Get("disable_parallel_tool_use"); parallel.Exists() {
		if parallel.Type != gjson.True && parallel.Type != gjson.False {
			return invalid("tool_choice.disable_parallel_tool_use", "expected a boolean")
		}
		if parallel.Bool() && opts.Record != nil {
			opts.Record(apicompat.ConversionNotice{Field: "tool_choice.disable_parallel_tool_use", Action: "omit"})
		}
	}
	mode := ""
	var names []string
	switch choice.Get("type").String() {
	case "auto":
		mode = "AUTO"
	case "none":
		mode = "NONE"
		// Remove builtin declarations that could still start a call. Historical
		// functionCall/functionResponse parts in contents are untouched.
		var tools []json.RawMessage
		for _, tool := range gjson.GetBytes(body, "tools").Array() {
			if declarations := tool.Get("functionDeclarations"); declarations.Exists() {
				tools = append(tools, json.RawMessage(`{"functionDeclarations":`+declarations.Raw+`}`))
			}
		}
		var err error
		if len(tools) == 0 {
			body, err = sjson.DeleteBytes(body, "tools")
		} else {
			body, err = sjson.SetBytes(body, "tools", tools)
		}
		if err != nil {
			return nil, err
		}
	case "any":
		mode = "ANY"
	case "tool":
		name := choice.Get("name")
		if name.Type != gjson.String || strings.TrimSpace(name.String()) == "" {
			return invalid("tool_choice.name", "expected a declared tool name")
		}
		declared, builtin := false, false
		for _, tool := range gjson.GetBytes(claudeBody, "tools").Array() {
			if tool.Get("name").String() == name.String() {
				declared = true
				kind := tool.Get("type").String()
				builtin = strings.HasPrefix(kind, "web_search") || kind == "google_search"
				break
			}
		}
		if !declared {
			return invalid("tool_choice.name", "tool is not declared in tools")
		}
		if builtin {
			if opts.Record != nil {
				opts.Record(apicompat.ConversionNotice{Field: "tool_choice", Action: "map"})
			}
			return body, nil
		}
		mode, names = "ANY", []string{name.String()}
	default:
		return invalid("tool_choice.type", "expected auto, none, any or tool")
	}
	calling := map[string]any{"mode": mode}
	if len(names) > 0 {
		calling["allowedFunctionNames"] = names
	}
	return sjson.SetBytes(body, "toolConfig.functionCallingConfig", calling)
}

func pruneGeminiEmptyEnums(schema map[string]any, path string) []string {
	var omitted []string
	remove := func(keyword string) {
		if _, exists := schema[keyword]; exists {
			delete(schema, keyword)
			omitted = append(omitted, path+"."+keyword)
		}
	}
	for _, keyword := range []string{"not", "oneOf"} {
		if hasGeminiEmptyEnum(schema[keyword]) {
			remove(keyword)
		}
	}
	if hasGeminiEmptyEnum(schema["if"]) || hasGeminiEmptyEnum(schema["then"]) || hasGeminiEmptyEnum(schema["else"]) {
		for _, keyword := range []string{"if", "then", "else"} {
			remove(keyword)
		}
	}
	if hasGeminiEmptyEnum(schema["contains"]) {
		for _, keyword := range []string{"contains", "minContains", "maxContains"} {
			remove(keyword)
		}
	}
	if values, ok := schema["enum"].([]any); ok {
		kept := make([]any, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); !ok || text != "" {
				kept = append(kept, value)
			}
		}
		if len(kept) == 0 {
			remove("enum")
		} else if len(kept) != len(values) {
			schema["enum"] = kept
			omitted = append(omitted, path+".enum")
		}
	}
	visitGeminiSchemaChildren(schema, func(child any, suffix string) {
		if object, ok := child.(map[string]any); ok {
			omitted = append(omitted, pruneGeminiEmptyEnums(object, path+suffix)...)
		}
	})
	return omitted
}

func hasGeminiEmptyEnum(schema any) bool {
	if branches, ok := schema.([]any); ok {
		for _, branch := range branches {
			if hasGeminiEmptyEnum(branch) {
				return true
			}
		}
		return false
	}
	object, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	if values, ok := object["enum"].([]any); ok {
		if len(values) == 0 {
			return true
		}
		for _, value := range values {
			if text, ok := value.(string); ok && text == "" {
				return true
			}
		}
	}
	found := false
	visitGeminiSchemaChildren(object, func(child any, _ string) {
		found = found || hasGeminiEmptyEnum(child)
	})
	return found
}

func visitGeminiSchemaChildren(schema map[string]any, visit func(any, string)) {
	for _, keyword := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		if container, ok := schema[keyword].(map[string]any); ok {
			names := make([]string, 0, len(container))
			for name := range container {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				visit(container[name], "."+keyword+"."+name)
			}
		}
	}
	for _, keyword := range []string{"items", "prefixItems", "anyOf", "allOf", "oneOf"} {
		if children, ok := schema[keyword].([]any); ok {
			for index, child := range children {
				visit(child, fmt.Sprintf(".%s[%d]", keyword, index))
			}
		} else if keyword == "items" {
			visit(schema[keyword], "."+keyword)
		}
	}
	for _, keyword := range []string{"additionalProperties", "propertyNames", "not", "if", "then", "else", "contains"} {
		visit(schema[keyword], "."+keyword)
	}
}
