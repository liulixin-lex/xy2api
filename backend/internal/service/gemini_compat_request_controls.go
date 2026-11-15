package service

import (
	"context"
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
