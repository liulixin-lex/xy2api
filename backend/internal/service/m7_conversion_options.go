package service

import (
	"context"
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

func m7ConversionOptions(ctx context.Context) apicompat.ConversionOptions {
	return apicompat.ConversionOptions{Record: func(notice apicompat.ConversionNotice) {
		resilience.RecordConversionNotices(ctx, notice)
	}}
}

// Each API-key bridge keeps one mutable identity table for its entire attempt.
func m7BridgeConversionOptions(ctx context.Context, account *Account) apicompat.ConversionOptions {
	opts := m7ConversionOptions(ctx)
	if account != nil && account.Type == AccountTypeAPIKey && account.Platform != PlatformGrok {
		opts.Bridge = &apicompat.BridgeOptions{Enabled: true}
	}
	return opts
}

func m7SelectedConversionOptions(options []apicompat.ConversionOptions) apicompat.ConversionOptions {
	if len(options) > 0 {
		return options[0]
	}
	return apicompat.ConversionOptions{}
}

func m7ResponsesToAnthropicRequest(req *apicompat.ResponsesRequest, opts apicompat.ConversionOptions) (*apicompat.AnthropicRequest, error) {
	if opts.Bridge == nil {
		return apicompat.ResponsesToAnthropicRequest(req)
	}
	return apicompat.ResponsesToAnthropicRequestWithOptions(req, opts)
}

func m7ForwardChatMetadata(req *apicompat.ResponsesRequest, out *apicompat.AnthropicRequest, opts apicompat.ConversionOptions) {
	if opts.Bridge == nil {
		return
	}
	if userID := gjson.GetBytes(req.Metadata, "user_id"); userID.Type == gjson.String {
		out.Metadata, _ = json.Marshal(map[string]string{"user_id": userID.String()})
	}
}

func m7RestoreBridgeResponse(resp *apicompat.ResponsesResponse, opts apicompat.ConversionOptions) error {
	if opts.Bridge == nil {
		return nil
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	raw, _, err = apicompat.RestoreResponsesClientToolPayload(raw, apicompat.ResponsesClientToolMapping{Bridge: opts.Bridge})
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, resp)
}

func m7BridgeStreamRestorer(opts apicompat.ConversionOptions) *apicompat.ResponsesClientToolStreamRestorer {
	if opts.Bridge == nil {
		return nil
	}
	return apicompat.NewResponsesClientToolStreamRestorer(apicompat.ResponsesClientToolMapping{Bridge: opts.Bridge})
}

func m7RestoreBridgeEvents(events []apicompat.ResponsesStreamEvent, restorer *apicompat.ResponsesClientToolStreamRestorer) []apicompat.ResponsesStreamEvent {
	if restorer == nil {
		return events
	}
	var out []apicompat.ResponsesStreamEvent
	for _, event := range events {
		out = append(out, restorer.Restore(event)...)
	}
	return out
}

func m7RecordLegacyChatTerminal(ctx context.Context, raw []byte, opts apicompat.ConversionOptions) {
	if opts.Bridge != nil {
		return
	}
	for _, choice := range gjson.GetBytes(raw, "choices").Array() {
		if reason := choice.Get("finish_reason"); reason.Type == gjson.String && reason.Str != "" {
			recordConvertedTerminalMapping(ctx, "chat", "responses", "", reason.Str)
		}
	}
}
