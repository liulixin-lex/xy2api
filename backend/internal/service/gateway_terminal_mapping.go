package service

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

func recordConvertedTerminalMapping(ctx context.Context, source, target, status, reason string) {
	_, _, mapped, err := apicompat.ProjectTerminalWithMapping(source, target, status, reason, true, false)
	if err != nil || !mapped {
		return
	}
	field := "finish_reason"
	if source == "anthropic" {
		field = "stop_reason"
	}
	if source == "responses" {
		field = "incomplete_details.reason"
	}
	resilience.RecordConversionNotices(ctx, apicompat.ConversionNotice{Field: field, Action: "map"})
}

func recordAnthropicTerminalMapping(ctx context.Context, payload string) {
	if reason := gjson.Get(payload, "delta.stop_reason"); reason.Type == gjson.String && reason.Str != "" {
		recordConvertedTerminalMapping(ctx, "anthropic", "responses", "", reason.Str)
	}
}
